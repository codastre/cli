package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fixture transcript with a distinctive string in every content-bearing
// position: the prompt, the tool input, and the tool result. Nothing derived
// from it may ever carry those strings.
const (
	secretPrompt = "SECRET_PROMPT_TEXT"
	secretQuery  = "SECRET_QUERY_TEXT"
	secretResult = "SECRET_RESULT_TEXT"
)

func fixtureLines() []string {
	return []string{
		// Turn 1 — a codastre CLI call, then a grep: fallback_after_codastre.
		userPrompt("2026-09-20T10:00:00.000Z", secretPrompt),
		assistantToolUse("2026-09-20T10:00:01.000Z", "t1", "Bash",
			`codastre query "`+secretQuery+`" --top-k 6`, 100, 20, 5, 1000, 200),
		toolResult("2026-09-20T10:00:02.000Z", "t1", secretResult+" aaaaaaaaaa", false),
		assistantToolUse("2026-09-20T10:00:03.000Z", "t2", "Grep", "", 10, 5, 0, 500, 0),
		toolResult("2026-09-20T10:00:04.000Z", "t2", "matchmatch", false),

		// Turn 2 — a failed codastre MCP call: codastre_failed.
		userPrompt("2026-09-20T10:01:00.000Z", secretPrompt+" again"),
		assistantToolUse("2026-09-20T10:01:01.000Z", "t3", "mcp__codastre__QUERY", "", 10, 5, 0, 100, 0),
		toolResult("2026-09-20T10:01:02.000Z", "t3", "RETRIEVAL_UNAVAILABLE", true),

		// Turn 3 — reads only: no search ran, so it is unclassified.
		userPrompt("2026-09-20T10:02:00.000Z", secretPrompt+" more"),
		assistantToolUse("2026-09-20T10:02:01.000Z", "t4", "Read", "", 10, 5, 0, 100, 0),
		toolResult("2026-09-20T10:02:02.000Z", "t4", strings.Repeat("x", 50), false),

		// The session wraps up, which closes turn 3.
		costState(),
	}
}

func userPrompt(ts, text string) string {
	return mustJSON(map[string]any{
		"type": "user", "sessionId": "sess-1", "timestamp": ts,
		"cwd": "/w/repo", "gitBranch": "main", "version": "2.1.0",
		"message": map[string]any{"role": "user", "content": text},
	})
}

func assistantToolUse(ts, id, name, command string, in, out, think, cacheRead, cacheCreate int) string {
	return mustJSON(map[string]any{
		"type": "assistant", "sessionId": "sess-1", "timestamp": ts, "cwd": "/w/repo",
		"message": map[string]any{
			"role": "assistant",
			"usage": map[string]any{
				"input_tokens": in, "output_tokens": out,
				"output_tokens_details":       map[string]any{"thinking_tokens": think},
				"cache_read_input_tokens":     cacheRead,
				"cache_creation_input_tokens": cacheCreate,
			},
			"content": []any{map[string]any{
				"type": "tool_use", "id": id, "name": name,
				"input": map[string]any{"command": command},
			}},
		},
	})
}

func toolResult(ts, id, content string, isErr bool) string {
	return mustJSON(map[string]any{
		"type": "user", "sessionId": "sess-1", "timestamp": ts, "cwd": "/w/repo",
		"message": map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": id, "content": content, "is_error": isErr,
		}}},
		"toolUseResult": map[string]any{"stdout": content},
	})
}

