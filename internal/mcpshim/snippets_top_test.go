package mcpshim

import (
	"encoding/json"
	"strings"
	"testing"
)

// rankedResponse is bigFileResponse with n hits on the same short file, in
// rank order — enough to see where a SnippetsTop cut falls.
func rankedResponse(t *testing.T, n int) (Config, []byte) {
	t.Helper()
	cfg, one := bigFileResponse(t, "app/models/small.rb", 50, 5, "app")
	var env map[string]any
	if err := json.Unmarshal(one, &env); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	hit := env["results"].([]any)[0]
	results := make([]any, n)
	for i := range results {
		results[i] = hit
	}
	env["results"] = results
	out, _ := json.Marshal(env)
	return cfg, out
}

func hydrationByRank(t *testing.T, payload []byte) []string {
	t.Helper()
	var env struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("decode enriched: %v", err)
	}
	out := make([]string, len(env.Results))
	for i, r := range env.Results {
		if _, ok := r["snippet"]; ok {
			out[i] = "body"
			continue
		}
		out[i], _ = unmarshalString(r["hydration"])
	}
	return out
}

func TestSnippetsTop_HydratesOnlyTheTopRanks(t *testing.T) {
	for _, tc := range []struct {
		name string
		top  int
		want []string
	}{
		{"zero is the built-in top 3", 0, []string{"body", "body", "body", hydrationBeyondSnippetsTop, hydrationBeyondSnippetsTop}},
		{"explicit count", 1, []string{"body", hydrationBeyondSnippetsTop, hydrationBeyondSnippetsTop, hydrationBeyondSnippetsTop, hydrationBeyondSnippetsTop}},
		{"negative hydrates all", -1, []string{"body", "body", "body", "body", "body"}},
		{"count above the hit count", 10, []string{"body", "body", "body", "body", "body"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, payload := rankedResponse(t, 5)
			cfg.SnippetsTop = tc.top
			got := hydrationByRank(t, enrichQueryResponse(cfg, payload))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("hydration by rank = %v, want %v", got, tc.want)
			}
		})
	}
}

// The cut is stated once in the header; repeating it under every lower hit
// would spend bytes restating the mode — the same rule as snippets_disabled.
func TestSnippetsTop_AgentRenderStatesCutOnce(t *testing.T) {
	cfg, payload := rankedResponse(t, 5)
	cfg.SnippetsTop = 2
	text, ok := renderQueryText(enrichQueryResponse(cfg, payload), RenderOptions{})
	if !ok {
		t.Fatal("render failed")
	}
	if !strings.Contains(text, "snippets:top 2") {
		t.Errorf("header does not state the cut:\n%s", text)
	}
	if strings.Contains(text, hydrationBeyondSnippetsTop) {
		t.Errorf("per-hit reason repeated the header:\n%s", text)
	}
}

func TestSnippetsTop_NoCutKeepsHeader(t *testing.T) {
	cfg, payload := rankedResponse(t, 2)
	text, _ := renderQueryText(enrichQueryResponse(cfg, payload), RenderOptions{})
	if !strings.Contains(text, "snippets:on") {
		t.Errorf("a response with no cut should read snippets:on:\n%s", text)
	}
}

func TestSnippetsTop_PerCallOverride(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"QUERY","arguments":{"query_text":"x","snippets_top":5}}}`)
	fwd, ov := takeCallOverrides(Config{}, body)
	if ov.snippetsTop == nil || *ov.snippetsTop != 5 {
		t.Fatalf("snippets_top not taken: %+v", ov)
	}
	if strings.Contains(string(fwd), "snippets_top") {
		t.Errorf("client-only snippets_top forwarded to the server: %s", fwd)
	}
	if got := ov.apply(Config{SnippetsTop: 3}); got.SnippetsTop != 5 || got.NoSnippets {
		t.Errorf("apply = top %d noSnippets %v, want 5/false", got.SnippetsTop, got.NoSnippets)
	}
	zero := 0
	if got := (callOverrides{snippetsTop: &zero}).apply(Config{}); !got.NoSnippets {
		t.Error("snippets_top=0 should mean no bodies")
	}
}
