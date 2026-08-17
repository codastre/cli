package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveTarget_Precedence(t *testing.T) {
	t.Run("index-id targets a single index", func(t *testing.T) {
		tgt, err := resolveTarget("idx-1", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if tgt.indexID != "idx-1" || tgt.repoURL != "" || tgt.federated {
			t.Fatalf("got %+v", tgt)
		}
		args := map[string]any{}
		tgt.apply(args)
		if args["index_id"] != "idx-1" {
			t.Fatalf("apply wrote %v", args)
		}
	})

	t.Run("repo-url is normalized", func(t *testing.T) {
		tgt, err := resolveTarget("", "git@github.com:acme/api.git", false)
		if err != nil {
			t.Fatal(err)
		}
		if tgt.repoURL != "github.com/acme/api" {
			t.Fatalf("repoURL = %q, want github.com/acme/api", tgt.repoURL)
		}
	})

	t.Run("all searches every visible repo", func(t *testing.T) {
		tgt, err := resolveTarget("", "", true)
		if err != nil {
			t.Fatal(err)
		}
		if !tgt.federated {
			t.Fatalf("got %+v, want federated", tgt)
		}
		args := map[string]any{}
		tgt.apply(args)
		if _, ok := args["index_id"]; ok {
			t.Fatal("federated must not set index_id")
		}
		if _, ok := args["repo_url"]; ok {
			t.Fatal("federated must not set repo_url")
		}
	})

	t.Run("conflicting flags error", func(t *testing.T) {
		if _, err := resolveTarget("idx-1", "github.com/acme/api", false); err == nil {
			t.Fatal("expected conflict error for index-id + repo-url")
		}
		if _, err := resolveTarget("", "github.com/acme/api", true); err == nil {
			t.Fatal("expected conflict error for repo-url + all")
		}
	})
}

func TestRenderQueryHuman_MultiRepo(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status":         "ok",
		"freshness":      "fresh",
		"searched_repos": []string{"r1", "r2"},
		"sync_job_id":    nil,
		"repos": map[string]any{
			"r1": map[string]any{"remote_url": "github.com/acme/api"},
		},
		"results": []any{
			map[string]any{"repo_id": "r1", "path_token": "a/b.go", "line_start": 1, "line_end": 9, "score": 0.5, "kind": "code", "symbol_name": "Foo", "content_kind": "code"},
			map[string]any{"repo_id": "r2", "path_token": "c/d.py", "line_start": 3, "line_end": 7, "score": 0.4, "kind": "code", "symbol_name": nil, "content_kind": "code"},
		},
	})

	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "github.com/acme/api") {
		t.Error("expected remote_url label from repos map")
	}
	if !strings.Contains(out, "r2") {
		t.Error("expected raw repo_id fallback when repos map lacks the entry")
	}
	if !strings.Contains(out, "a/b.go:1-9") || !strings.Contains(out, "Foo") {
		t.Error("expected path:lines and symbol")
	}
}

func TestRenderQueryHuman_DocumentTitle(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status":         "ok",
		"freshness":      "fresh",
		"searched_repos": []string{"c1"},
		"sync_job_id":    nil,
		"results": []any{
			map[string]any{
				"repo_id": "c1", "path_token": "38e51cdf-doc-id", "line_start": 0, "line_end": 0,
				"score": 1.0, "kind": "runbook", "symbol_name": nil, "content_kind": "runbook",
				"title": "kafka-consumer-lag.md", "source_ref": "https://wiki/kafka-lag",
			},
		},
	})

	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "kafka-consumer-lag.md") || !strings.Contains(out, "(runbook)") {
		t.Errorf("expected title and content_kind on the line, got:\n%s", out)
	}
	if !strings.Contains(out, "[https://wiki/kafka-lag]") {
		t.Errorf("expected source_ref shown when it differs from title, got:\n%s", out)
	}
	if !strings.Contains(out, "doc 38e51cdf-doc-id") {
		t.Errorf("expected doc-id retained for content fetch, got:\n%s", out)
	}
	if strings.Contains(out, "38e51cdf-doc-id:0-0") {
		t.Errorf("doc hit must not render the code-style path:lines form, got:\n%s", out)
	}
}

func TestRenderQueryHuman_Empty(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status":         "ok",
		"freshness":      "fresh",
		"searched_repos": []string{"r1"},
		"results":        []any{},
	})
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No matches.") {
		t.Errorf("expected 'No matches.', got %q", buf.String())
	}
}

func TestRenderGraphHuman_Count(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"edges": []any{
			map[string]any{
				"edge":       map[string]any{"kind": "calls"},
				"src":        map[string]any{"path_token": "a.py", "line_start": 0, "line_end": 100},
				"dst":        map[string]any{"path_token": "b.py", "line_start": 0, "line_end": 50},
				"confidence": 0.9, "resolution": "heuristic", "count": 19,
			},
			map[string]any{
				"edge":       map[string]any{"kind": "kafka"},
				"src":        map[string]any{"path_token": "c.py", "line_start": 1, "line_end": 2},
				"dst":        map[string]any{"path_token": "d.py", "line_start": 3, "line_end": 4},
				"confidence": 0.3, "resolution": "dynamic_unresolved", "count": 1,
			},
		},
	})
	var buf bytes.Buffer
	if err := renderGraphHuman(&buf, payload, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "(×19)") {
		t.Errorf("expected (×19) for collapsed edge, got %q", out)
	}
	if strings.Contains(out, "(×1)") {
		t.Error("count of 1 should not render a multiplier")
	}
	if !strings.Contains(out, "[hypothesis]") {
		t.Error("expected [hypothesis] flag for low-confidence dynamic edge")
	}
}

