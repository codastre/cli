package mcpshim

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A two-repo response where one file is hit twice and the repos interleave by
// rank — the shape measured against the live index, and the one the grouping
// exists for.
const renderFixture = `{
  "status": "ok",
  "freshness": "fresh",
  "mask_key_rev": 0,
  "searched_repo_count": 93,
  "repos": {
    "repo-ios": {"remote_url": "github.com/acme/ios"},
    "repo-go": {"remote_url": "github.com/acme/api"}
  },
  "results": [
    {"repo_id": "repo-ios", "path_token": "App/Chat/InputBar.swift", "real_path": "App/Chat/InputBar.swift",
     "line_start": 250, "line_end": 291, "score": 0.01639344262295082, "content_kind": "code",
     "symbol_name": "InputBarViewModel", "blob_sha": "aaaa1111", "chunk_id": "c1",
     "snippet": "func send() {\n    upload(attachment)\n}", "snippet_truncated": true, "snippet_line_end": 252},
    {"repo_id": "repo-go", "path_token": "internal/wise/quote.go", "real_path": "internal/wise/quote.go",
     "line_start": 0, "line_end": 240, "score": 0.016129032258064516, "content_kind": "code",
     "blob_sha": "bbbb2222", "chunk_id": "c2", "hydration": "no_local_checkout"},
    {"repo_id": "repo-ios", "path_token": "App/Chat/InputBar.swift", "real_path": "App/Chat/InputBar.swift",
     "line_start": 170, "line_end": 201, "score": 0.015873015873015872, "content_kind": "code",
     "symbol_name": "InputBarViewModel", "blob_sha": "aaaa1111", "chunk_id": "c3",
     "hydration": "snippets_disabled"},
    {"repo_id": "repo-go", "path_token": "web/dist/bundle.min.js", "real_path": "web/dist/bundle.min.js",
     "line_start": 1, "line_end": 4, "score": 0.015625, "content_kind": "text",
     "path_class": "vendored", "blob_sha": "cccc3333", "chunk_id": "c4", "stale": true,
     "snippet": "var x=1"}
  ]
}`

func render(t *testing.T, payload string, opts RenderOptions) string {
	t.Helper()
	out, ok := renderQueryText([]byte(payload), opts)
	if !ok {
		t.Fatal("renderQueryText rejected the payload")
	}
	return out
}

// The saving the whole format is for: a path printed once per file rather than
// once per hit, and never as both path_token and real_path.
func TestRenderQueryText_GroupsHitsByFile(t *testing.T) {
	out := render(t, renderFixture, RenderOptions{})

	// One file header for two hits. Counted as headers rather than as substrings
	// because the path deliberately reappears in a truncation's "Read <path>:a-b"
	// citation, which is a handover, not repetition.
	if n := strings.Count(out, "\n  App/Chat/InputBar.swift @"); n != 1 {
		t.Errorf("file header printed %d times, want 1 (two hits share the file)\n%s", n, out)
	}
	if n := strings.Count(out, "github.com/acme/ios"); n != 1 {
		t.Errorf("repo printed %d times, want 1 — its hits ranked 1st and 3rd, so\n"+
			"the groups must be gathered rather than left interleaved\n%s", n, out)
	}
	// Gathering must not reorder within a repo, and the ranking must survive.
	iosAt, goAt := strings.Index(out, "github.com/acme/ios"), strings.Index(out, "github.com/acme/api")
	if iosAt > goAt {
		t.Error("repos are not ordered by their best hit")
	}
	if first, second := strings.Index(out, "250-291"), strings.Index(out, "170-201"); first > second {
		t.Error("hits within a file are not in rank order")
	}
}

// Scores are RRF-scale in production; two decimals would render every rank as
// "0.02". This is the assertion that keeps that from creeping back.
func TestRenderQueryText_ScoresStayDistinguishable(t *testing.T) {
	out := render(t, renderFixture, RenderOptions{})

	for _, want := range []string{".0164", ".0161", ".0159", ".0156"} {
		if !strings.Contains(out, want) {
			t.Errorf("score %s missing — RRF-scale scores must stay distinct\n%s", want, out)
		}
	}
}

// F7: citations drift when a line number is derived from a chunk range. Each
// body line carries its own real number so nothing has to be derived.
func TestRenderQueryText_NumbersBodyLines(t *testing.T) {
	out := render(t, renderFixture, RenderOptions{})

	for _, want := range []string{"250│ func send() {", "251│     upload(attachment)", "252│ }"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing numbered body line %q\n%s", want, out)
		}
	}
	// Truncation hands over the exact range to Read rather than just saying
	// something is missing.
	if !strings.Contains(out, "Read App/Chat/InputBar.swift:253-291") {
		t.Errorf("truncation marker should name the resume range\n%s", out)
	}
}

func TestRenderQueryText_AnnotatesHits(t *testing.T) {
	out := render(t, renderFixture, RenderOptions{})

	for _, want := range []string{
		"no body: no_local_checkout", // per-hit reason, actionable
		"vendored",                   // non-default path_class
		"text",                       // non-default content_kind
		"stale",                      // the body is a lead, not a quote
		"@aaaa1111",                  // blob anchor, once per file
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing annotation %q\n%s", want, out)
		}
	}
	// Defaults are the reader's existing assumption; printing them is noise.
	if strings.Contains(out, "· code") || strings.Contains(out, "· app") {
		t.Errorf("default content_kind/path_class should not be printed\n%s", out)
	}
}

