package mcpshim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// queryResponse builds a QUERY envelope as the index-free (federated) path emits
// it: singular mask_key_rev is null; the authoritative version lives in the
// per-repo mask_key_revs map. Each result carries its repo_id.
func queryResponse(repoID, pathToken string, perRepoRev int) []byte {
	env := map[string]any{
		"status":        "ok",
		"freshness":     "fresh",
		"mask_key_rev":  nil,
		"mask_key_revs": map[string]any{repoID: perRepoRev},
		"results": []map[string]any{
			{
				"chunk_id":   "c1",
				"repo_id":    repoID,
				"path_token": pathToken,
				"line_start": 1,
				"line_end":   2,
			},
		},
	}
	b, _ := json.Marshal(env)
	return b
}

// After a key rotation the federated QUERY envelope reports the new rev only in
// mask_key_revs (mask_key_rev is null). The proxy must resolve each result at its
// repo's rev — not the singular field, which decodes to 0 and would make the
// resolver skip the key fetch and return the masked token verbatim.
func TestEnrichQueryResponse_UsesPerRepoRev(t *testing.T) {
	const repoID = "reviewfy-uuid"
	var gotRev int
	cfg := Config{
		RepoRoot: "/nonexistent", // snippet hydration fails harmlessly
		UnmaskPath: func(_ string, rev int) (string, bool) {
			gotRev = rev
			return "src/app/main.go", true
		},
	}

	out := enrichQueryResponse(cfg, queryResponse(repoID, "deadbeeftoken", 4))

	if gotRev != 4 {
		t.Fatalf("UnmaskPath called with rev %d, want 4 (from mask_key_revs)", gotRev)
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal enriched envelope: %v", err)
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(env["results"], &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if _, ok := results[0]["real_path"]; !ok {
		t.Error("result should carry real_path after unmasking at the correct rev")
	}
}

// The real MCP transport wraps the QUERY payload in a JSON-RPC envelope under
// result.structuredContent (mirrored as a JSON string in result.content[0].text).
// enrichResponse must unwrap it, unmask, and write the result back to BOTH
// representations — not bail because "results" isn't a top-level key.
func TestEnrichResponse_UnwrapsJSONRPCEnvelope(t *testing.T) {
	const repoID = "reviewfy-uuid"
	cfg := Config{
		RepoRoot: "/nonexistent",
		UnmaskPath: func(_ string, rev int) (string, bool) {
			if rev != 4 {
				t.Errorf("UnmaskPath called with rev %d, want 4", rev)
			}
			return "app/jobs/weekly_leaderboard_job.py", true
		},
	}

	inner := queryResponse(repoID, "deadbeeftoken", 4) // {status, results, mask_key_revs,…}
	envelope, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(inner)}},
			"structuredContent": json.RawMessage(inner),
			"isError":           false,
		},
	})

	out := enrichResponse(cfg, envelope)

	// Dig back into both representations and assert real_path is present.
	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var result map[string]json.RawMessage
	_ = json.Unmarshal(env["result"], &result)

	// structuredContent
	if !strings.Contains(string(result["structuredContent"]), "real_path") {
		t.Error("structuredContent should contain real_path after enrichment")
	}
	// content[0].text (a JSON string)
	var content []map[string]json.RawMessage
	_ = json.Unmarshal(result["content"], &content)
	var text string
	_ = json.Unmarshal(content[0]["text"], &text)
	if !strings.Contains(text, "real_path") {
		t.Error("content[0].text should contain real_path after enrichment")
	}
}

