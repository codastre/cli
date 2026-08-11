// Package mcpshim implements the stdio MCP proxy (impl-spec §2.7).
// It reads newline-delimited JSON-RPC from stdin, forwards each message to the
// server's MCP HTTP endpoint with the auth header, and writes the response to stdout.
// QUERY responses have their path_tokens unmasked and snippets hydrated from disk.
// GRAPH responses have their src/dst path_tokens and evidence file_path_tokens unmasked.
package mcpshim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config configures the MCP proxy.
type Config struct {
	ServerURL string
	APIKey    string
	RepoRoot  string
	// UnmaskPath, if non-nil, converts a path_token to the real path for a given mask_key_rev.
	UnmaskPath func(pathToken string, maskKeyRev int) (string, bool)
	// RepoScheme returns a repo's masking scheme ("hmac" | "none") by repo_id, when
	// known. It lets the proxy hydrate `none`-scheme (cleartext) repos — where the
	// path_token already IS the real repo-relative path — without an UnmaskPath.
	// Snippet hydration only needs a repo root, not masking; gating it on unmasking
	// wrongly starved cleartext repos of the inline snippets that are the proxy's
	// whole value-add. Nil → schemes unknown (legacy: only UnmaskPath-backed hmac
	// repos are enriched).
	RepoScheme func(repoID string) (string, bool)
	// RepoRootFor returns the local checkout root for a repo_id, so a FEDERATED hit
	// from a repo other than the CWD one can still be hydrated. A miss means "no
	// checkout known for this repo" and the result is left unhydrated — see
	// rootFor for why falling back to RepoRoot would be wrong.
	RepoRootFor func(repoID string) (string, bool)
	// CWDRepoID is the repo_id of the repo the CLI is running inside, when it is
	// registered. It is the ONLY repo for which RepoRoot is a sound hydration
	// root; see rootFor.
	CWDRepoID string
	// RepoRemoteURL returns a repo's remote URL by repo_id. GRAPH's server-side
	// repos map carries only {masking_scheme, mask_key_rev} — no URL — so a
	// federated edge identifies its endpoints by bare UUID and an agent cannot name
	// the service without extra calls. The proxy already holds this mapping for
	// hydration, so it backfills remote_url locally. Nil → no backfill.
	RepoRemoteURL func(repoID string) (string, bool)
}

// Reasons emitted on a QUERY result as `hydration` when no snippet could be
// produced. Absence of the field means the snippet is present (or the payload
// predates this proxy). They are stable strings an agent can branch on.
const (
	// No local checkout is known for the result's repo. The remedy is external to
	// this machine's state: clone the repo, or read the file from the forge API.
	// This is the common federated case and the only one the agent can't fix by
	// re-running anything locally.
	hydrationNoCheckout = "no_local_checkout"
	// A checkout is known but the file isn't at that path in it — indexed at a ref
	// the checkout lacks, or moved/deleted since. Usually fixed by fetching.
	hydrationFileMissing = "file_not_found_in_checkout"
	// The file exists but couldn't be read (permissions, I/O, decode).
	hydrationReadError = "read_error"
	// The file was read but the result's line range isn't in it — the checkout is
	// at a ref where the file is shorter, or the envelope carried no usable
	// line_end. Distinct from hydrationFileMissing: the path is right, the range
	// isn't. Re-querying at the checked-out ref (or fetching) is the fix.
	hydrationRangeMissing = "line_range_not_in_file"
	// The path_token could not be unmasked: an hmac repo whose masking key isn't
	// in this machine's keychain — the common case for a federated hit from a repo
	// the developer has no key for. Nothing local can be read, since even the real
	// path is unknown. `codastre masking-key --repo-url …` is the fix when the dev
	// is entitled to the repo.
	hydrationUnmaskFailed = "path_unmask_failed"
)

// canEnrich reports whether the proxy has any way to produce real paths /
// snippets: an unmasker (hmac repos) or scheme knowledge (cleartext repos).
func (cfg Config) canEnrich() bool {
	return cfg.UnmaskPath != nil || cfg.RepoScheme != nil
}

// unmaskOrIdentity maps a path_token to its real path. For a `none`-scheme repo
// the token is already cleartext, so the mapping is the identity — no key, no
// unmasker needed. For hmac (or unknown-scheme) repos it defers to UnmaskPath.
func (cfg Config) unmaskOrIdentity(pathToken, repoID string, rev int) (string, bool) {
	if cfg.RepoScheme != nil {
		if s, ok := cfg.RepoScheme(repoID); ok && s == "none" {
			return pathToken, true
		}
	}
	if cfg.UnmaskPath == nil {
		return "", false
	}
	return cfg.UnmaskPath(pathToken, rev)
}

