package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleLog = `{"ts":"2026-09-20T10:00:00.000Z","session_id":"s1","cwd":"/w/a","tool":"mcp__codastre__QUERY","class":"codastre","plane":"mcp","detail":"q","out_tokens":1000,"tok_basis":"json","rung":"verbose"}
{"ts":"2026-09-20T10:01:00.000Z","session_id":"s1","cwd":"/w/a","tool":"Bash","class":"codastre","plane":"cli","detail":"codastre query x","out_tokens":200,"tok_basis":"agent","rung":"agent"}
{"ts":"2026-09-21T09:00:00.000Z","session_id":"s2","cwd":"/w/b","tool":"Grep","class":"text-search","detail":"foo","out_tokens":500,"tok_basis":"text"}
{"ts":"2026-09-21T09:05:00.000Z","session_id":"s2","cwd":"/w/b","tool":"Read","class":"read","detail":"/w/b/x.go","out_tokens":300,"tok_basis":"text"}
{"ts":"2026-09-21T09:06:00.000Z","session_id":"s2","cwd":"/w/b","tool":"Grep","class":"text-search","detail":"bar","out_tokens":100,"tok_basis":"text"}
`

func writeLog(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-token-log.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndAggregate(t *testing.T) {
	path := writeLog(t, sampleLog)
	records, err := Load(path, time.Time{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("records = %d, want 5", len(records))
	}

	s := Aggregate(records, "all", path, time.Time{})
	if s.TotalCalls != 5 || s.TotalToken != 2100 {
		t.Fatalf("totals = %d calls / %d tok, want 5 / 2100", s.TotalCalls, s.TotalToken)
	}
	want := map[string][2]int{
		GroupCodastreMCP: {1, 1000},
		GroupCodastreCLI: {1, 200},
		GroupTextSearch:  {2, 600},
		GroupRead:        {1, 300},
	}
	got := map[string][2]int{}
	for _, g := range s.Groups {
		got[g.Key] = [2]int{g.Calls, g.Tokens}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("group %s = %v, want %v", k, got[k], v)
		}
	}
	if s.Reliance.Sessions != 2 || s.Reliance.Workspaces != 2 || s.Reliance.Days != 2 {
		t.Errorf("reliance = %+v, want 2/2/2", s.Reliance)
	}
	if s.Reliance.CodastreCalls != 2 || s.Reliance.TextSearchCall != 2 {
		t.Errorf("call split = %d/%d, want 2/2", s.Reliance.CodastreCalls, s.Reliance.TextSearchCall)
	}
	// Groups are ordered by tokens, descending.
	if s.Groups[0].Key != GroupCodastreMCP {
		t.Errorf("first group = %s, want %s", s.Groups[0].Key, GroupCodastreMCP)
	}
	if !strings.Contains(s.Caveat, "±20%") || !strings.Contains(s.Caveat, "unmeasured") {
		t.Errorf("caveat lost its provenance: %q", s.Caveat)
	}
}

func TestLoadWindowFiltersByTimestamp(t *testing.T) {
	path := writeLog(t, sampleLog)
	since := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	records, err := Load(path, since)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
}

// A hook killed mid-append leaves a truncated final line. Losing the whole
// window over it would be worse than losing the line.
func TestLoadToleratesMalformedLines(t *testing.T) {
	path := writeLog(t, sampleLog+`{"ts":"2026-09-21T10:00:00.000Z","class":"codas`)
	records, err := Load(path, time.Time{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("records = %d, want 5 (truncated line dropped)", len(records))
	}
}

func TestLoadIncludesRotatedFile(t *testing.T) {
	path := writeLog(t, sampleLog)
	rotated := `{"ts":"2026-09-01T10:00:00.000Z","session_id":"s0","cwd":"/w/c","tool":"Grep","class":"text-search","detail":"old","out_tokens":50,"tok_basis":"text"}` + "\n"
	if err := os.WriteFile(path+".1", []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := Load(path, time.Time{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("records = %d, want 6 (rotated file included)", len(records))
	}
	if records[0].SessionID != "s0" {
		t.Errorf("rotated records should come first, got %q", records[0].SessionID)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"), time.Time{})
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

// A record written before the hook had a `plane` field must not be guessed
// into one of the two planes.
func TestGroupKeyOfLegacyRecord(t *testing.T) {
	if got := GroupKeyOf(Record{Class: "codastre"}); got != GroupCodastre {
		t.Errorf("legacy codastre record = %q, want %q", got, GroupCodastre)
	}
	if got := GroupKeyOf(Record{Class: ""}); got != GroupOther {
		t.Errorf("classless record = %q, want %q", got, GroupOther)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in    string
		label string
		since time.Time
	}{
		{"", "30d", now.AddDate(0, 0, -30)},
		{"7d", "7d", now.AddDate(0, 0, -7)},
		{"ALL", "all", time.Time{}},
		{"12h", "12h", now.Add(-12 * time.Hour)},
	} {
		since, label, err := ParseWindow(tc.in, now)
		if err != nil {
			t.Fatalf("ParseWindow(%q): %v", tc.in, err)
		}
		if label != tc.label || !since.Equal(tc.since) {
			t.Errorf("ParseWindow(%q) = %v/%s, want %v/%s", tc.in, since, label, tc.since, tc.label)
		}
	}
	for _, bad := range []string{"7", "d", "0d", "-3d", "7w"} {
		if _, _, err := ParseWindow(bad, now); err == nil {
			t.Errorf("ParseWindow(%q) accepted a bad window", bad)
		}
	}
}

// The one-sided metric this design exists to remove must not be able to creep
// back in through the JSON surface.
func TestSummaryPublishesNoCounterfactual(t *testing.T) {
	records, err := Load(writeLog(t, sampleLog), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(Aggregate(records, "all", "/tmp/log", time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(blob, &generic); err != nil {
		t.Fatal(err)
	}
	for key := range generic {
		for _, banned := range []string{"saved", "baseline", "avoided", "would_have"} {
			if strings.Contains(key, banned) {
				t.Errorf("summary field %q publishes a counterfactual", key)
			}
		}
	}
}

func TestRenderIncludesReceiptAndReach(t *testing.T) {
	records, err := Load(writeLog(t, sampleLog), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	Render(&buf, Aggregate(records, "all", "/tmp/log", time.Time{}))
	out := buf.String()
	for _, want := range []string{
		"Codastre (MCP QUERY/GRAPH)",
		"Codastre (CLI plane)",
		"Text search (grep/glob)",
		"TOTAL",
		"2 sessions",
		"search calls: 2 codastre vs 2 text search",
		"No counterfactual is computed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderEmptyWindow(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Aggregate(nil, "7d", "/tmp/log", time.Now()))
	if !strings.Contains(buf.String(), "No search or read tool calls logged") {
		t.Errorf("empty render: %s", buf.String())
	}
}

func TestReachLineDirection(t *testing.T) {
	if got := reachLine(Reliance{CodastreCalls: 10, TextSearchCall: 110}); !strings.Contains(got, "11.0:1 against the tool") {
		t.Errorf("got %q", got)
	}
	if got := reachLine(Reliance{CodastreCalls: 30, TextSearchCall: 10}); !strings.Contains(got, "3.0:1 toward the tool") {
		t.Errorf("got %q", got)
	}
	if got := reachLine(Reliance{TextSearchCall: 5}); !strings.Contains(got, "codastre unused") {
		t.Errorf("got %q", got)
	}
	if got := reachLine(Reliance{}); got != "" {
		t.Errorf("empty reliance should print nothing, got %q", got)
	}
}

func TestThousands(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q, want %q", in, got, want)
		}
	}
}