// EV2 (corpus-hygiene plan): a SCOPED, COMPACTED federated envelope — per-repo
// maps limited to result repos, searched_repos replaced by searched_repo_count,
// no legacy `kind` mirror, null optional fields omitted — must still unmask and
// hydrate. This pins the CLI contract the API2/API3 changes rely on.
func TestEnrichQueryResponse_ScopedCompactedEnvelope(t *testing.T) {
	const repoID = "repo-in-results"
	var gotRev int
	cfg := Config{
		RepoRoot: "/nonexistent", // snippet hydration fails harmlessly
		UnmaskPath: func(_ string, rev int) (string, bool) {
			gotRev = rev
			return "app/consumer.py", true
		},
	}
	env := map[string]any{
		"status":              "ok",
		"freshness":           "fresh",
		"mask_key_rev":        nil,
		"searched_repo_count": 21,
		"repo_freshness":      map[string]any{repoID: "fresh"},
		"mask_key_revs":       map[string]any{repoID: 3}, // scoped: result repos only
		"repos":               map[string]any{repoID: map[string]any{"remote_url": "github.com/acme/billing"}},
		"filter_matched":      true,
		"results": []map[string]any{
			{
				"chunk_id":     "c1",
				"repo_id":      repoID,
				"path_token":   "tok",
				"line_start":   1,
				"line_end":     2,
				"score":        0.7,
				"content_kind": "code", // no `kind` mirror, no null symbol_name/title
			},
		},
	}
	b, _ := json.Marshal(env)

	out := enrichQueryResponse(cfg, b)

	if gotRev != 3 {
		t.Fatalf("UnmaskPath called with rev %d, want 3 (scoped mask_key_revs)", gotRev)
	}
	var enriched map[string]json.RawMessage
	if err := json.Unmarshal(out, &enriched); err != nil {
		t.Fatalf("unmarshal enriched envelope: %v", err)
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(enriched["results"], &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if _, ok := results[0]["real_path"]; !ok {
		t.Error("scoped compacted envelope should still unmask to real_path")
	}
	// Round-tripped envelope fields survive untouched.
	if string(enriched["searched_repo_count"]) != "21" {
		t.Errorf("searched_repo_count round-trip = %s", enriched["searched_repo_count"])
	}
	if string(enriched["filter_matched"]) != "true" {
		t.Errorf("filter_matched round-trip = %s", enriched["filter_matched"])
	}
}

// TestEnrichQueryResponse_NoneSchemeHydratesFromCheckout: a `none`-scheme
// (cleartext) repo has NO UnmaskPath. With RepoScheme reporting "none" and
// RepoRootFor pointing at a local checkout, the proxy must (a) identity-map
// path_token → real_path and (b) hydrate the snippet from disk — the whole
// point of the "decouple hydration from unmasking" fix. Previously this returned
// path_token only, forcing a follow-up Read on the common dev/eval setup.
func TestEnrichQueryResponse_NoneSchemeHydratesFromCheckout(t *testing.T) {
	const repoID = "cleartext-repo"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := Config{
		// no UnmaskPath — cleartext repo
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return dir, true },
	}

	env := map[string]any{
		"status": "ok",
		"repos":  map[string]any{repoID: map[string]any{"masking_scheme": "none", "remote_url": "github.com/acme/svc"}},
		"results": []map[string]any{
			{"repo_id": repoID, "path_token": "app.py", "line_start": 1, "line_end": 2},
		},
	}
	b, _ := json.Marshal(env)

	out := enrichQueryResponse(cfg, b)

	var enriched map[string]json.RawMessage
	if err := json.Unmarshal(out, &enriched); err != nil {
		t.Fatalf("unmarshal enriched: %v", err)
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(enriched["results"], &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	var realPath, snippet string
	_ = json.Unmarshal(results[0]["real_path"], &realPath)
	_ = json.Unmarshal(results[0]["snippet"], &snippet)
	if realPath != "app.py" {
		t.Errorf("real_path = %q, want identity %q", realPath, "app.py")
	}
	if snippet != "line1\nline2" {
		t.Errorf("snippet = %q, want %q (1-based [1,2] hydrated from checkout)", snippet, "line1\nline2")
	}
}

// Single-index (Mode A) responses populate the singular mask_key_rev and may omit
// the repo from mask_key_revs; the proxy must fall back to the singular field.
func TestEnrichQueryResponse_FallsBackToSingularRev(t *testing.T) {
	var gotRev int
	cfg := Config{
		RepoRoot: "/nonexistent",
		UnmaskPath: func(_ string, rev int) (string, bool) {
			gotRev = rev
			return "src/app/main.go", true
		},
	}
	env := map[string]any{
		"status":       "ok",
		"mask_key_rev": 2,
		"results": []map[string]any{
			{"repo_id": "r1", "path_token": "tok", "line_start": 1, "line_end": 2},
		},
	}
	b, _ := json.Marshal(env)

	_ = enrichQueryResponse(cfg, b)

	if gotRev != 2 {
		t.Fatalf("UnmaskPath called with rev %d, want 2 (singular fallback)", gotRev)
	}
}

// The wrong-repo hydration bug (ANALYSIS §"Confirmed bug"): a QUERY scoped to
// one repo returned a CLAUDE.md hit whose inlined snippet was the *calling*
// repo's CLAUDE.md. rootFor fell back to cfg.RepoRoot whenever RepoRootFor had
// no checkout for the result's repo_id, so filepath.Join pointed into the CWD
// tree — and because CLAUDE.md / README.md / Makefile exist in most repos, the
// read succeeded and shipped real but wrong content under a correct path.
//
// Two repos share the relative path; a checkout is registered for only one.
// The other must come back UNHYDRATED rather than hydrated from the first tree.
func TestEnrichQueryResponse_NoCheckoutDoesNotHydrateFromCWDRepo(t *testing.T) {
	const knownRepo = "repo-with-checkout"
	const otherRepo = "repo-without-checkout"

	cwdDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwdDir, "CLAUDE.md"), []byte("CWD REPO CONTENT\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := Config{
		RepoRoot:   cwdDir,
		CWDRepoID:  knownRepo,
		RepoScheme: func(string) (string, bool) { return "none", true },
		RepoRootFor: func(id string) (string, bool) {
			if id == knownRepo {
				return cwdDir, true
			}
			return "", false // no checkout known for otherRepo
		},
	}

	env := map[string]any{
		"status": "ok",
		"repos": map[string]any{
			knownRepo: map[string]any{"masking_scheme": "none", "remote_url": "github.com/acme/known"},
			otherRepo: map[string]any{"masking_scheme": "none", "remote_url": "github.com/acme/other"},
		},
		"results": []map[string]any{
			{"repo_id": knownRepo, "path_token": "CLAUDE.md", "line_start": 1, "line_end": 1},
			{"repo_id": otherRepo, "path_token": "CLAUDE.md", "line_start": 1, "line_end": 1},
		},
	}
	b, _ := json.Marshal(env)

	out := enrichQueryResponse(cfg, b)

	var enriched map[string]json.RawMessage
	if err := json.Unmarshal(out, &enriched); err != nil {
		t.Fatalf("unmarshal enriched: %v", err)
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(enriched["results"], &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}

	// The repo WITH a checkout still hydrates — the fix must not starve it.
	var knownSnippet string
	_ = json.Unmarshal(results[0]["snippet"], &knownSnippet)
	if knownSnippet != "CWD REPO CONTENT" {
		t.Errorf("known-checkout snippet = %q, want %q", knownSnippet, "CWD REPO CONTENT")
	}

	// The repo WITHOUT a checkout must carry no snippet at all.
	if raw, ok := results[1]["snippet"]; ok {
		var got string
		_ = json.Unmarshal(raw, &got)
		t.Errorf("snippet for repo with no checkout = %q, want none (would be the CWD repo's file)", got)
	}
	// real_path still stands — only hydration is skipped.
	var realPath string
	_ = json.Unmarshal(results[1]["real_path"], &realPath)
	if realPath != "CLAUDE.md" {
		t.Errorf("real_path = %q, want %q", realPath, "CLAUDE.md")
	}
}

// blob_sha on the envelope is what makes hydrateSnippet's staleness check live.
// When the local file differs from the indexed blob, the result must be flagged
// rather than silently shipped as current.
func TestEnrichQueryResponse_BlobShaMismatchMarksStale(t *testing.T) {
	const repoID = "cleartext-repo"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("local content\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := Config{
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return dir, true },
	}

	env := map[string]any{
		"status": "ok",
		"repos":  map[string]any{repoID: map[string]any{"masking_scheme": "none", "remote_url": "github.com/acme/svc"}},
		"results": []map[string]any{
			{
				"repo_id":    repoID,
				"path_token": "app.py",
				"line_start": 1,
				"line_end":   1,
				"blob_sha":   "0000000000000000000000000000000000000000",
			},
		},
	}
	b, _ := json.Marshal(env)

	out := enrichQueryResponse(cfg, b)

	var enriched map[string]json.RawMessage
	_ = json.Unmarshal(out, &enriched)
	var results []map[string]json.RawMessage
	_ = json.Unmarshal(enriched["results"], &results)

	var stale bool
	if raw, ok := results[0]["stale"]; ok {
		_ = json.Unmarshal(raw, &stale)
	}
	if !stale {
		t.Error("stale = false, want true (local file does not match the indexed blob_sha)")
	}
}

// A federated hit from a repo with no local checkout cannot be hydrated. The
// result must still carry real_path AND an explicit hydration reason: a bare
// missing snippet is indistinguishable from "the file moved" or "the read
// failed", and an agent that can't tell them apart either guesses a path (which
// can silently read the wrong repo's same-named file) or gives up.
func TestEnrichQueryResponse_NoCheckoutReportsReason(t *testing.T) {
	const repoID = "foreign-repo-uuid"
	cfg := Config{
		RepoRoot:    "/some/cwd/checkout",
		CWDRepoID:   "cwd-repo-uuid", // NOT repoID, so RepoRoot must not be used
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return "", false }, // nothing known
	}

	out := enrichQueryResponse(cfg, queryResponse(repoID, "internal/infra/kafka/kafka.go", 1))

	r := firstResult(t, out)
	if _, ok := r["snippet"]; ok {
		t.Error("snippet must not be hydrated when no checkout is known")
	}
	if got, _ := unmarshalString(r["real_path"]); got != "internal/infra/kafka/kafka.go" {
		t.Errorf("real_path = %q; want the cleartext token (unmasking needs no disk)", got)
	}
	if got, _ := unmarshalString(r["hydration"]); got != hydrationNoCheckout {
		t.Errorf("hydration = %q, want %q", got, hydrationNoCheckout)
	}
}

// Checkout known but the path absent in it (indexed at a ref this tree lacks, or
// moved since) is a different fix from "no checkout" — fetch vs clone — so it
// gets its own reason.
func TestEnrichQueryResponse_MissingFileReportsReason(t *testing.T) {
	const repoID = "known-repo-uuid"
	root := t.TempDir() // exists, but the file inside does not
	cfg := Config{
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return root, true },
	}

	out := enrichQueryResponse(cfg, queryResponse(repoID, "gone/file.go", 1))

	r := firstResult(t, out)
	if _, ok := r["snippet"]; ok {
		t.Error("snippet must not be set when the file is absent")
	}
	if got, _ := unmarshalString(r["hydration"]); got != hydrationFileMissing {
		t.Errorf("hydration = %q, want %q", got, hydrationFileMissing)
	}
}