// rootFor returns the local checkout root to hydrate a repo's file from, or ""
// when no checkout is known for repoID.
//
// Returning "" (rather than falling back to RepoRoot) is the whole point. The
// CWD checkout is a valid hydration root for exactly one repo — the one the CLI
// is running inside. Using it for any other repo joins that repo's path onto
// this tree, and because files like CLAUDE.md / README.md / Makefile exist in
// most repos, the read SUCCEEDS and returns real but wrong content under a
// correct-looking path. That is worse than returning nothing: it is confidently
// wrong, and the staleness check can't catch it when the envelope carries no
// blob_sha.
func (cfg Config) rootFor(repoID string) string {
	if cfg.RepoRootFor != nil {
		if r, ok := cfg.RepoRootFor(repoID); ok && r != "" {
			return r
		}
	}
	// Sound only when repoID IS the CWD repo. When CWDRepoID is unset we cannot
	// prove that, so hydration is skipped rather than guessed.
	if cfg.CWDRepoID != "" && repoID == cfg.CWDRepoID {
		return cfg.RepoRoot
	}
	// Legacy single-repo path: no per-repo lookup at all. Hydration is then
	// gated behind UnmaskPath, which is keyed to the CWD repo's masking key, so
	// a foreign repo's token does not unmask and never reaches here.
	if cfg.RepoRootFor == nil && cfg.RepoScheme == nil {
		return cfg.RepoRoot
	}
	return ""
}

// Run reads JSON-RPC messages from in, proxies them to the server, and writes
// responses to out. Blocks until in is closed or a read error occurs.
func Run(cfg Config, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, err := forwardMessage(cfg, line)
		if err != nil {
			resp = errorEnvelope(line, err)
		} else {
			// A notification (no id) is acked by the server with 202 and an
			// empty body; it has no JSON-RPC response, so emit nothing rather
			// than a stray blank line the client would have to skip.
			if len(bytes.TrimSpace(resp)) == 0 {
				continue
			}
			resp = enrichResponse(cfg, resp)
		}
		fmt.Fprintf(out, "%s\n", resp)
	}
	return sc.Err()
}

func forwardMessage(cfg Config, body []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// FastMCP's streamable-HTTP transport validates Accept and requires BOTH
	// media types even when the server is configured for JSON responses
	// (stateless_http=True, json_response=True). Omitting this yields a 406 that
	// surfaces to the agent as a -32000 "failed to connect" error.
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, data)
	}
	return data, nil
}

