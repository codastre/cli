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
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		savingsWindow, savingsJSON, savingsLogPath = "30d", false, ""
	})
	rootCmd.SetArgs(append([]string{"savings"}, args...))
	err := rootCmd.Execute()
	return out.String(), err
}

func runSavingsCmd(t *testing.T, args ...string) string {
	t.Helper()
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