// A successful hydration must NOT carry a hydration reason — the snippet's
// presence is the signal, and a redundant field would just cost tokens.
func TestEnrichQueryResponse_HydratedHasNoReason(t *testing.T) {
	const repoID = "known-repo-uuid"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return root, true },
	}

	out := enrichQueryResponse(cfg, queryResponse(repoID, "main.go", 1))

	r := firstResult(t, out)
	if snip, _ := unmarshalString(r["snippet"]); !strings.Contains(snip, "package main") {
		t.Errorf("snippet = %q; want the file's first lines", snip)
	}
	if _, ok := r["hydration"]; ok {
		t.Error("hydration reason must be absent when the snippet hydrated")
	}
}

// An hmac repo whose masking key isn't in this machine's keychain — the common
// case for a federated hit from a repo the dev has no key for — yields neither a
// real_path nor a snippet. That is the most ambiguous outcome of all, so it needs
// a reason too: without one the result is a bare masked token and an agent cannot
// tell "no key" from "nothing on disk".
func TestEnrichQueryResponse_UnmaskFailureReportsReason(t *testing.T) {
	const repoID = "hmac-repo-no-key"
	root := t.TempDir()
	cfg := Config{
		// hmac repo (scheme is not "none"), and the unmasker has no key for it.
		RepoScheme:  func(string) (string, bool) { return "hmac", true },
		RepoRootFor: func(string) (string, bool) { return root, true },
		UnmaskPath:  func(string, int) (string, bool) { return "", false },
	}

	out := enrichQueryResponse(cfg, queryResponse(repoID, "deadbeeftoken", 1))

	r := firstResult(t, out)
	if _, ok := r["real_path"]; ok {
		t.Error("real_path must be absent when unmasking failed")
	}
	if _, ok := r["snippet"]; ok {
		t.Error("snippet must be absent when the real path is unknown")
	}
	if got, _ := unmarshalString(r["hydration"]); got != hydrationUnmaskFailed {
		t.Errorf("hydration = %q, want %q", got, hydrationUnmaskFailed)
	}
}

