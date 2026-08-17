package mcpshim

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// HydrateQueryPayload is the entry point `codastre query` uses: it talks to the
// server directly, so nothing has enriched the envelope for it. It must produce
// the same fields the proxy writes, from the same code — two implementations
// would be two chances to disagree about which line a body starts at.
func TestHydrateQueryPayload(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{
		"status": "ok", "freshness": "fresh",
		"results": []map[string]any{{
			"repo_id": "r1", "path_token": "src/main.go", "line_start": 1, "line_end": 3,
		}},
	})

	cfg := Config{
		RepoRoot:   root,
		CWDRepoID:  "r1",
		RepoScheme: func(string) (string, bool) { return "none", true },
	}
	var log bytes.Buffer
	cfg.Log = &log

	out := HydrateQueryPayload(cfg, payload)

	var env struct {
		Results []struct {
			RealPath string `json:"real_path"`
			Snippet  string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal hydrated envelope: %v", err)
	}
	if len(env.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(env.Results))
	}
	if env.Results[0].RealPath != "src/main.go" {
		t.Errorf("real_path = %q", env.Results[0].RealPath)
	}
	if env.Results[0].Snippet != "package main\n\nfunc main() {}" {
		t.Errorf("snippet = %q", env.Results[0].Snippet)
	}
	// The cost line bills the caller for bytes it was sent. This caller renders
	// the payload itself, so a line here would report a number nobody paid.
	if log.Len() != 0 {
		t.Errorf("expected no cost line, got %q", log.String())
	}
}

// A cfg with no way to resolve any path leaves the envelope byte-identical:
// nothing to hydrate, and inventing a `hydration` reason on every result would
// bill the caller for a diagnosis it cannot act on.
func TestHydrateQueryPayload_NoResolution(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"status":  "ok",
		"results": []map[string]any{{"repo_id": "r1", "path_token": "src/main.go"}},
	})
	if out := HydrateQueryPayload(Config{}, payload); !bytes.Equal(out, payload) {
		t.Errorf("payload changed with no resolver:\n got %s\nwant %s", out, payload)
	}
}

// The gutter is shared with `codastre query`'s human renderer so a line number
// is derived in exactly one place.
func TestWriteSnippetLines(t *testing.T) {
	var b bytes.Buffer
	last := WriteSnippetLines(&b, "  ", "a\nb\nc", 9)
	// Width is set by the LAST number, so a run crossing a digit boundary stays
	// right-aligned instead of stepping left at 10.
	if got := b.String(); got != "   9│ a\n  10│ b\n  11│ c\n" {
		t.Errorf("got %q", got)
	}
	if last != 11 {
		t.Errorf("last = %d, want 11", last)
	}
	// hydrateSnippet clamps line_start below 1 to 1; the gutter must agree, or
	// the body and its numbers would disagree by one.
	b.Reset()
	if WriteSnippetLines(&b, "", "x", 0); b.String() != "1│ x\n" {
		t.Errorf("lineStart 0 not clamped to 1: %q", b.String())
	}
}
