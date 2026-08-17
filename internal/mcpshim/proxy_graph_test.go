package mcpshim

import (
	"encoding/json"
	"strings"
	"testing"
)

// graphResponse builds a minimal GRAPH response JSON with one edge, in the
// compacted wire shape (corpus-hygiene plan API3): the edge object is canonical
// for confidence/resolution (no item-level mirrors), and per-repo masking
// metadata rides in the repos map.
// If withEvidence is true, the edge includes an evidence object with file_path_token.
func graphResponse(srcToken, dstToken string, withEvidence bool) []byte {
	edge := map[string]any{
		"edge": map[string]any{
			"edge_id":    "edge-uuid-1",
			"kind":       "kafka",
			"confidence": 1.0,
			"resolution": "resolved",
		},
		"src": map[string]any{
			"repo_id":    "repo-src",
			"path_token": srcToken,
			"line_start": 1,
			"line_end":   10,
		},
		"dst": map[string]any{
			"repo_id":    "repo-dst",
			"path_token": dstToken,
			"line_start": 5,
			"line_end":   15,
		},
		"count": 1,
	}
	if withEvidence {
		edge["evidence"] = map[string]any{
			"file_path_token": "abc123",
		}
	}
	resp := map[string]any{
		"edges": []any{edge},
		"repos": map[string]any{
			"repo-src": map[string]any{"masking_scheme": "hmac", "mask_key_rev": 2},
			"repo-dst": map[string]any{"masking_scheme": "hmac", "mask_key_rev": 0},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// extractString navigates a decoded JSON map by dot-separated key path.
func extractString(t *testing.T, data []byte, keys ...string) (string, bool) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal top level: %v", err)
	}
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(m["edges"], &edges); err != nil {
		t.Fatalf("unmarshal edges: %v", err)
	}
	if len(edges) == 0 {
		t.Fatal("expected at least one edge")
	}
	obj := edges[0]
	for i, k := range keys {
		raw, ok := obj[k]
		if !ok {
			return "", false
		}
		if i == len(keys)-1 {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				return s, true
			}
			return "", false
		}
		// Drill into nested object.
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			t.Fatalf("unmarshal nested key %q: %v", k, err)
		}
		obj = next
	}
	return "", false
}

// TestEnrichGraphResponse_MaskedRepo: UnmaskPath always succeeds; src and dst
// should gain a real_path field.
func TestEnrichGraphResponse_MaskedRepo(t *testing.T) {
	cfg := Config{
		UnmaskPath: func(_ string, _ int) (string, bool) {
			return "src/main/Foo.java", true
		},
	}
	input := graphResponse("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100", false)
	got := enrichGraphResponse(cfg, input)

	srcReal, ok := extractString(t, got, "src", "real_path")
	if !ok {
		t.Fatal("src.real_path missing")
	}
	if srcReal != "src/main/Foo.java" {
		t.Errorf("src.real_path = %q, want %q", srcReal, "src/main/Foo.java")
	}

	dstReal, ok := extractString(t, got, "dst", "real_path")
	if !ok {
		t.Fatal("dst.real_path missing")
	}
	if dstReal != "src/main/Foo.java" {
		t.Errorf("dst.real_path = %q, want %q", dstReal, "src/main/Foo.java")
	}
}

// TestEnrichGraphResponse_UnmaskedRepo: UnmaskPath always returns ok=false
// (masking_scheme=none or key unavailable); real_path must NOT be added.
func TestEnrichGraphResponse_UnmaskedRepo(t *testing.T) {
	cfg := Config{
		UnmaskPath: func(_ string, _ int) (string, bool) {
			return "", false
		},
	}
	input := graphResponse("plain/path/Foo.java", "plain/path/Bar.java", false)
	got := enrichGraphResponse(cfg, input)

	if _, ok := extractString(t, got, "src", "real_path"); ok {
		t.Error("src.real_path should not be present when UnmaskPath returns false")
	}
	if _, ok := extractString(t, got, "dst", "real_path"); ok {
		t.Error("dst.real_path should not be present when UnmaskPath returns false")
	}
}

