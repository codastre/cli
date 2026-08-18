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

	out, ov := takeCallOverrides(body)

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
		out, ov := takeCallOverrides(body)
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
		ov         callOverrides
		wantLines  int
		wantNoSnip bool
	}{
		{"empty leaves config alone", callOverrides{}, 80, false},
		{"explicit budget", callOverrides{maxSnippetLines: &five}, 5, false},
		{"snippets false", callOverrides{snippets: &no}, 80, true},
		{"snippets true is a no-op", callOverrides{snippets: &yes}, 80, false},
		// "at most zero lines" is a request for no bodies, not for the default
		// budget — which is what a 0 means to snippetLineBudget.
		{"zero budget means no snippets", callOverrides{maxSnippetLines: &zero}, 80, true},
		{"negative budget means no snippets", callOverrides{maxSnippetLines: &negative}, 80, true},
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

	_, ov := takeCallOverrides(toolCall(t, map[string]any{
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

// `format` is the one override the server shares. It must reach the server —
// rewritten to a value the server understands when the caller asked for the one
// it cannot produce, and untouched otherwise.
func TestTakeCallOverrides_Format(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sent       any
		wantOnWire any
		wantFormat string
	}{
		// agent is rendered by the proxy, so the server is asked for the compact
		// JSON the rendering is built from rather than the verbose default.
		{"agent is rewritten to compact", "agent", "compact", formatAgent},
		{"compact is forwarded", "compact", "compact", formatJSON},
		{"verbose is forwarded", "verbose", "verbose", formatJSON},
		// "json" is the Config spelling (serve --format json, and the env var),
		// not a wire value — the server's enum is verbose|compact. It is a
		// synonym for verbose rather than a value left to strand: without the
		// rewrite it reached the server unchanged, and without a case in apply
		// it silently inherited `serve --format agent` and came back as text —
		// the one spelling of "I want JSON" that did not get JSON.
		{"json is rewritten to verbose", "json", "verbose", formatJSON},
		// An unknown value is the server's to reject: INVALID_REQUEST from the
		// tool that owns the argument beats a silent fallback here.
		{"unknown is forwarded untouched", "xml", "xml", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := toolCall(t, map[string]any{"query_text": "x", "format": tc.sent})

			out, ov := takeCallOverrides(body)

			got, _ := unmarshalString(callArguments(t, out)[argFormat])
			if got != tc.wantOnWire {
				t.Errorf("forwarded format %q, want %q", got, tc.wantOnWire)
			}
			cfg := ov.apply(Config{})
			if cfg.Format != tc.wantFormat {
				t.Errorf("Config.Format = %q, want %q", cfg.Format, tc.wantFormat)
			}
		})
	}
}

// GRAPH renders too, so it gets the same rewrite QUERY does: `agent` is the one
// value the server cannot produce, and compact is what its rendering reads.
func TestTakeCallOverrides_FormatOnGraph(t *testing.T) {
	body := namedToolCall(t, "GRAPH", map[string]any{"chunk_or_symbol": "X", "format": "agent"})

	out, ov := takeCallOverrides(body)

	if got, _ := unmarshalString(callArguments(t, out)[argFormat]); got != formatCompact {
		t.Errorf("forwarded format %q, want %q", got, formatCompact)
	}
	if ov.apply(Config{}).Format != formatAgent {
		t.Error("GRAPH's format=agent did not switch the proxy into agent rendering")
	}
}

// The rewrite is keyed to the two tools that declare `format`. Any other tool
// growing a same-named argument must not be silently rewritten by this proxy.
func TestTakeCallOverrides_FormatIsRenderedToolsOnly(t *testing.T) {
	body := namedToolCall(t, "SYNC", map[string]any{"index_id": "X", "format": "agent"})

	out, ov := takeCallOverrides(body)

	if got, _ := unmarshalString(callArguments(t, out)[argFormat]); got != "agent" {
		t.Errorf("SYNC's format was rewritten to %q; it belongs to that tool", got)
	}
	if ov.apply(Config{}).Format == formatAgent {
		t.Error("a SYNC argument switched the proxy into agent rendering")
	}
}

// namedToolCall builds a tools/call body for an arbitrary tool name.
func namedToolCall(t *testing.T, name string, args map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}