// In snippets:off mode the header already says bodies were not requested;
// repeating it per hit spends bytes restating the mode.
func TestRenderQueryText_SuppressesRedundantDisabledReason(t *testing.T) {
	on := render(t, renderFixture, RenderOptions{})
	off := render(t, renderFixture, RenderOptions{NoSnippets: true})

	if !strings.Contains(on, "snippets_disabled") {
		t.Error("with hydration on, a per-result snippets_disabled is real information")
	}
	if strings.Contains(off, "snippets_disabled") {
		t.Errorf("snippets:off header already says it; per-hit repeats are noise\n%s", off)
	}
	if !strings.Contains(off, "snippets:off") {
		t.Errorf("header should name the tier\n%s", off)
	}
	// Reasons that are NOT the global mode still have to survive.
	if !strings.Contains(off, "no_local_checkout") {
		t.Errorf("a per-hit hydration failure must still be reported\n%s", off)
	}
}

// The rendering must beat the JSON it replaces, or it is only a different shape.
func TestRenderQueryText_IsSmallerThanTheJSON(t *testing.T) {
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, []byte(renderFixture)); err != nil {
		t.Fatalf("compact fixture: %v", err)
	}
	out := render(t, renderFixture, RenderOptions{})

	if len(out) >= compacted.Len() {
		t.Errorf("rendering is %d B, JSON is %d B — no saving", len(out), compacted.Len())
	}
	t.Logf("rendering %d B vs JSON %d B (%.0f%% smaller)",
		len(out), compacted.Len(), 100*(1-float64(len(out))/float64(compacted.Len())))
}

func TestRenderQueryText_EmptyResults(t *testing.T) {
	out := render(t, `{"status":"ok","freshness":"fresh","searched_repos":["a"],
	  "filter_matched": false, "results": []}`, RenderOptions{})

	if !strings.Contains(out, "no matches") {
		t.Errorf("empty results must read as an answer, not an error\n%s", out)
	}
	// "searched, nothing matched" and "your prefix matched nothing indexed" are
	// different answers with different fixes.
	if !strings.Contains(out, "path_prefix matched nothing indexed") {
		t.Errorf("filter_matched=false must be surfaced\n%s", out)
	}
}

// `codastre query` renders the raw server envelope, which has no real_path.
func TestRenderQueryText_UnmasksWhenAsked(t *testing.T) {
	payload := `{"status":"ok","freshness":"fresh","mask_key_rev":3,"results":[
	  {"repo_id":"r","path_token":"deadbeef","line_start":1,"line_end":2,"score":0.5}]}`

	gotRev := -1
	out := render(t, payload, RenderOptions{Unmask: func(token string, rev int) (string, bool) {
		gotRev = rev
		return "app/models/user.rb", true
	}})

	if !strings.Contains(out, "app/models/user.rb") {
		t.Errorf("unmasked path missing\n%s", out)
	}
	if gotRev != 3 {
		t.Errorf("unmasked at rev %d, want the envelope's 3", gotRev)
	}
	// An HMAC token that cannot be unmasked is labelled, so it is never mistaken
	// for a path that is simply absent locally.
	hmac := strings.Repeat("a1b2", 16) // 64 lowercase hex chars, as §8.3 emits
	out = render(t, strings.Replace(payload, "deadbeef", hmac, 1),
		RenderOptions{Unmask: func(string, int) (string, bool) { return "", false }})
	if !strings.Contains(out, hmac+" [masked]") {
		t.Errorf("un-unmaskable HMAC token should be marked\n%s", out)
	}
}

// A `none`-scheme repo's path_token IS the path. Marking it "[masked]" would
// state the opposite of the truth, and `codastre query` reaches the renderer
// without the proxy's per-repo scheme lookup to tell it apart.
func TestRenderQueryText_CleartextTokenIsNotMarked(t *testing.T) {
	payload := `{"status":"ok","freshness":"fresh","results":[
	  {"repo_id":"r","path_token":"app/models/user.rb","line_start":1,"line_end":2,"score":0.5}]}`

	out := render(t, payload, RenderOptions{Unmask: func(string, int) (string, bool) { return "", false }})

	if strings.Contains(out, "[masked]") {
		t.Errorf("cleartext token marked as masked\n%s", out)
	}
	if !strings.Contains(out, "app/models/user.rb") {
		t.Errorf("path missing\n%s", out)
	}
}

// The blob anchor is printed abbreviated. On the deployed index the full 40-hex
// form was 20% of a locate-tier response — the largest reducible line in it —
// and both of its consumers compare by prefix.
func TestRenderQueryText_AbbreviatesBlobSHA(t *testing.T) {
	const full = "788306271f689ff37b61f2995c3b46e9e2f4b238"
	payload := `{"status":"ok","freshness":"fresh","repos":{"r":{"remote_url":"github.com/acme/ios"}},
	  "results":[{"repo_id":"r","path_token":"App/Recall.swift","real_path":"App/Recall.swift",
	  "line_start":2,"line_end":34,"score":0.5833333,"content_kind":"code",
	  "symbol_name":"RecallServiceImpl","blob_sha":"` + full + `"}]}`

	out := render(t, payload, RenderOptions{NoSnippets: true})

	if !strings.Contains(out, "@"+full[:blobSHAAbbrev]) {
		t.Errorf("abbreviated blob sha missing\n%s", out)
	}
	if strings.Contains(out, full) {
		t.Errorf("full 40-hex blob sha still printed\n%s", out)
	}
}

// A sha already shorter than the abbreviation is passed through, not padded or
// dropped: fixtures and re-rendered output must survive a round trip.
func TestAbbrevBlobSHA_PassesShortValuesThrough(t *testing.T) {
	for _, s := range []string{"", "aaaa1111", strings.Repeat("b", blobSHAAbbrev)} {
		if got := abbrevBlobSHA(s); got != s {
			t.Errorf("abbrevBlobSHA(%q) = %q, want unchanged", s, got)
		}
	}
}
