package mcpshim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// bigFileResponse builds a QUERY envelope with one `none`-scheme result whose
// span covers lines 1..span of a file written into a temp checkout.
func bigFileResponse(t *testing.T, relPath string, fileLines, span int, pathClass string) (Config, []byte) {
	t.Helper()
	const repoID = "repo-uuid"
	root := t.TempDir()
	abs := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := make([]string, fileLines)
	for i := range body {
		body[i] = fmt.Sprintf("line %d", i+1)
	}
	if err := os.WriteFile(abs, []byte(strings.Join(body, "\n")), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result := map[string]any{
		"chunk_id":   "c1",
		"repo_id":    repoID,
		"path_token": relPath, // `none` scheme: the token IS the real path
		"line_start": 1,
		"line_end":   span,
	}
	if pathClass != "" {
		result["path_class"] = pathClass
	}
	env, _ := json.Marshal(map[string]any{
		"status":        "ok",
		"mask_key_revs": map[string]any{repoID: 0},
		"results":       []map[string]any{result},
	})

	cfg := Config{
		RepoRoot:   root,
		CWDRepoID:  repoID,
		RepoScheme: func(string) (string, bool) { return "none", true },
	}
	return cfg, env
}

func snippetLines(t *testing.T, r map[string]json.RawMessage) int {
	t.Helper()
	s, ok := unmarshalString(r["snippet"])
	if !ok {
		t.Fatalf("result carries no snippet (hydration=%s)", r["hydration"])
	}
	return len(strings.Split(s, "\n"))
}

// C1: a span wider than the budget is trimmed, and the result says so. The
// server already clamps spans to 240 lines, but 240 lines of source per hit ×
// top_k is still the dominant cost of a response.
func TestHydration_CapsSnippetLinesAndMarksTruncation(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/big.rb", 500, 240, "app")

	r := firstResult(t, enrichQueryResponse(cfg, payload))

	if got := snippetLines(t, r); got != defaultMaxSnippetLines {
		t.Errorf("snippet has %d lines, want %d", got, defaultMaxSnippetLines)
	}
	var truncated bool
	if err := json.Unmarshal(r["snippet_truncated"], &truncated); err != nil || !truncated {
		t.Error("a trimmed snippet must be marked snippet_truncated so the agent knows to Read for more")
	}
	var snippetEnd int
	if err := json.Unmarshal(r["snippet_line_end"], &snippetEnd); err != nil {
		t.Fatalf("snippet_line_end missing on a truncated result: %v", err)
	}
	if snippetEnd != defaultMaxSnippetLines {
		t.Errorf("snippet_line_end = %d, want %d (last line delivered)", snippetEnd, defaultMaxSnippetLines)
	}
	// line_start/line_end describe the chunk the ranker scored. Rewriting them
	// client-side would make the result disclaim the span it was ranked on.
	var lineEnd int
	if err := json.Unmarshal(r["line_end"], &lineEnd); err != nil || lineEnd != 240 {
		t.Errorf("line_end = %d, want the server's 240 left intact", lineEnd)
	}
}

// A span that fits the budget is delivered whole and NOT marked truncated —
// the marker has to mean something.
func TestHydration_ShortSpanIsNotMarkedTruncated(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/small.rb", 500, 30, "app")

	r := firstResult(t, enrichQueryResponse(cfg, payload))

	if got := snippetLines(t, r); got != 30 {
		t.Errorf("snippet has %d lines, want 30 (the whole span)", got)
	}
	if _, ok := r["snippet_truncated"]; ok {
		t.Error("an untrimmed snippet must not be marked snippet_truncated")
	}
}

// A span overhanging the end of a shorter file delivered every line it had, so
// it is complete, not truncated. Distinguishing this from a budget cut is why
// truncation is detected while reading rather than from (line_end - line_start).
func TestHydration_SpanPastEndOfFileIsNotTruncated(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/short.rb", 12, 40, "app")

	r := firstResult(t, enrichQueryResponse(cfg, payload))

	if got := snippetLines(t, r); got != 12 {
		t.Errorf("snippet has %d lines, want 12 (the whole file)", got)
	}
	if _, ok := r["snippet_truncated"]; ok {
		t.Error("a span that ran off the end of the file delivered everything it had")
	}
}

// C3: derived content is demoted to a headline. In the measured run a generated
// admin-framework schema dump and a SQL structure dump were 27% of the payload and
// answered nothing.
func TestHydration_DerivedArtifactGetsHeadlineBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		path      string
		pathClass string
	}{
		{"generated schema dump", "app/.admin-schema.json", "app"},
		{"sql structure dump", "db/reporting_structure.sql", "app"},
		{"lockfile", "Gemfile.lock", "app"},
		{"build output", "dist/bundle.js", "app"},
		{"server-classified generated", "internal/store/mock_store.go", "generated"},
		{"server-classified vendored", "third_party/lib/thing.go", "vendored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, payload := bigFileResponse(t, tc.path, 500, 240, tc.pathClass)

			r := firstResult(t, enrichQueryResponse(cfg, payload))

			if got := snippetLines(t, r); got != demotedSnippetLines {
				t.Errorf("snippet has %d lines, want %d", got, demotedSnippetLines)
			}
			// The result is still returned in full otherwise: only the body is
			// abbreviated, so a caller who does want it knows where to look.
			if _, ok := r["real_path"]; !ok {
				t.Error("a demoted result must still carry real_path")
			}
		})
	}
}

