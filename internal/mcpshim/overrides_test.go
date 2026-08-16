package mcpshim

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolCall(t *testing.T, args map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "QUERY", "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	return b
}

func callArguments(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var msg struct {
		Params struct {
			Arguments map[string]json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("unmarshal forwarded body: %v", err)
	}
	return msg.Params.Arguments
}

// The client-only arguments must never reach the server: QUERY's tool schema
// doesn't declare them, so forwarding one turns a cost hint into a validation
// error. Everything else in the call has to survive untouched.
func TestTakeHydrationOverrides_StripsClientOnlyArgs(t *testing.T) {
	body := toolCall(t, map[string]any{
		"query_text":        "recall outbound transfer",
		"top_k":             4,
		"language":          "ruby",
		"max_snippet_lines": 20,
		"snippets":          false,
	})

	out, ov := takeHydrationOverrides(body)

	args := callArguments(t, out)
	for _, k := range []string{argMaxSnippetLines, argSnippets} {
		if _, ok := args[k]; ok {
			t.Errorf("%q was forwarded to the server; it must be stripped", k)
		}
	}
	for _, k := range []string{"query_text", "top_k", "language"} {
		if _, ok := args[k]; !ok {
			t.Errorf("server argument %q was lost", k)
		}
	}
	if ov.maxSnippetLines == nil || *ov.maxSnippetLines != 20 {
		t.Errorf("maxSnippetLines = %v, want 20", ov.maxSnippetLines)
	}
	if ov.snippets == nil || *ov.snippets {
		t.Errorf("snippets = %v, want false", ov.snippets)
	}
}

// A message carrying no override must be forwarded byte-for-byte. This runs on
// every message on the wire, including ones the proxy knows nothing about, so a
// re-marshal that silently reorders or drops a field would break them.
func TestTakeHydrationOverrides_PassesOtherMessagesThroughUnchanged(t *testing.T) {
	for _, body := range [][]byte{
		toolCall(t, map[string]any{"query_text": "x", "top_k": 4}),
		[]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`),
		[]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`),
		[]byte(`not json at all`),
	} {
		out, ov := takeHydrationOverrides(body)
		if string(out) != string(body) {
			t.Errorf("body rewritten:\n got %s\nwant %s", out, body)
		}
		if !ov.empty() {
			t.Errorf("overrides found in %s, want none", body)
		}
	}
}

// The overrides fold into a Config copy; a call that passes neither argument
// must leave the operator's `serve` settings exactly as configured.
func TestHydrationOverrides_Apply(t *testing.T) {
	base := Config{MaxSnippetLines: 80}
	five, zero, negative := 5, 0, -3
	no, yes := false, true

	for _, tc := range []struct {
		name       string
		ov         hydrationOverrides
		wantLines  int
		wantNoSnip bool
	}{
		{"empty leaves config alone", hydrationOverrides{}, 80, false},
		{"explicit budget", hydrationOverrides{maxSnippetLines: &five}, 5, false},
		{"snippets false", hydrationOverrides{snippets: &no}, 80, true},
		{"snippets true is a no-op", hydrationOverrides{snippets: &yes}, 80, false},
		// "at most zero lines" is a request for no bodies, not for the default
		// budget — which is what a 0 means to snippetLineBudget.
		{"zero budget means no snippets", hydrationOverrides{maxSnippetLines: &zero}, 80, true},
		{"negative budget means no snippets", hydrationOverrides{maxSnippetLines: &negative}, 80, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ov.apply(base)
			if got.MaxSnippetLines != tc.wantLines {
				t.Errorf("MaxSnippetLines = %d, want %d", got.MaxSnippetLines, tc.wantLines)
			}
			if got.NoSnippets != tc.wantNoSnip {
				t.Errorf("NoSnippets = %v, want %v", got.NoSnippets, tc.wantNoSnip)
			}
			if base.MaxSnippetLines != 80 || base.NoSnippets {
				t.Error("apply mutated the base Config instead of copying it")
			}
		})
	}
}

// End-to-end: a per-call budget actually changes what gets hydrated.
func TestHydrationOverrides_ChangeHydratedOutput(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/big.rb", 500, 240, "app")

	_, ov := takeHydrationOverrides(toolCall(t, map[string]any{
		"query_text":        "x",
		"max_snippet_lines": 12,
	}))
	r := firstResult(t, enrichQueryResponse(ov.apply(cfg), payload))

	s, ok := unmarshalString(r["snippet"])
	if !ok {
		t.Fatalf("no snippet (hydration=%s)", r["hydration"])
	}
	if got := len(strings.Split(s, "\n")); got != 12 {
		t.Errorf("snippet has %d lines, want the per-call 12", got)
	}
}