func TestRenderGraphHuman_Unmask(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"edges": []any{
			map[string]any{
				"edge":       map[string]any{"kind": "calls"},
				"src":        map[string]any{"path_token": "TOKsrc", "line_start": 1, "line_end": 9},
				"dst":        map[string]any{"path_token": "TOKdst", "line_start": 3, "line_end": 7},
				"confidence": 0.9, "resolution": "heuristic", "count": 1,
			},
		},
	})
	// GRAPH carries no mask_key_rev, so the renderer must pass rev 0.
	unmask := func(token string, rev int) (string, bool) {
		if rev != 0 {
			t.Errorf("expected rev 0 for GRAPH tokens, got %d", rev)
		}
		switch token {
		case "TOKsrc":
			return "real/src.go", true
		case "TOKdst":
			return "real/dst.go", true
		}
		return "", false
	}
	var buf bytes.Buffer
	if err := renderGraphHuman(&buf, payload, unmask); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "real/src.go:1-9") || !strings.Contains(out, "real/dst.go:3-7") {
		t.Errorf("expected unmasked src/dst paths, got %q", out)
	}
	if strings.Contains(out, "TOK") {
		t.Errorf("raw tokens leaked into unmasked output: %q", out)
	}
}

func TestRenderGraphHuman_UnmaskMissFallsBackToToken(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"edges": []any{
			map[string]any{
				"edge":       map[string]any{"kind": "calls"},
				"src":        map[string]any{"path_token": "TOKsrc", "line_start": 1, "line_end": 9},
				"dst":        map[string]any{"path_token": "TOKdst", "line_start": 3, "line_end": 7},
				"confidence": 0.9, "resolution": "heuristic", "count": 1,
			},
		},
	})
	// An unmask that resolves nothing (e.g. file not checked out locally) must
	// leave the raw token visible rather than dropping the path.
	unmask := func(token string, rev int) (string, bool) { return "", false }
	var buf bytes.Buffer
	if err := renderGraphHuman(&buf, payload, unmask); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "TOKsrc:1-9") || !strings.Contains(out, "TOKdst:3-7") {
		t.Errorf("expected raw tokens on unmask miss, got %q", out)
	}
}

func TestRenderQueryHuman_Unmask(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"freshness":      "fresh",
		"searched_repos": []string{"r1"},
		"mask_key_rev":   3,
		"results": []any{
			map[string]any{"repo_id": "r1", "path_token": "TOK", "line_start": 1, "line_end": 9, "score": 0.5, "content_kind": "code"},
		},
	})
	unmask := func(token string, rev int) (string, bool) {
		if token == "TOK" && rev == 3 {
			return "real/path.go", true
		}
		return "", false
	}
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, unmask, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "real/path.go:1-9") {
		t.Errorf("expected unmasked path with the envelope's mask_key_rev, got %q", out)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The tri-state resolver: --format grew a third value, and a bool cannot carry
// three. The legacy --json flag stays a synonym for --format json.
func TestResolveFormat(t *testing.T) {
	cases := []struct {
		flag   bool
		format string
		want   string
		errs   bool
	}{
		{false, "human", formatHuman, false},
		{false, "", formatHuman, false},
		{true, "", formatJSON, false},
		{true, "human", formatJSON, false},
		{false, "json", formatJSON, false},
		{false, "agent", formatAgent, false},
		// An explicit format wins over the legacy bool: the specific flag beats
		// the general one, whichever order they were typed in.
		{true, "agent", formatAgent, false},
		{false, "xml", "", true},
	}
	for _, c := range cases {
		got, err := resolveFormat(c.flag, c.format)
		if c.errs {
			if err == nil {
				t.Errorf("resolveFormat(%v,%q): expected error", c.flag, c.format)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("resolveFormat(%v,%q) = %q,%v; want %q", c.flag, c.format, got, err, c.want)
		}
	}
}

// The CLI talks to the server directly, so it must forward the server-side rung
// of the compaction ladder itself — and only for the one format whose renderer
// reads nothing compact drops.
func TestWireFormatFor(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   string
		send   bool
	}{
		{formatAgent, "compact", true},
		{formatJSON, "", false},  // documented raw passthrough of the envelope
		{formatHuman, "", false}, // prints content_kind unconditionally
	} {
		got, ok := wireFormatFor(tc.format)
		if ok != tc.send || got != tc.want {
			t.Errorf("wireFormatFor(%q) = (%q, %v), want (%q, %v)", tc.format, got, ok, tc.want, tc.send)
		}
	}
}
