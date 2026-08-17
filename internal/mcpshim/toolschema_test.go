package mcpshim

import (
	"encoding/json"
	"testing"
)

func toolListResponse(t *testing.T, toolNames ...string) []byte {
	t.Helper()
	tools := make([]map[string]any, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, map[string]any{
			"name":        name,
			"description": name + " tool",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query_text": map[string]any{"type": "string"},
					"top_k":      map[string]any{"type": "integer"},
				},
				"required": []string{"query_text"},
			},
		})
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  map[string]any{"tools": tools},
	})
	if err != nil {
		t.Fatalf("marshal tools/list response: %v", err)
	}
	return b
}

func toolSchemaProps(t *testing.T, data []byte, toolName string) map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal tools/list response: %v", err)
	}
	for _, tool := range env.Result.Tools {
		if tool.Name == toolName {
			return tool.InputSchema.Properties
		}
	}
	t.Fatalf("tool %q missing from response", toolName)
	return nil
}

// The point of the whole file: the hydration arguments are implemented in this
// proxy, so unless the proxy advertises them no agent can discover them and a
// strict client may refuse to send them.
func TestAnnotateToolList_AdvertisesHydrationArgs(t *testing.T) {
	cfg := Config{RepoScheme: func(string) (string, bool) { return "none", true }}

	out := annotateToolList(cfg, toolListResponse(t, "QUERY", "GRAPH"))

	props := toolSchemaProps(t, out, "QUERY")
	for _, name := range []string{argSnippets, argMaxSnippetLines} {
		if _, ok := props[name]; !ok {
			t.Errorf("%q missing from the advertised QUERY schema", name)
		}
	}
	// The server's own arguments must survive the rewrite.
	for _, name := range []string{"query_text", "top_k"} {
		if _, ok := props[name]; !ok {
			t.Errorf("server argument %q was dropped from the QUERY schema", name)
		}
	}
	// GRAPH hydrates nothing, so the dials would be inert there.
	graphProps := toolSchemaProps(t, out, "GRAPH")
	for _, name := range []string{argSnippets, argMaxSnippetLines} {
		if _, ok := graphProps[name]; ok {
			t.Errorf("%q advertised on GRAPH, which does not hydrate", name)
		}
	}
}

// Advertising a dial that cannot move anything is worse than not advertising it:
// the overrides can only turn hydration off, never back on.
func TestAnnotateToolList_SilentWhenHydrationUnavailable(t *testing.T) {
	scheme := func(string) (string, bool) { return "none", true }
	for name, cfg := range map[string]Config{
		"no-snippets":   {RepoScheme: scheme, NoSnippets: true},
		"cannot-enrich": {
			// Neither an unmasker nor scheme knowledge: nothing is ever hydrated.
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := toolListResponse(t, "QUERY")
			if got := annotateToolList(cfg, in); string(got) != string(in) {
				t.Errorf("response was rewritten; want it untouched\n got: %s", got)
			}
		})
	}
}

// Run passes every message through annotateToolList, including ones it knows
// nothing about. Those must come back byte-identical.
func TestAnnotateToolList_LeavesOtherMessagesUntouched(t *testing.T) {
	cfg := Config{RepoScheme: func(string) (string, bool) { return "none", true }}
	cases := map[string]string{
		"not json":        `{"jsonrpc": "2.0"`,
		"no result":       `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
		"tool result":     `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":[]}}}`,
		"tools not array": `{"jsonrpc":"2.0","id":1,"result":{"tools":"nope"}}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := annotateToolList(cfg, []byte(in)); string(got) != in {
				t.Errorf("message was rewritten\n got: %s\nwant: %s", got, in)
			}
		})
	}
}

// If a future server version declares these arguments itself, its definition is
// the authoritative one — the proxy must not clobber it.
func TestAnnotateToolList_KeepsServerDefinition(t *testing.T) {
	schema, ok := withHydrationArgs(json.RawMessage(
		`{"type":"object","properties":{"snippets":{"type":"string","description":"server owned"}}}`,
	))
	if !ok {
		t.Fatal("withHydrationArgs returned not-ok on a schema missing max_snippet_lines")
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(schema, &got); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal(got["properties"], &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	var snippets struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(props[argSnippets], &snippets); err != nil {
		t.Fatalf("unmarshal %s: %v", argSnippets, err)
	}
	if snippets.Description != "server owned" {
		t.Errorf("proxy overwrote the server's %s definition: %+v", argSnippets, snippets)
	}
	if _, ok := props[argMaxSnippetLines]; !ok {
		t.Errorf("%q missing: the proxy should still add what the server omits", argMaxSnippetLines)
	}
}