// TestEnrichGraphResponse_PerRepoRev: each endpoint unmasks at its OWN repo's
// mask_key_rev from the repos map (plan API3 — was hardcoded rev 0, which
// wrong-unmasked rotated hmac repos). src is at rev 2, dst at rev 0; evidence
// rides the src repo's rev.
func TestEnrichGraphResponse_PerRepoRev(t *testing.T) {
	revsSeen := map[string]int{}
	cfg := Config{
		UnmaskPath: func(tok string, rev int) (string, bool) {
			revsSeen[tok] = rev
			return "real/" + tok, true
		},
	}
	input := graphResponse("srctok", "dsttok", true)
	got := enrichGraphResponse(cfg, input)

	if rev, ok := revsSeen["srctok"]; !ok || rev != 2 {
		t.Errorf("src unmasked at rev %d (seen=%v), want 2", rev, revsSeen)
	}
	if rev, ok := revsSeen["dsttok"]; !ok || rev != 0 {
		t.Errorf("dst unmasked at rev %d, want 0", rev)
	}
	// Evidence belongs to the src side → src repo's rev.
	if rev, ok := revsSeen["abc123"]; !ok || rev != 2 {
		t.Errorf("evidence unmasked at rev %d, want 2 (src repo's rev)", rev)
	}
	if real, ok := extractString(t, got, "src", "real_path"); !ok || real != "real/srctok" {
		t.Errorf("src.real_path = %q", real)
	}
}

// TestEnrichGraphResponse_NoneSchemeIdentity: a `none`-scheme (cleartext) repo
// has NO UnmaskPath, but RepoScheme reports "none" — the proxy must identity-map
// path_token → real_path (the token already IS the real path) instead of leaving
// the edge unenriched. Regression guard for the "snippets/paths starved on
// cleartext repos" fix.
func TestEnrichGraphResponse_NoneSchemeIdentity(t *testing.T) {
	cfg := Config{
		// no UnmaskPath — cleartext repos have no key/unmasker
		RepoScheme: func(repoID string) (string, bool) { return "none", true },
	}
	input := graphResponse("app/events.py", "app/consumer.py", true)
	got := enrichGraphResponse(cfg, input)

	if real, ok := extractString(t, got, "src", "real_path"); !ok || real != "app/events.py" {
		t.Errorf("src.real_path = %q, ok=%v; want identity %q", real, ok, "app/events.py")
	}
	if real, ok := extractString(t, got, "dst", "real_path"); !ok || real != "app/consumer.py" {
		t.Errorf("dst.real_path = %q, ok=%v; want identity %q", real, ok, "app/consumer.py")
	}
	if real, ok := extractString(t, got, "evidence", "real_file_path"); !ok || real != "abc123" {
		t.Errorf("evidence.real_file_path = %q, ok=%v; want identity %q", real, ok, "abc123")
	}
}

// TestEnrichGraphResponse_EvidenceUnmask: edge has evidence.file_path_token;
// UnmaskPath succeeds; evidence.real_file_path should be set.
func TestEnrichGraphResponse_EvidenceUnmask(t *testing.T) {
	cfg := Config{
		UnmaskPath: func(_ string, _ int) (string, bool) {
			return "src/Producer.java", true
		},
	}
	input := graphResponse("tok1", "tok2", true)
	got := enrichGraphResponse(cfg, input)

	realFile, ok := extractString(t, got, "evidence", "real_file_path")
	if !ok {
		t.Fatal("evidence.real_file_path missing")
	}
	if realFile != "src/Producer.java" {
		t.Errorf("evidence.real_file_path = %q, want %q", realFile, "src/Producer.java")
	}
}