// The file is where the envelope says, but the checkout is at a ref where it is
// shorter than the indexed range (or line_end is missing/0, which reads nothing).
// Emitting `"snippet": ""` would read as a successful hydration of an empty
// region, so the result must carry a reason and no snippet.
func TestEnrichQueryResponse_RangeNotInFileReportsReason(t *testing.T) {
	const repoID = "known-repo-uuid"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "short.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return root, true },
	}

	env := map[string]any{
		"status": "ok",
		"results": []map[string]any{
			// The file has 1 line; the chunk was indexed at lines 40–48.
			{"repo_id": repoID, "path_token": "short.go", "line_start": 40, "line_end": 48},
			// line_end absent entirely — the scan reads nothing at all.
			{"repo_id": repoID, "path_token": "short.go", "line_start": 1},
		},
	}
	b, _ := json.Marshal(env)

	out := enrichQueryResponse(cfg, b)

	var enriched map[string]json.RawMessage
	if err := json.Unmarshal(out, &enriched); err != nil {
		t.Fatalf("unmarshal enriched: %v", err)
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(enriched["results"], &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	for i, r := range results {
		if raw, ok := r["snippet"]; ok {
			t.Errorf("results[%d]: snippet = %s, want none (range not in file)", i, raw)
		}
		if got, _ := unmarshalString(r["hydration"]); got != hydrationRangeMissing {
			t.Errorf("results[%d]: hydration = %q, want %q", i, got, hydrationRangeMissing)
		}
	}
}

// A range that IS in the file but holds only blank lines is a real (if useless)
// hydration, not a failure: it must keep the empty-ish snippet and stay
// reason-free, so the range-missing check can't be a bare `snippet == ""` test.
func TestEnrichQueryResponse_BlankLinesHydrateWithoutReason(t *testing.T) {
	const repoID = "known-repo-uuid"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "blank.go"), []byte("\n\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		RepoScheme:  func(string) (string, bool) { return "none", true },
		RepoRootFor: func(string) (string, bool) { return root, true },
	}

	out := enrichQueryResponse(cfg, queryResponse(repoID, "blank.go", 1))

	r := firstResult(t, out)
	if got, ok := unmarshalString(r["snippet"]); !ok || got != "\n" {
		t.Errorf("snippet = %q (present=%v), want the two blank lines", got, ok)
	}
	if _, ok := r["hydration"]; ok {
		t.Error("hydration reason must be absent when the range was read")
	}
}

