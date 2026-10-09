package mcpshim

import (
	"strings"
	"testing"
)

func TestParseSymbolQuery(t *testing.T) {
	cases := []struct {
		text      string
		flag      bool
		wantText  string
		wantSym   bool
		wantError bool
	}{
		{"retry policy", false, "retry policy", false, false},
		{"isSearchStage", true, "isSearchStage", true, false},
		{" Session.Merge ", true, "Session.Merge", true, false},
		{"symbol:pkg::Type", false, "pkg::Type", true, false},
		{"symbol:isSearchStage", true, "isSearchStage", true, false},
		{"where is X defined", true, "", false, true},
		{"symbol:foo()", false, "", false, true},
		{"symbol:", false, "", false, true},
		// The prefix is sugar only at the start; elsewhere it is ordinary text.
		{"the symbol:x thing", false, "the symbol:x thing", false, false},
	}
	for _, c := range cases {
		got, sym, err := ParseSymbolQuery(c.text, c.flag)
		if c.wantError {
			if err == nil {
				t.Errorf("ParseSymbolQuery(%q, %v): want error, got %q", c.text, c.flag, got)
			}
			continue
		}
		if err != nil || got != c.wantText || sym != c.wantSym {
			t.Errorf("ParseSymbolQuery(%q, %v) = (%q, %v, %v), want (%q, %v, nil)",
				c.text, c.flag, got, sym, err, c.wantText, c.wantSym)
		}
	}
}

func renderMatch(t *testing.T, payload string, opts RenderOptions) []string {
	t.Helper()
	text, ok := RenderQueryText([]byte(payload), opts)
	if !ok {
		t.Fatal("render failed")
	}
	return strings.Split(text, "\n")
}

const symbolHit = `{"repo_id":"r","path_token":"cli/pipeline.go","line_start":10,"line_end":30,` +
	`"score":1.0,"symbol_name":"isSearchStage","match":"symbol"}`

func TestRenderQueryText_SymbolAnswerIsTagged(t *testing.T) {
	lines := renderMatch(t, `{"status":"ok","freshness":"fresh","searched_repos":["r"],`+
		`"match_used":"symbol","symbol_found":true,"results":[`+symbolHit+`]}`,
		RenderOptions{NoSnippets: true, SymbolRequested: true, QueryText: "isSearchStage"})
	if !strings.Contains(lines[0], "· symbol definitions ·") {
		t.Fatalf("header = %q, want the symbol tag", lines[0])
	}
	if strings.Contains(strings.Join(lines, "\n"), "no definition") {
		t.Fatal("a found definition must not carry the fallback notice")
	}
}

func TestRenderQueryText_FallbackNamesTheSymbol(t *testing.T) {
	lines := renderMatch(t, `{"status":"ok","freshness":"fresh","searched_repos":["r"],`+
		`"match_used":"hybrid","symbol_found":false,"results":[]}`,
		RenderOptions{NoSnippets: true, SymbolRequested: true, QueryText: "isSearchStage"})
	if strings.Contains(lines[0], "symbol definitions") {
		t.Fatalf("hybrid fallback tagged as definitions: %q", lines[0])
	}
	if lines[1] != "no definition named `isSearchStage` — showing hybrid results" {
		t.Fatalf("notice = %q", lines[1])
	}
}

func TestRenderQueryText_OldServerIgnoringMatchIsLabelled(t *testing.T) {
	// No match_used: the server predates symbol mode and ran hybrid.
	payload := `{"status":"ok","freshness":"fresh","searched_repos":["r"],"results":[]}`
	lines := renderMatch(t, payload, RenderOptions{NoSnippets: true, SymbolRequested: true})
	if !strings.Contains(lines[1], "does not support symbol mode") {
		t.Fatalf("missing old-server label: %q", lines[1])
	}
	// The proxy (no request knowledge) renders the same envelope unlabelled.
	lines = renderMatch(t, payload, RenderOptions{NoSnippets: true})
	if lines[1] != "no matches" {
		t.Fatalf("unexpected line for a plain hybrid envelope: %q", lines[1])
	}
}