// `test` is NOT demoted: a test body is frequently the answer to "where is X
// tested", and the server's de-ranker already handles ordering.
func TestHydration_TestClassIsNotDemoted(t *testing.T) {
	cfg, payload := bigFileResponse(t, "spec/models/user_spec.rb", 500, 240, "test")

	r := firstResult(t, enrichQueryResponse(cfg, payload))

	if got := snippetLines(t, r); got != defaultMaxSnippetLines {
		t.Errorf("snippet has %d lines, want the full budget %d", got, defaultMaxSnippetLines)
	}
}

// C5: --no-snippets returns ranked locations only, and says so rather than
// leaving the agent to read a missing snippet as a failure it should repair.
func TestHydration_NoSnippetsReturnsLocationsOnly(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/big.rb", 500, 240, "app")
	cfg.NoSnippets = true

	r := firstResult(t, enrichQueryResponse(cfg, payload))

	if _, ok := r["snippet"]; ok {
		t.Error("--no-snippets must not hydrate a body")
	}
	if _, ok := r["real_path"]; !ok {
		t.Error("--no-snippets still resolves the real path — the location IS the answer")
	}
	reason, _ := unmarshalString(r["hydration"])
	if reason != hydrationSnippetsDisabled {
		t.Errorf("hydration = %q, want %q", reason, hydrationSnippetsDisabled)
	}
}

// An explicit MaxSnippetLines overrides the built-in default in both directions.
func TestHydration_MaxSnippetLinesIsConfigurable(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/big.rb", 500, 240, "app")
	cfg.MaxSnippetLines = 15

	if got := snippetLines(t, firstResult(t, enrichQueryResponse(cfg, payload))); got != 15 {
		t.Errorf("snippet has %d lines, want the configured 15", got)
	}
}

// C6: cost is invisible at the point of use, which is why measuring it took a
// dedicated experiment. One line per response fixes that — on the Log writer,
// never on stdout, which carries the JSON-RPC stream.
func TestHydration_ReportsPayloadCostToLog(t *testing.T) {
	cfg, payload := bigFileResponse(t, "app/models/big.rb", 500, 240, "app")
	var log bytes.Buffer
	cfg.Log = &log

	enrichQueryResponse(cfg, payload)

	line := log.String()
	for _, want := range []string{"1 results", "1 hydrated", "80 lines", "tokens", "1 truncated"} {
		if !strings.Contains(line, want) {
			t.Errorf("cost line %q missing %q", strings.TrimSpace(line), want)
		}
	}
}

// A line budget alone does not bound a snippet: minified assets and one-line
// JSON put the whole file on line 1. Found in a live federated run, where such
// files failed hydration outright with bufio.Scanner's 64 KB ErrTooLong and were
// reported as `read_error` — sending the agent to check permissions on a file
// that is merely one long line.
func TestHydration_CapsIndividualLineLength(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "fixtures/nodes.json")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// One line, far past the old scanner ceiling. The trailing multi-byte rune
	// lands the cut mid-character if the clip is naive.
	body := strings.Repeat("é", 200_000)
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := hydrateSnippet(abs, 1, 1, defaultMaxSnippetLines, "")
	if err != nil {
		t.Fatalf("hydrateSnippet on a one-line file: %v", err)
	}
	if res.Lines != 1 {
		t.Fatalf("Lines = %d, want 1", res.Lines)
	}
	if len(res.Text) > maxSnippetLineChars+8 {
		t.Errorf("line delivered as %d bytes, want ≤ %d", len(res.Text), maxSnippetLineChars)
	}
	if !res.Truncated {
		t.Error("a shortened line must set Truncated")
	}
	if !strings.HasSuffix(res.Text, "…") {
		t.Error("a shortened line must end in the cut marker")
	}
	if !utf8.ValidString(res.Text) {
		t.Error("clipped mid-rune: the snippet is no longer valid UTF-8")
	}
}

