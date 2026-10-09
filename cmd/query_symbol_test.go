package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestQueryToolArgs_SymbolSendsMatchOnlyWhenAsked(t *testing.T) {
	resetQueryFilterFlags(t)
	if _, sent := queryToolArgs("retry", formatHuman, false)["match"]; sent {
		t.Fatal("hybrid request must not send `match`: an older server sees the old request")
	}
	args := queryToolArgs("isSearchStage", formatHuman, true)
	if args["match"] != "symbol" || args["query_text"] != "isSearchStage" {
		t.Fatalf("args = %#v", args)
	}
}

func TestCorporaRejectsSymbolFlag(t *testing.T) {
	errs, _ := corporaFlagConflicts(func(f string) bool { return f == "symbol" })
	if len(errs) != 1 || errs[0] != "--symbol" {
		t.Fatalf("errs = %v, want [--symbol]", errs)
	}
}

func TestHasMatchEcho(t *testing.T) {
	if hasMatchEcho(json.RawMessage(`{"results":[]}`)) {
		t.Fatal("no echo reported as echo")
	}
	if !hasMatchEcho(json.RawMessage(`{"match_used":"hybrid","results":[]}`)) {
		t.Fatal("echo not seen")
	}
}

func TestRenderQueryHuman_SymbolTagAndFallbackNotice(t *testing.T) {
	var buf bytes.Buffer
	payload := json.RawMessage(`{"freshness":"fresh","searched_repos":["r"],` +
		`"match_used":"symbol","symbol_found":true,"results":[{"repo_id":"r",` +
		`"path_token":"a.go","line_start":1,"line_end":9,"score":1,"content_kind":"code"}]}`)
	if err := renderQueryHuman(&buf, payload, nil, true); err != nil {
		t.Fatal(err)
	}
	if first := strings.SplitN(buf.String(), "\n", 2)[0]; !strings.Contains(first, "symbol definitions") {
		t.Fatalf("header = %q", first)
	}

	buf.Reset()
	payload = json.RawMessage(`{"freshness":"fresh","searched_repos":["r"],` +
		`"match_used":"hybrid","symbol_found":false,"results":[]}`)
	if err := renderQueryHuman(&buf, payload, nil, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no definition found — showing hybrid results") {
		t.Fatalf("missing fallback notice:\n%s", buf.String())
	}
}