// GRAPH's server-side repos map carries only {masking_scheme, mask_key_rev}, so a
// federated edge identifies its endpoints by bare UUID and an agent cannot say
// WHICH service is on the other end of a kafka/http edge. The proxy already holds
// repo_id → remote_url for hydration, so it backfills the field locally, matching
// QUERY's repos map.
func TestEnrichGraphResponse_BackfillsRemoteURL(t *testing.T) {
	cfg := Config{
		RepoScheme: func(string) (string, bool) { return "none", true },
		RepoRemoteURL: func(repoID string) (string, bool) {
			if repoID == "src-repo" {
				return "github.com/my-org/orders-service", true
			}
			return "", false
		},
	}
	input := []byte(`{
	  "edges": [{"edge":{"kind":"kafka","confidence":0.95},
	             "src":{"repo_id":"src-repo","path_token":"a.go"},
	             "dst":{"repo_id":"unknown-repo","path_token":"b.go"}}],
	  "repos": {"src-repo":{"masking_scheme":"none","mask_key_rev":1},
	            "unknown-repo":{"masking_scheme":"none","mask_key_rev":1}}
	}`)

	out := enrichGraphResponse(cfg, input)

	var env struct {
		Repos map[string]map[string]any `json:"repos"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := env.Repos["src-repo"]["remote_url"]; got != "github.com/my-org/orders-service" {
		t.Errorf("src-repo remote_url = %v, want the backfilled URL", got)
	}
	// A repo the proxy doesn't know is left alone rather than invented.
	if _, ok := env.Repos["unknown-repo"]["remote_url"]; ok {
		t.Error("unknown-repo must not gain a remote_url")
	}
	// Pre-existing masking fields must survive the rewrite.
	if got := env.Repos["src-repo"]["masking_scheme"]; got != "none" {
		t.Errorf("masking_scheme lost: %v", got)
	}
}

// If a future server version starts sending remote_url, its value must win over
// the proxy's local snapshot, which can be stale.
func TestEnrichGraphResponse_DoesNotOverwriteServerRemoteURL(t *testing.T) {
	cfg := Config{
		RepoScheme:    func(string) (string, bool) { return "none", true },
		RepoRemoteURL: func(string) (string, bool) { return "github.com/my-org/stale", true },
	}
	input := []byte(`{
	  "edges": [{"edge":{"kind":"kafka"},"src":{"repo_id":"r1","path_token":"a.go"},"dst":{"repo_id":"r1","path_token":"b.go"}}],
	  "repos": {"r1":{"masking_scheme":"none","mask_key_rev":1,"remote_url":"github.com/my-org/authoritative"}}
	}`)

	out := enrichGraphResponse(cfg, input)

	var env struct {
		Repos map[string]map[string]any `json:"repos"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := env.Repos["r1"]["remote_url"]; got != "github.com/my-org/authoritative" {
		t.Errorf("remote_url = %v; server value must win", got)
	}
}

// Agent format on GRAPH: the tool result's two representations both carry the
// rendering instead of two copies of the JSON — the largest single saving in
// this format, and the one that is invisible unless it is pinned.
func TestEnrichResponse_GraphAgentFormat(t *testing.T) {
	cfg := Config{
		Format:        formatAgent,
		RepoScheme:    func(string) (string, bool) { return "none", true },
		RepoRemoteURL: func(string) (string, bool) { return "github.com/acme/api", true },
	}
	inner := graphResponse("internal/pay/charge.go", "internal/pay/card.go", false)
	envelope, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(inner)}},
			"structuredContent": json.RawMessage(inner),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out := enrichResponse(cfg, envelope)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(env["result"], &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	var structured struct {
		Format    string `json:"format"`
		Rendering string `json:"rendering"`
	}
	if err := json.Unmarshal(result["structuredContent"], &structured); err != nil {
		t.Fatalf("unmarshal structuredContent: %v", err)
	}
	if structured.Format != formatAgent {
		t.Errorf("structuredContent.format = %q, want %q", structured.Format, formatAgent)
	}
	if !strings.Contains(structured.Rendering, "codastre · graph") {
		t.Errorf("structuredContent carries JSON, not the rendering: %s", structured.Rendering)
	}

	var content []map[string]json.RawMessage
	if err := json.Unmarshal(result["content"], &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	var text string
	if err := json.Unmarshal(content[0]["text"], &text); err != nil {
		t.Fatalf("unmarshal text block: %v", err)
	}
	if text != structured.Rendering {
		t.Errorf("the two representations disagree:\n%q\n%q", text, structured.Rendering)
	}
	if !strings.Contains(text, "internal/pay/card.go") {
		t.Errorf("rendering lost the destination path:\n%s", text)
	}
}

// Without the agent format the JSON path is untouched: the enriched envelope
// still ships as JSON in both representations.
func TestEnrichResponse_GraphJSONFormatUnchanged(t *testing.T) {
	cfg := Config{RepoScheme: func(string) (string, bool) { return "none", true }}
	inner := graphResponse("a.go", "b.go", false)
	envelope, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(inner)}},
			"structuredContent": json.RawMessage(inner),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out := enrichResponse(cfg, envelope)

	if !strings.Contains(string(out), `\"edges\"`) && !strings.Contains(string(out), `"edges"`) {
		t.Errorf("JSON-format GRAPH result was rewritten as text: %s", out)
	}
	if strings.Contains(string(out), "codastre · graph") {
		t.Errorf("JSON-format GRAPH result carries a rendering: %s", out)
	}
}