func TestClipRunes(t *testing.T) {
	// "é" is 2 bytes: clipping to 3 must back off to 2, not split the second one.
	if got := clipRunes("ééé", 3); got != "é" {
		t.Errorf("clipRunes(\"ééé\", 3) = %q, want %q", got, "é")
	}
	if got := clipRunes("abc", 10); got != "abc" {
		t.Errorf("clipRunes should not touch a short string, got %q", got)
	}
}

func TestIsDerivedArtifact(t *testing.T) {
	derived := []string{
		"app/.admin-schema.json",
		"schemas/openapi-schema.json",
		"db/foo_structure.sql",
		"yarn.lock",
		"go.sum",
		"web/dist/app.js",
		"web/build/main.css",
		"assets/app.min.js",
		"bundle.js.map",
		"__snapshots__/thing.snap",
	}
	for _, p := range derived {
		if !isDerivedArtifact(p) {
			t.Errorf("isDerivedArtifact(%q) = false, want true", p)
		}
	}
	// Hand-written source that merely resembles the patterns must not be demoted.
	notDerived := []string{
		"app/models/schema.rb",
		"db/migrate/001_create_users.sql",
		"lib/admin/collections/outbound_transfer.rb",
		"server/domain/path_class.py",
		"cli/internal/mcpshim/proxy.go",
		"distribution/service.go", // "dist" must match a segment, not a prefix
	}
	for _, p := range notDerived {
		if isDerivedArtifact(p) {
			t.Errorf("isDerivedArtifact(%q) = true, want false", p)
		}
	}
}

// The staleness check compares by prefix, so the abbreviated blob_sha the agent
// rendering prints stays a usable anchor. Equality here would report every
// hydrated file stale — worse than not checking at all, since a stale marker on
// a current file trains the reader to ignore it.
func TestHydration_AbbreviatedBlobShaIsNotStale(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "app.py")
	if err := os.WriteFile(abs, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	full, err := currentBlobSHA(abs)
	if err != nil {
		t.Skipf("git hash-object unavailable: %v", err)
	}

	for _, tc := range []struct {
		name  string
		sha   string
		stale bool
	}{
		{"full sha", full, false},
		{"abbreviated to the rendering's width", full[:blobSHAAbbrev], false},
		{"a prefix of the wrong blob", strings.Repeat("0", blobSHAAbbrev), true},
		{"a full sha of the wrong blob", strings.Repeat("0", 40), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := hydrateSnippet(abs, 1, 1, defaultMaxSnippetLines, tc.sha)
			if err != nil {
				t.Fatalf("hydrateSnippet: %v", err)
			}
			if res.Stale != tc.stale {
				t.Errorf("Stale = %v, want %v (blob_sha %q vs %q)", res.Stale, tc.stale, tc.sha, full)
			}
		})
	}
}

// The cost line's token figure exists to compare tiers, so its divisor must
// follow the payload shape. bytes/4 — the prose default it used to carry —
// understated every payload this tool emits, and understated JSON worst, which
// flattered exactly the format the compaction ladder exists to move callers off.
func TestPayloadCost_TokenDivisorFollowsFormat(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{formatAgent, "~0.3k tokens"}, // 1000 B / 3.0
		{formatJSON, "~0.4k tokens"},  // 1000 B / 2.5
		{"", "~0.4k tokens"},          // unset defaults to JSON, the default format
	} {
		var log bytes.Buffer
		acct := payloadAccount{}
		acct.report(Config{Log: &log, Format: tc.format}, 1, 1000)
		if !strings.Contains(log.String(), tc.want) {
			t.Errorf("format %q: cost line %q, want %q", tc.format, strings.TrimSpace(log.String()), tc.want)
		}
	}
}

// The measured ratios the constants come from, pinned so a future edit has to
// argue with the measurement rather than drift back to a round number.
func TestPayloadCost_DivisorsMatchMeasuredRange(t *testing.T) {
	if bytesPerTokenJSON < 2.38 || bytesPerTokenJSON > 2.69 {
		t.Errorf("JSON divisor %.2f outside the measured 2.38–2.69", bytesPerTokenJSON)
	}
	if bytesPerTokenAgent < 2.48 || bytesPerTokenAgent > 3.48 {
		t.Errorf("agent divisor %.2f outside the measured 2.48–3.48", bytesPerTokenAgent)
	}
	if bytesPerTokenAgent <= bytesPerTokenJSON {
		t.Error("the rendering tokenises more efficiently than JSON; its divisor must be larger")
	}
}
