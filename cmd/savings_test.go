package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const savingsFixture = `{"ts":"2026-09-20T10:00:00.000Z","session_id":"s1","cwd":"/w/a","tool":"mcp__codastre__QUERY","class":"codastre","plane":"mcp","detail":"secret query text","out_tokens":1000,"tok_basis":"json"}
{"ts":"2026-09-20T10:01:00.000Z","session_id":"s1","cwd":"/w/a","tool":"Grep","class":"text-search","detail":"secret pattern","out_tokens":400,"tok_basis":"text"}
`

// execSavings runs `codastre savings …` through the root command, which is
// where cobra parses flags, and restores the package-level flag vars after.
func execSavings(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if os.Getenv("CODASTRE_COLLECT_STATE") == "" {
		t.Setenv("CODASTRE_COLLECT_STATE", filepath.Join(t.TempDir(), "empty-state.json"))
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		savingsWindow, savingsJSON, savingsLogPath, savingsSource = "30d", false, "", "auto"
	})
	rootCmd.SetArgs(append([]string{"savings"}, args...))
	err := rootCmd.Execute()
	return out.String(), err
}

// runSavingsCmd exercises the JSONL-log path. It points the collector at an
// empty state file so the developer's real collection cannot leak into a test.
func runSavingsCmd(t *testing.T, args ...string) string {
	t.Helper()
	t.Setenv("CODASTRE_COLLECT_STATE", filepath.Join(t.TempDir(), "empty-state.json"))
	out, err := execSavings(t, args...)
	if err != nil {
		t.Fatalf("savings %v: %v", args, err)
	}
	return out
}

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(savingsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavingsHumanOutput(t *testing.T) {
	got := runSavingsCmd(t, "--log", writeFixture(t), "--window", "all")
	for _, want := range []string{"codastre savings — local, all window", "TOTAL", "~1,400 tok"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestSavingsJSONOutput(t *testing.T) {
	got := runSavingsCmd(t, "--log", writeFixture(t), "--window", "all", "--json")
	var summary struct {
		Window     string `json:"window"`
		TotalCalls int    `json:"total_calls"`
		TotalToken int    `json:"total_tokens"`
		Groups     []struct {
			Key string `json:"key"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(got), &summary); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if summary.Window != "all" || summary.TotalCalls != 2 || summary.TotalToken != 1400 {
		t.Errorf("summary = %+v", summary)
	}
	if len(summary.Groups) != 2 {
		t.Errorf("groups = %d, want 2", len(summary.Groups))
	}
}

// The log holds query strings and file paths. The summary is counters, and
// `--json` is the shape a developer is most likely to paste somewhere.
func TestSavingsNeverEchoesLogDetail(t *testing.T) {
	path := writeFixture(t)
	for _, out := range []string{
		runSavingsCmd(t, "--log", path, "--window", "all"),
		runSavingsCmd(t, "--log", path, "--window", "all", "--json"),
	} {
		for _, leaked := range []string{"secret query text", "secret pattern"} {
			if strings.Contains(out, leaked) {
				t.Errorf("output leaked log detail %q:\n%s", leaked, out)
			}
		}
	}
}

func TestSavingsMissingLogIsNotAnError(t *testing.T) {
	got := runSavingsCmd(t, "--log", filepath.Join(t.TempDir(), "absent.jsonl"))
	if !strings.Contains(got, "No token log at") {
		t.Errorf("unexpected output:\n%s", got)
	}
}

func TestSavingsRejectsBadWindow(t *testing.T) {
	if _, err := execSavings(t, "--window", "7w", "--log", writeFixture(t)); err == nil {
		t.Fatal("expected an error for --window 7w")
	}
}

func TestUsageTrackingDetailNamesBothFlags(t *testing.T) {
	t.Setenv("CODASTRE_TRACK_TOKENS", "")
	t.Setenv("CODASTRE_USAGE_REPORT", "")
	off := usageTrackingDetail()
	if !strings.Contains(off, "local log off") || !strings.Contains(off, "nothing leaves this machine") {
		t.Errorf("off detail = %q", off)
	}

	t.Setenv("CODASTRE_TRACK_TOKENS", "1")
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	on := usageTrackingDetail()
	if !strings.Contains(on, "local log on") || !strings.Contains(on, "upload on") {
		t.Errorf("on detail = %q", on)
	}
}

// A minimal transcript: one turn, one codastre call, no fallback.
const transcriptFixture = `{"type":"user","sessionId":"sess-x","timestamp":"2026-09-20T10:00:00.000Z","cwd":"/w/repo","message":{"role":"user","content":"SECRET_PROMPT"}}
{"type":"assistant","sessionId":"sess-x","timestamp":"2026-09-20T10:00:01.000Z","message":{"role":"assistant","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":900,"cache_creation_input_tokens":100},"content":[{"type":"tool_use","id":"t1","name":"mcp__codastre__QUERY","input":{}}]}}
{"type":"user","sessionId":"sess-x","timestamp":"2026-09-20T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"SECRET_RESULT","is_error":false}]}}
{"type":"cost-state","sessionId":"sess-x","totalCostUSD":0.5,"totalDuration":1000,"modelUsage":{"m":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":900,"cacheCreationInputTokens":100}}}
`

// stageTranscript points the collector at a temp projects dir and state file.
func stageTranscript(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "-w-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sess-x.jsonl"), []byte(transcriptFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECTS_DIR", root)
	t.Setenv("CODASTRE_COLLECT_STATE", filepath.Join(t.TempDir(), "collect-state.json"))
	t.Setenv("CODASTRE_SESSION_EVENTS_LOG", filepath.Join(t.TempDir(), "session-events.jsonl"))
	return root
}

func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		savingsWindow, savingsJSON, savingsLogPath, savingsSource = "30d", false, "", "auto"
		collectLimit, collectJSON, collectRoot, collectReset = 0, false, "", false
		collectUpload, collectServerURL, collectKey = false, defaultServerURL(), ""
		collectBackfill, collectYes = false, false
	})
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

func TestCollectThenSavingsPrefersTranscript(t *testing.T) {
	stageTranscript(t)

	out, err := execRoot(t, "collect")
	if err != nil {
		t.Fatalf("collect: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 transcripts seen, 1 parsed") {
		t.Errorf("collect output:\n%s", out)
	}
	if !strings.Contains(out, "nothing uploaded") {
		t.Errorf("collect should state that nothing is uploaded:\n%s", out)
	}

	// --source auto must pick the exact source now that a collection exists,
	// even though a token log is also configured.
	t.Setenv("CODASTRE_TOKEN_LOG", writeFixture(t))
	got, err := execRoot(t, "savings", "--window", "all")
	if err != nil {
		t.Fatalf("savings: %v", err)
	}
	if !strings.Contains(got, "source: Claude Code transcripts") {
		t.Errorf("savings did not prefer the transcript:\n%s", got)
	}
	if !strings.Contains(got, "answered without fallback: 1 / 1") {
		t.Errorf("missing the rate with its denominator:\n%s", got)
	}
	for _, leaked := range []string{"SECRET_PROMPT", "SECRET_RESULT"} {
		if strings.Contains(got, leaked) {
			t.Errorf("savings leaked %q", leaked)
		}
	}
}

// The two sources are different worlds — one exact, one estimated — so
// --source log must still reach the log even when a collection exists.
func TestSavingsSourceLogOverridesTranscript(t *testing.T) {
	stageTranscript(t)
	if _, err := execRoot(t, "collect"); err != nil {
		t.Fatal(err)
	}
	got, err := execRoot(t, "savings", "--window", "all", "--source", "log", "--log", writeFixture(t))
	if err != nil {
		t.Fatalf("savings: %v", err)
	}
	if strings.Contains(got, "Claude Code transcripts") {
		t.Errorf("--source log fell through to the transcript:\n%s", got)
	}
	if !strings.Contains(got, "TOTAL") {
		t.Errorf("log receipt missing:\n%s", got)
	}
}

func TestSavingsSourceTranscriptWithNothingCollected(t *testing.T) {
	t.Setenv("CODASTRE_COLLECT_STATE", filepath.Join(t.TempDir(), "state.json"))
	got, err := execRoot(t, "savings", "--source", "transcript")
	if err != nil {
		t.Fatalf("savings: %v", err)
	}
	if !strings.Contains(got, "Run `codastre collect`") {
		t.Errorf("expected a pointer to collect:\n%s", got)
	}
}

func TestSavingsRejectsBadSource(t *testing.T) {
	if _, err := execRoot(t, "savings", "--source", "otel"); err == nil {
		t.Fatal("expected an error for an unknown --source")
	}
}

func TestCollectJSONIsCountersOnly(t *testing.T) {
	stageTranscript(t)
	got, err := execRoot(t, "collect", "--json")
	if err != nil {
		t.Fatalf("collect --json: %v", err)
	}
	var res struct {
		FilesSeen   int   `json:"files_seen"`
		FilesParsed int   `json:"files_parsed"`
		BytesRead   int64 `json:"bytes_read"`
		Sessions    int   `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if res.FilesSeen != 1 || res.FilesParsed != 1 || res.Sessions != 1 || res.BytesRead == 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestOtelContentVarsFlagsOnlyEnabledOnes(t *testing.T) {
	t.Setenv("OTEL_LOG_USER_PROMPTS", "")
	t.Setenv("OTEL_LOG_TOOL_CONTENT", "false")
	t.Setenv("OTEL_LOG_RAW_API_BODIES", "0")
	if got := otelContentVars(); len(got) != 0 {
		t.Errorf("unset/false vars flagged: %v", got)
	}
	t.Setenv("OTEL_LOG_ASSISTANT_RESPONSES", "1")
	got := otelContentVars()
	if len(got) != 1 || got[0] != "OTEL_LOG_ASSISTANT_RESPONSES" {
		t.Errorf("got %v", got)
	}
}

func TestUsageTrackingDetailNamesCollection(t *testing.T) {
	t.Setenv("CODASTRE_COLLECT_STATE", filepath.Join(t.TempDir(), "state.json"))
	if got := usageTrackingDetail(); !strings.Contains(got, "no transcripts collected") {
		t.Errorf("detail = %q", got)
	}
	stageTranscript(t)
	if _, err := execRoot(t, "collect"); err != nil {
		t.Fatal(err)
	}
	if got := usageTrackingDetail(); !strings.Contains(got, "1 session collected") {
		t.Errorf("detail = %q", got)
	}
}