// firstResult decodes results[0] from an enriched QUERY envelope.
func firstResult(t *testing.T, out []byte) map[string]json.RawMessage {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(env["results"], &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	return results[0]
}

// The saving §4 of the implementation report identified and then declined: an
// MCP tool result carries the payload twice, and in agent format only one of the
// two copies needs to be the answer.
//
// The trade is deliberate and asymmetric — a structuredContent-only client loses
// the results — which is why it applies to `format: "agent"` alone, an explicit
// request for a text rendering, and never to the JSON default.
func TestEnrichResponse_QueryAgentFormatSummarisesStructuredContent(t *testing.T) {
	const repoID = "reviewfy-uuid"
	cfg := Config{
		Format:     formatAgent,
		NoSnippets: true,
		RepoRoot:   "/nonexistent",
		UnmaskPath: func(string, int) (string, bool) {
			return "app/jobs/weekly_leaderboard_job.py", true
		},
	}

	inner := queryResponse(repoID, "deadbeeftoken", 4)
	envelope, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(inner)}},
			"structuredContent": json.RawMessage(inner),
			"isError":           false,
		},
	})

	out := enrichResponse(cfg, envelope)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var result map[string]json.RawMessage
	_ = json.Unmarshal(env["result"], &result)

	var structured AgentSummary
	if err := json.Unmarshal(result["structuredContent"], &structured); err != nil {
		t.Fatalf("unmarshal structuredContent: %v", err)
	}
	if structured.Format != formatAgent {
		t.Errorf("format = %q, want %q", structured.Format, formatAgent)
	}
	if structured.Status != "ok" {
		t.Errorf("status = %q, want ok — a program still has to tell an empty answer from a broken one", structured.Status)
	}
	if structured.ResultCount == nil || *structured.ResultCount != 1 {
		t.Errorf("result_count = %v, want 1", structured.ResultCount)
	}
	// A search is not a traversal, and vice versa.
	if structured.EdgeCount != nil {
		t.Errorf("edge_count = %v on a QUERY summary, want absent", *structured.EdgeCount)
	}

	var content []map[string]json.RawMessage
	_ = json.Unmarshal(result["content"], &content)
	var text string
	_ = json.Unmarshal(content[0]["text"], &text)
	if !strings.Contains(text, "app/jobs/weekly_leaderboard_job.py") {
		t.Errorf("content block is not the rendering:\n%s", text)
	}

	// The point of the change: the result is one copy of the answer plus a
	// fixed-size summary, not two copies. Bounded absolutely rather than as a
	// fraction of the rendering — the summary does not scale with the payload,
	// which is the property, and on a one-hit fixture it is legitimately a large
	// fraction of a very small rendering.
	if got := len(result["structuredContent"]); got > 256 {
		t.Errorf("structuredContent is %d B — a summary, not the payload again", got)
	}
}

// The property the byte bound above is a proxy for: the summary is fixed-size.
// A hundredfold larger response must not make it meaningfully larger, or the
// duplication has crept back in proportional form.
func TestAgentSummary_DoesNotScaleWithThePayload(t *testing.T) {
	build := func(n int) []byte {
		results := make([]map[string]any, n)
		for i := range results {
			results[i] = map[string]any{
				"repo_id": "r", "path_token": strings.Repeat("deep/nested/path/", 8),
				"line_start": i, "line_end": i + 20, "score": 0.5,
			}
		}
		b, _ := json.Marshal(map[string]any{"status": "ok", "freshness": "fresh", "results": results})
		return b
	}

	small, _ := json.Marshal(agentSummary(build(1)))
	large, _ := json.Marshal(agentSummary(build(50)))
	if delta := len(large) - len(small); delta > 8 {
		t.Errorf("summary grew %d B between a 1-hit and a 50-hit payload; it must not scale", delta)
	}
}
