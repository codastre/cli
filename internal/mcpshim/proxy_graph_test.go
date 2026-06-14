package mcpshim

import (
	"encoding/json"
	"testing"
)

// graphResponse builds a minimal GRAPH response JSON with one edge.
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
			"path_token": srcToken,
			"line_start": 1,
			"line_end":   10,
		},
		"dst": map[string]any{
			"path_token": dstToken,
			"line_start": 5,
			"line_end":   15,
		},
		"confidence": 1.0,
		"resolution": "resolved",
	}
	if withEvidence {
		edge["evidence"] = map[string]any{
			"file_path_token": "abc123",
		}
	}
	resp := map[string]any{
		"edges": []any{edge},
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
