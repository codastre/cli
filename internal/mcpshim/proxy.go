// Package mcpshim implements the stdio MCP proxy (impl-spec §2.7).
// It reads newline-delimited JSON-RPC from stdin, forwards each message to the
// server's MCP HTTP endpoint with the auth header, and writes the response to stdout.
// QUERY responses have their path_tokens unmasked and snippets hydrated from disk
// (see query.go and snippet.go). GRAPH responses have their src/dst path_tokens
// and evidence file_path_tokens unmasked (see graph.go).
package mcpshim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	// MaxSnippetLines caps how many lines each hydrated snippet may carry.
	// Zero → defaultMaxSnippetLines. Hydration is where a QUERY response's cost
	// is incurred, so this is the proxy's cost dial; see snippet.go.
	MaxSnippetLines int
	// NoSnippets skips hydration entirely, returning ranked locations only. For
	// cheap orientation queries where the paths are the answer.
	NoSnippets bool
	// Format is how a QUERY or GRAPH result is encoded: "json" (default, the
	// server's envelope enriched in place) or "agent" (a text rendering — see
	// render.go and render_graph.go). Empty is "json".
	Format string
	// Log, when non-nil, receives one human-readable cost line per QUERY
	// response. Must NOT be stdout: that carries the JSON-RPC stream.
	Log io.Writer
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
	// Hydration was switched off by the operator (`serve --no-snippets`). Not a
	// failure: the paths and spans are complete and the agent should Read what it
	// needs rather than trying to repair anything.
	hydrationSnippetsDisabled = "snippets_disabled"
)

// HydrationSnippetsDisabled is hydrationSnippetsDisabled for callers outside this
// package. `codastre query`'s renderer needs it for the same reason the agent
// rendering does: it is the one reason the header already stated, so repeating it
// per hit restates the mode instead of saying anything.
const HydrationSnippetsDisabled = hydrationSnippetsDisabled

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
		// Client-only hydration arguments are consumed here and stripped from
		// the request: they have no server-side counterpart and would fail the
		// QUERY tool's schema validation. See overrides.go.
		line, ov := takeCallOverrides(line)
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
			// tools/list is annotated with the client-only hydration arguments
			// so agents can discover them (see toolschema.go); tool results are
			// unmasked and hydrated.
			resp = annotateToolList(cfg, resp)
			resp = enrichResponse(ov.apply(cfg), resp)
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
	enriched, rendering, ok := enrichToolPayload(cfg, result)
	if !ok {
		return data
	}
	if rendering != "" {
		// Agent format: the rendering goes in the content block, and
		// structuredContent carries a summary rather than a second copy of it.
		// QUERY declares an outputSchema, so a spec-strict client is entitled to
		// structuredContent — but it is an open object, so what it is entitled to
		// is an object, not the answer twice. See AgentSummary in render.go for
		// the trade this makes and who loses it.
		result["structuredContent"], _ = json.Marshal(agentSummary(enriched))
		setContentText(result, []byte(rendering))
	} else {
		result["structuredContent"] = enriched
		setContentText(result, enriched)
	}
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
// the first text content block. Returns (enriched, rendering, true) when a known
// payload was found and processed; rendering is non-empty only for a QUERY
// payload in agent format, and is then what both representations should carry.
func enrichToolPayload(cfg Config, result map[string]json.RawMessage) ([]byte, string, bool) {
	payload, ok := toolPayloadBytes(result)
	if !ok {
		return nil, "", false
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, "", false
	}
	if _, ok := env["results"]; ok {
		enriched, results, acct := enrichQueryPayload(cfg, payload)
		if cfg.Format == formatAgent {
			// A rendering that failed to build falls back to the JSON: half an
			// answer in the caller's preferred shape is worse than the whole
			// answer in the other one.
			if text, ok := renderQueryText(enriched, RenderOptions{NoSnippets: cfg.NoSnippets}); ok {
				acct.report(cfg, results, len(text))
				return enriched, text, true
			}
		}
		acct.report(cfg, results, len(enriched))
		return enriched, "", true
	}
	if _, ok := env["edges"]; ok {
		enriched := enrichGraphResponse(cfg, payload)
		if cfg.Format == formatAgent {
			// Same fallback rule as QUERY: half an answer in the caller's
			// preferred shape is worse than the whole answer in the other one.
			if text, ok := renderGraphText(enriched, RenderOptions{}); ok {
				return enriched, text, true
			}
		}
		return enriched, "", true
	}
	return nil, "", false
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
