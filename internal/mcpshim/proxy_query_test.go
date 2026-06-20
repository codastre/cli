package mcpshim

import (
	"encoding/json"
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
