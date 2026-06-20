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

func TestWantJSON(t *testing.T) {
	cases := []struct {
		flag   bool
		format string
		want   bool
		errs   bool
	}{
		{false, "human", false, false},
		{true, "human", true, false},
		{false, "json", true, false},
		{false, "", false, false},
		{false, "xml", false, true},
	}
	for _, c := range cases {
		got, err := wantJSON(c.flag, c.format)
		if c.errs {
			if err == nil {
				t.Errorf("wantJSON(%v,%q): expected error", c.flag, c.format)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("wantJSON(%v,%q) = %v,%v; want %v", c.flag, c.format, got, err, c.want)
		}
	}
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
	if err := renderQueryHuman(&buf, payload, nil); err != nil {
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

func TestRenderQueryHuman_Empty(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status":         "ok",
		"freshness":      "fresh",
		"searched_repos": []string{"r1"},
		"results":        []any{},
	})
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil); err != nil {
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
	if err := renderGraphHuman(&buf, payload); err != nil {
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

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