func costState() string {
	return mustJSON(map[string]any{
		"type": "cost-state", "sessionId": "sess-1",
		"totalCostUSD": 1.25, "totalDuration": 60000, "totalToolDuration": 9000,
		"totalLinesAdded": 3, "totalLinesRemoved": 1,
		"modelUsage": map[string]any{"claude-opus-5[1m]": map[string]any{
			"inputTokens": 120, "outputTokens": 30, "thinkingTokens": 5,
			"cacheReadInputTokens": 1600, "cacheCreationInputTokens": 200,
		}},
	})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func writeTranscript(t *testing.T, lines []string) (root, path string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "-w-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "sess-1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func collectFixture(t *testing.T, lines []string) (*State, string) {
	t.Helper()
	root, _ := writeTranscript(t, lines)
	state := LoadState("")
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return state, root
}

func TestParseCountsSessionAndEpisodes(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	sess := state.Sessions["sess-1"]
	if sess == nil {
		t.Fatalf("no session collected: %+v", state.Sessions)
	}
	if sess.Turns != 3 {
		t.Errorf("turns = %d, want 3", sess.Turns)
	}
	if !sess.Cost.Present || sess.Cost.CostUSD != 1.25 {
		t.Errorf("cost = %+v", sess.Cost)
	}
	if sess.Cost.CacheReadTokens != 1600 || sess.Cost.ThinkingTokens != 5 {
		t.Errorf("cost tokens = %+v", sess.Cost)
	}
	// Per-message usage is summed independently of the snapshot.
	if sess.Messages.Messages != 4 || sess.Messages.OutputTokens != 35 {
		t.Errorf("messages = %+v", sess.Messages)
	}
	want := map[string]int{
		OutcomeFallbackAfter:  1,
		OutcomeCodastreFailed: 1,
		OutcomeUnclassified:   1,
	}
	for outcome, n := range want {
		got := 0
		if st := sess.Episodes[outcome]; st != nil {
			got = st.Episodes
		}
		if got != n {
			t.Errorf("episodes[%s] = %d, want %d", outcome, got, n)
		}
	}
	if st := sess.ClassMix[ClassCodastre]; st == nil || st.Calls != 2 || st.Errors != 1 {
		t.Errorf("codastre class = %+v", st)
	}
	if st := sess.ToolMix["Bash"]; st == nil || st.Calls != 1 {
		t.Errorf("Bash tool = %+v", st)
	}
	if sess.GitBranch != "main" || sess.ClientVersion != "2.1.0" {
		t.Errorf("session metadata = %+v", sess)
	}
}

// The parse is a counter, not an archive. Nothing it writes may carry a
// prompt, a query, a path or tool output — which is the invariant that lets
// it run over transcripts at all.
func TestCollectedStateHoldsNoContent(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	blob, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarise(state.SessionsSince(time.Time{}), "all", "/tmp/state", time.Time{})
	sblob, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretPrompt, secretQuery, secretResult} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("collected state leaked %q", secret)
		}
		if strings.Contains(string(sblob), secret) {
			t.Errorf("summary leaked %q", secret)
		}
	}
}

// A transcript grows while a session runs. Collecting twice must produce the
// same numbers as collecting once at the end — no double counting, and no
// half-parsed turn booked as a whole one.
func TestCollectIsIncrementalAndIdempotent(t *testing.T) {
	lines := fixtureLines()
	root, path := writeTranscript(t, lines[:6]) // mid-turn-2
	state := LoadState("")
	first, err := Collect(root, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.FilesParsed != 1 {
		t.Fatalf("first run parsed %d files", first.FilesParsed)
	}

	// Re-running with nothing appended must read nothing and change nothing.
	before := mustJSON(state)
	again, err := Collect(root, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.FilesParsed != 0 || again.BytesRead != 0 {
		t.Errorf("no-op run parsed %d files / %d bytes", again.FilesParsed, again.BytesRead)
	}
	if mustJSON(state) != before {
		t.Errorf("no-op run mutated the state")
	}

	// Append the rest and collect again.
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}

	oneShot := LoadState("")
	rootFull, _ := writeTranscript(t, lines)
	if _, err := Collect(rootFull, oneShot, 0); err != nil {
		t.Fatal(err)
	}
	got, want := state.Sessions["sess-1"], oneShot.Sessions["sess-1"]
	if got.Turns != want.Turns {
		t.Errorf("turns incremental=%d one-shot=%d", got.Turns, want.Turns)
	}
	for _, class := range []string{ClassCodastre, ClassTextSearch, ClassRead} {
		g, w := got.ClassMix[class], want.ClassMix[class]
		if (g == nil) != (w == nil) {
			t.Fatalf("class %s presence differs", class)
		}
		if g != nil && (g.Calls != w.Calls || g.ResultBytes != w.ResultBytes) {
			t.Errorf("class %s incremental=%+v one-shot=%+v", class, g, w)
		}
	}
	if got.Messages != want.Messages {
		t.Errorf("messages incremental=%+v one-shot=%+v", got.Messages, want.Messages)
	}
	for outcome := range want.Episodes {
		if got.Episodes[outcome] == nil || got.Episodes[outcome].Episodes != want.Episodes[outcome].Episodes {
			t.Errorf("episodes[%s] incremental=%v one-shot=%v", outcome, got.Episodes[outcome], want.Episodes[outcome])
		}
	}
}

// A transcript truncated or replaced under the watermark must be re-read from
// zero rather than resumed at a meaningless offset.
func TestCollectResetsOnTruncation(t *testing.T) {
	lines := fixtureLines()
	root, path := writeTranscript(t, lines)
	state := LoadState("")
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}
	full := state.Sessions["sess-1"].Turns

	if err := os.WriteFile(path, []byte(strings.Join(lines[:6], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}
	if got := state.Sessions["sess-1"].Turns; got >= full {
		t.Errorf("turns after truncation = %d, want fewer than %d", got, full)
	}
}

func TestParseToleratesTruncatedFinalLine(t *testing.T) {
	lines := append(fixtureLines(), `{"type":"assistant","sessionId":"sess-1","mess`)
	state, _ := collectFixture(t, lines)
	if state.Sessions["sess-1"].Turns != 3 {
		t.Errorf("turns = %d, want 3", state.Sessions["sess-1"].Turns)
	}
}
