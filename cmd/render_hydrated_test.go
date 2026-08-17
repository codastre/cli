package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// Human output for a hydrated hit: the body under the location, numbered with
// the file's real line numbers. The numbers are the point — a body printed
// without them makes the reader derive line numbers from the span, which is
// exactly the drift the gutter exists to prevent.
func TestRenderQueryHuman_Snippet(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status":         "ok",
		"freshness":      "fresh",
		"searched_repos": []string{"r1"},
		"results": []any{
			map[string]any{
				"repo_id": "r1", "path_token": "tok", "real_path": "a/b.go",
				"line_start": 40, "line_end": 42, "score": 0.5, "content_kind": "code",
				"snippet": "func Foo() {\n\treturn 1\n}",
			},
		},
	})

	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"40│ func Foo() {", "41│ \treturn 1", "42│ }"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
	// real_path beats the raw token: hydration read a file at that path, so it
	// is the more authoritative of the two.
	if !strings.Contains(out, "a/b.go:40-42") || strings.Contains(out, "tok:") {
		t.Errorf("expected real_path in the location line, got:\n%s", out)
	}
}

// A truncated body says where the rest is. Without it the reader cannot tell a
// budget cut from a chunk that genuinely ends there.
func TestRenderQueryHuman_SnippetTruncated(t *testing.T) {
	cases := []struct {
		name    string
		lineEnd int
		want    string
	}{
		{"range", 60, "lines 43-60 have the rest"},
		// One line left over is common when the budget lands just short of the
		// span, and "lines 43-43" reads as a bug.
		{"single line", 43, "line 43 has the rest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := mustJSON(t, map[string]any{
				"status": "ok", "freshness": "fresh", "searched_repos": []string{"r1"},
				"results": []any{
					map[string]any{
						"repo_id": "r1", "path_token": "a/b.go",
						"line_start": 40, "line_end": c.lineEnd, "score": 0.5, "content_kind": "code",
						"snippet": "one\ntwo\nthree", "snippet_truncated": true, "snippet_line_end": 42,
					},
				},
			})
			var buf bytes.Buffer
			if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), c.want) {
				t.Errorf("expected %q in:\n%s", c.want, buf.String())
			}
		})
	}
}

// A stale body is a lead, not a quote: the local file has moved on from the blob
// that was indexed, so the span may no longer hold what the ranker scored.
func TestRenderQueryHuman_Stale(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status": "ok", "freshness": "fresh", "searched_repos": []string{"r1"},
		"results": []any{
			map[string]any{
				"repo_id": "r1", "path_token": "a/b.go", "line_start": 1, "line_end": 1,
				"score": 0.5, "content_kind": "code", "snippet": "x := 1", "stale": true,
			},
		},
	})
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[stale") {
		t.Errorf("expected a stale marker, got:\n%s", buf.String())
	}
}

// Why there is no body, per hit. A silent absence is indistinguishable from "no
// checkout", "file moved" and "read failed" — three different fixes.
func TestRenderQueryHuman_HydrationReason(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status": "ok", "freshness": "fresh", "searched_repos": []string{"r1"},
		"results": []any{
			map[string]any{
				"repo_id": "r1", "path_token": "a/b.go", "line_start": 1, "line_end": 9,
				"score": 0.5, "content_kind": "code", "hydration": "no_local_checkout",
			},
		},
	})
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no body: no_local_checkout") {
		t.Errorf("expected the hydration reason, got:\n%s", buf.String())
	}
}

// Locations-only (the default): the header states the mode once and points at
// the flag that changes it, and the per-hit reason is suppressed rather than
// restating the mode under every result.
func TestRenderQueryHuman_SnippetsOff(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"status": "ok", "freshness": "fresh", "searched_repos": []string{"r1"},
		"results": []any{
			map[string]any{
				"repo_id": "r1", "path_token": "a/b.go", "line_start": 1, "line_end": 9,
				"score": 0.5, "content_kind": "code", "hydration": "snippets_disabled",
			},
		},
	})
	var buf bytes.Buffer
	if err := renderQueryHuman(&buf, payload, nil, true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "locations only (--snippets for bodies)") {
		t.Errorf("expected the mode and the flag that changes it, got:\n%s", out)
	}
	if strings.Contains(out, "no body:") {
		t.Errorf("per-hit reason should not restate the header's mode, got:\n%s", out)
	}
}
