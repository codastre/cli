package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// The compacted GRAPH wire shape (corpus-hygiene API3) carries confidence and
// resolution ONLY on the canonical edge object — no item-level mirrors. The
// human renderer must read them from there; reading only the removed mirrors
// rendered every edge as "conf 0.00  [hypothesis]" (the bug this pins).
func TestRenderGraphHuman_CompactedEnvelopeReadsEdgeObject(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"edges": []any{
			map[string]any{
				"edge":  map[string]any{"kind": "kafka", "confidence": 0.61, "resolution": "resolved"},
				"src":   map[string]any{"path_token": "app/events.py", "line_start": 0, "line_end": 12},
				"dst":   map[string]any{"path_token": "app/consumer.py", "line_start": 0, "line_end": 12},
				"count": 1,
			},
		},
	})
	var buf bytes.Buffer
	if err := renderGraphHuman(&buf, payload, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "conf 0.61") {
		t.Errorf("expected conf 0.61 from edge object, got %q", out)
	}
	if !strings.Contains(out, "resolved") {
		t.Errorf("expected resolution from edge object, got %q", out)
	}
	if strings.Contains(out, "[hypothesis]") {
		t.Errorf("0.61 resolved edge must not be flagged as hypothesis: %q", out)
	}
}

// Legacy (pre-API3) responses carry item-level mirrors and no scores on the
// edge object — the fallback must keep rendering them correctly.
func TestRenderGraphHuman_LegacyMirrorsStillRender(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"edges": []any{
			map[string]any{
				"edge":       map[string]any{"kind": "kafka"},
				"src":        map[string]any{"path_token": "a.py", "line_start": 1, "line_end": 2},
				"dst":        map[string]any{"path_token": "b.py", "line_start": 3, "line_end": 4},
				"confidence": 0.74, "resolution": "resolved", "count": 1,
			},
		},
	})
	var buf bytes.Buffer
	if err := renderGraphHuman(&buf, payload, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "conf 0.74") || strings.Contains(out, "[hypothesis]") {
		t.Errorf("legacy mirror fallback broken: %q", out)
	}
}

// Scoped federated QUERY envelopes (API2) omit searched_repos and report
// searched_repo_count — the header must not claim "searched 0 repo(s)".
func TestRenderQueryHuman_ScopedEnvelopeSearchedCount(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"freshness":           "fresh",
		"searched_repo_count": 22,
		"repos":               map[string]any{},
		"mask_key_rev":        nil,
		"results":             []any{},
	})
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "searched 22 repo(s)") {
		t.Errorf("expected searched 22 repo(s), got %q", buf.String())
	}
}