// enrichResponse unmasks a QUERY/GRAPH payload. MCP tool results arrive wrapped
// in a JSON-RPC envelope ({"result":{"structuredContent":{…},"content":[{"type":
// "text","text":"<json>"}]}}); the payload may also appear bare (un-enveloped).
// Returns data unchanged when no known payload is found.
func enrichResponse(cfg Config, data []byte) []byte {
	if !cfg.canEnrich() {
		return data
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	// Bare payload (e.g. a direct REST shape or unit-test input).
	if _, ok := env["results"]; ok {
		return enrichQueryResponse(cfg, data)
	}
	if _, ok := env["edges"]; ok {
		return enrichGraphResponse(cfg, data)
	}

	// JSON-RPC tool-result envelope: the QUERY/GRAPH payload is nested under
	// result.structuredContent and mirrored as a JSON string in
	// result.content[0].text. Enrich the inner payload, then write it back to
	// both so agents reading either representation see the unmasked paths.
	resultRaw, ok := env["result"]
	if !ok {
		return data
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return data
	}
	enriched, ok := enrichToolPayload(cfg, result)
	if !ok {
		return data
	}
	result["structuredContent"] = enriched
	setContentText(result, enriched)
	resultB, err := json.Marshal(result)
	if err != nil {
		return data
	}
	env["result"] = resultB
	out, err := json.Marshal(env)
	if err != nil {
		return data
	}
	return out
}

// enrichToolPayload enriches the QUERY/GRAPH payload inside a tool result,
// preferring result.structuredContent and falling back to the JSON string in
// the first text content block. Returns (enriched, true) when a known payload
// was found and processed.
func enrichToolPayload(cfg Config, result map[string]json.RawMessage) ([]byte, bool) {
	payload, ok := toolPayloadBytes(result)
	if !ok {
		return nil, false
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, false
	}
	if _, ok := env["results"]; ok {
		return enrichQueryResponse(cfg, payload), true
	}
	if _, ok := env["edges"]; ok {
		return enrichGraphResponse(cfg, payload), true
	}
	return nil, false
}

// toolPayloadBytes returns the inner tool payload JSON: structuredContent when
// present, else the decoded text of the first text content block.
func toolPayloadBytes(result map[string]json.RawMessage) ([]byte, bool) {
	if sc, ok := result["structuredContent"]; ok && len(sc) > 0 && string(sc) != "null" {
		return sc, true
	}
	return firstContentText(result)
}

// firstContentText decodes the JSON string in the first type=="text" content block.
func firstContentText(result map[string]json.RawMessage) ([]byte, bool) {
	raw, ok := result["content"]
	if !ok {
		return nil, false
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, false
	}
	for _, block := range content {
		if blockType(block) != "text" {
			continue
		}
		var s string
		if err := json.Unmarshal(block["text"], &s); err != nil {
			return nil, false
		}
		return []byte(s), true
	}
	return nil, false
}

// setContentText writes enriched (a JSON payload) back into the first text
// content block as a JSON string.
func setContentText(result map[string]json.RawMessage, enriched []byte) {
	raw, ok := result["content"]
	if !ok {
		return
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &content); err != nil {
		return
	}
	for i, block := range content {
		if blockType(block) != "text" {
			continue
		}
		block["text"], _ = json.Marshal(string(enriched))
		content[i] = block
		break
	}
	result["content"], _ = json.Marshal(content)
}

func blockType(block map[string]json.RawMessage) string {
	var t string
	if raw, ok := block["type"]; ok {
		_ = json.Unmarshal(raw, &t)
	}
	return t
}

// enrichQueryResponse unmasks path_tokens and hydrates snippets in QUERY responses.
func enrichQueryResponse(cfg Config, data []byte) []byte {
	if !cfg.canEnrich() {
		return data
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	resultsRaw, ok := env["results"]
	if !ok {
		return data
	}

	var results []map[string]json.RawMessage
	if err := json.Unmarshal(resultsRaw, &results); err != nil {
		return data
	}

	// The index-free (federated) QUERY path returns mask_key_rev: null and puts
	// the authoritative version in the per-repo map mask_key_revs. Resolve each
	// result at its own repo's rev so a key rotation is honoured; fall back to
	// the singular field (Mode A, single-index queries).
	var maskKeyRev int
	if raw, ok := env["mask_key_rev"]; ok {
		_ = json.Unmarshal(raw, &maskKeyRev)
	}
	maskKeyRevs := map[string]int{}
	if raw, ok := env["mask_key_revs"]; ok {
		_ = json.Unmarshal(raw, &maskKeyRevs)
	}

	for i, r := range results {
		var pathToken string
		if raw, ok := r["path_token"]; ok {
			_ = json.Unmarshal(raw, &pathToken)
		}
		rev := maskKeyRev
		var repoID string
		if raw, ok := r["repo_id"]; ok {
			_ = json.Unmarshal(raw, &repoID)
		}
		if v, ok := maskKeyRevs[repoID]; ok {
			rev = v
		}
		// hmac → inverse-HMAC via UnmaskPath; none → identity (token is cleartext).
		realPath, ok := cfg.unmaskOrIdentity(pathToken, repoID, rev)
		if !ok {
			// No real path, so no snippet either — but the result must still say
			// why, or it lands in exactly the ambiguous state the `hydration`
			// field exists to remove (see the invariant above).
			r["hydration"], _ = json.Marshal(hydrationUnmaskFailed)
			results[i] = r
			continue
		}
		r["real_path"], _ = json.Marshal(realPath)

		// Snippet hydration (impl-spec §2.7). Independent of masking — it needs
		// only a local checkout root, resolved per-repo so federated hits hydrate
		// too. When hydration can't happen the result still carries real_path, plus
		// a `hydration` reason so the agent can act instead of guessing: a missing
		// snippet is otherwise indistinguishable from "this repo isn't checked out",
		// "the file moved", and "the read failed" — three cases with three fixes.
		root := cfg.rootFor(repoID)
		if root == "" {
			r["hydration"], _ = json.Marshal(hydrationNoCheckout)
		} else {
			var lineStart, lineEnd int
			var blobSHA string
			if raw, ok := r["line_start"]; ok {
				_ = json.Unmarshal(raw, &lineStart)
			}
			if raw, ok := r["line_end"]; ok {
				_ = json.Unmarshal(raw, &lineEnd)
			}
			if raw, ok := r["blob_sha"]; ok {
				_ = json.Unmarshal(raw, &blobSHA)
			}

			absPath := filepath.Join(root, realPath)
			res, err := hydrateSnippet(absPath, lineStart, lineEnd, blobSHA)
			switch {
			case err == nil && res.Lines == 0:
				// File read fine, but the range wasn't in it. Emitting
				// `"snippet": ""` here would look like a successful hydration of an
				// empty region, so report the reason instead.
				r["hydration"], _ = json.Marshal(hydrationRangeMissing)
			case err == nil:
				r["snippet"], _ = json.Marshal(res.Text)
				if res.Stale {
					r["stale"], _ = json.Marshal(true)
				}
			case os.IsNotExist(err):
				// Checkout known, file absent: indexed at a ref this checkout
				// doesn't have, or deleted/moved since. Pulling usually fixes it.
				r["hydration"], _ = json.Marshal(hydrationFileMissing)
			default:
				r["hydration"], _ = json.Marshal(hydrationReadError)
			}
		}
		results[i] = r
	}

	enriched, _ := json.Marshal(results)
	env["results"] = enriched
	out, _ := json.Marshal(env)
	return out
}

// enrichGraphResponse unmasks src/dst path_tokens and evidence file_path_tokens
// in GRAPH responses. For each edge:
//   - src["path_token"] -> src["real_path"] (if UnmaskPath returns ok)
//   - dst["path_token"] -> dst["real_path"] (if UnmaskPath returns ok)
//   - evidence["file_path_token"] -> evidence["real_file_path"] (if present and ok)
//
// Each endpoint is unmasked at its OWN repo's mask_key_rev, read from the
// response's repos map ({repo_id: {masking_scheme, mask_key_rev}}). The old
// hardcoded rev 0 silently wrong-unmasked rotated hmac repos (corpus-hygiene
// plan API3); repos absent from the map (or a missing map) still fall back to 0.
func enrichGraphResponse(cfg Config, data []byte) []byte {
	if !cfg.canEnrich() {
		return data
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	edgesRaw, ok := env["edges"]
	if !ok {
		return data
	}

	// Each element is a graph_edge_result: {edge, src, dst, count, evidence?}
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(edgesRaw, &edges); err != nil {
		return data
	}

	// repo_id -> mask_key_rev from the envelope's repos map.
	repoRevs := map[string]int{}
	if reposRaw, ok := env["repos"]; ok {
		var repos map[string]struct {
			MaskKeyRev int `json:"mask_key_rev"`
		}
		if err := json.Unmarshal(reposRaw, &repos); err == nil {
			for rid, info := range repos {
				repoRevs[rid] = info.MaskKeyRev
			}
		}
	}

	// unmaskNode unmasks node[tokenField] into node[realField] at the node's
	// own repo rev; returns the repo's rev for reuse (evidence rides src's repo).
	unmaskNode := func(e map[string]json.RawMessage, key, tokenField, realField string) int {
		rev := 0
		raw, ok := e[key]
		if !ok {
			return rev
		}
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			return rev
		}
		repoID, _ := unmarshalString(node["repo_id"])
		if r, ok := repoRevs[repoID]; ok {
			rev = r
		}
		if tok, ok := unmarshalString(node[tokenField]); ok {
			// hmac → UnmaskPath; none → identity (token is the real path).
			if realPath, ok := cfg.unmaskOrIdentity(tok, repoID, rev); ok {
				node[realField], _ = json.Marshal(realPath)
				e[key], _ = json.Marshal(node)
			}
		}
		return rev
	}

	for i, e := range edges {
		srcRev := unmaskNode(e, "src", "path_token", "real_path")
		unmaskNode(e, "dst", "path_token", "real_path")

		// Unmask evidence.file_path_token -> evidence.real_file_path (optional
		// field). Evidence is the SRC side's call site, so it unmasks at the src
		// repo's rev and scheme (identity for a `none`-scheme src repo).
		if evRaw, ok := e["evidence"]; ok {
			var ev map[string]json.RawMessage
			if err := json.Unmarshal(evRaw, &ev); err == nil {
				if tok, ok := unmarshalString(ev["file_path_token"]); ok {
					if realPath, ok := cfg.unmaskOrIdentity(tok, edgeNodeRepoID(e, "src"), srcRev); ok {
						ev["real_file_path"], _ = json.Marshal(realPath)
						e["evidence"], _ = json.Marshal(ev)
					}
				}
			}
		}

		edges[i] = e
	}

	enriched, _ := json.Marshal(edges)
	env["edges"] = enriched
	if repos, ok := backfillRepoURLs(cfg, env["repos"]); ok {
		env["repos"] = repos
	}
	out, _ := json.Marshal(env)
	return out
}

// backfillRepoURLs adds remote_url to each entry of a GRAPH repos map, which the
// server populates with only {masking_scheme, mask_key_rev}. Without it a
// federated edge names its endpoints by bare UUID, so "which service consumes
// this topic?" can't be answered from the response — the agent has to spend extra
// calls, or guess a service from a shared path like app/consumer.py. The proxy
// already holds repo_id → remote_url for hydration, so it fills the gap locally
// and matches QUERY's repos map, where remote_url is present.
//
// Existing keys are never overwritten: if a future server version starts sending
// remote_url, its value wins.
func backfillRepoURLs(cfg Config, reposRaw json.RawMessage) (json.RawMessage, bool) {
	if cfg.RepoRemoteURL == nil || len(reposRaw) == 0 {
		return nil, false
	}
	var repos map[string]map[string]json.RawMessage
	if err := json.Unmarshal(reposRaw, &repos); err != nil || len(repos) == 0 {
		return nil, false
	}
	changed := false
	for repoID, info := range repos {
		if info == nil {
			info = map[string]json.RawMessage{}
		}
		if _, exists := info["remote_url"]; exists {
			continue
		}
		url, ok := cfg.RepoRemoteURL(repoID)
		if !ok || url == "" {
			continue
		}
		info["remote_url"], _ = json.Marshal(url)
		repos[repoID] = info
		changed = true
	}
	if !changed {
		return nil, false
	}
	b, err := json.Marshal(repos)
	if err != nil {
		return nil, false
	}
	return b, true
}

// edgeNodeRepoID returns the repo_id of an edge's "src"/"dst" node, or "" when
// absent. Used to pick the masking scheme for evidence (which rides the src repo).
func edgeNodeRepoID(e map[string]json.RawMessage, key string) string {
	raw, ok := e[key]
	if !ok {
		return ""
	}
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	id, _ := unmarshalString(node["repo_id"])
	return id
}

// unmarshalString extracts a Go string from a json.RawMessage.
// Returns ("", false) if raw is nil or not a JSON string.
func unmarshalString(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// snippetResult is hydrateSnippet's outcome. Lines counts the lines actually
// read from the requested range, so the caller can tell "the range yielded
// nothing" (missing/zero line_end, or a checkout where the file is shorter than
// line_start) from "the range is genuinely blank lines" — the former is a
// hydration failure that needs a reason, not an empty success.
type snippetResult struct {
	Text  string
	Lines int
	Stale bool
}

// hydrateSnippet reads the inclusive, 1-based line range [lineStart, lineEnd]
// from the file and checks staleness. The server's chunk ranges are 1-based
// (see the proxy_query_test fixtures); a stray line_start of 0 is clamped to 1
// so the first line is included rather than skipped. (If a future server change
// switches to 0-based ranges, this is the one place to revisit.)
func hydrateSnippet(absPath string, lineStart, lineEnd int, blobSHA string) (snippetResult, error) {
	if lineStart < 1 {
		lineStart = 1
	}
	f, err := os.Open(absPath)
	if err != nil {
		return snippetResult{}, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var lines []string
	lineNum := 1
	for sc.Scan() {
		if lineNum >= lineStart && lineNum <= lineEnd {
			lines = append(lines, sc.Text())
		}
		if lineNum > lineEnd {
			break
		}
		lineNum++
	}
	if err := sc.Err(); err != nil {
		return snippetResult{}, err
	}
	if len(lines) == 0 {
		return snippetResult{}, nil
	}

	out := snippetResult{Text: strings.Join(lines, "\n"), Lines: len(lines)}

	// Staleness check: compare current blob hash to expected.
	if blobSHA != "" {
		current, err := currentBlobSHA(absPath)
		if err == nil && current != blobSHA {
			out.Stale = true
		}
	}

	return out, nil
}

func currentBlobSHA(absPath string) (string, error) {
	out, err := exec.Command("git", "hash-object", absPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// errorEnvelope extracts the JSON-RPC id from req and wraps err as a JSON-RPC error.
func errorEnvelope(req []byte, err error) []byte {
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(req, &msg)
	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      msg.ID,
		"error":   map[string]any{"code": -32000, "message": err.Error()},
	}
	b, _ := json.Marshal(env)
	return b
}
