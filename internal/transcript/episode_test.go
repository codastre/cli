package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture's three turns: fallback (ordinal 0), failed (1), reads-only
// (2, unclassified — counted as a turn, not logged as an episode).
func TestEpisodeLogKeepsSearchTurnsWithOrdinals(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	log := state.Sessions["sess-1"].EpisodeLog
	if len(log) != 2 {
		t.Fatalf("episode log = %+v, want 2 search episodes", log)
	}
	if log[0].Ordinal != 0 || log[0].Outcome != OutcomeFallbackAfter {
		t.Errorf("episode 0 = %+v", log[0])
	}
	if log[1].Ordinal != 1 || log[1].Outcome != OutcomeCodastreFailed {
		t.Errorf("episode 1 = %+v", log[1])
	}
	e := log[0]
	if e.CodastreCalls != 1 || e.TextSearchCalls != 1 || e.CodastreBytes == 0 || e.TextSearchBytes == 0 {
		t.Errorf("episode 0 counters = %+v", e)
	}
	wantStart := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 9, 20, 10, 0, 4, 0, time.UTC)
	if !e.StartedAt.Equal(wantStart) || !e.EndedAt.Equal(wantEnd) {
		t.Errorf("episode 0 span = %s..%s, want %s..%s", e.StartedAt, e.EndedAt, wantStart, wantEnd)
	}
}

// Ordinals must not depend on how the transcript was sliced across runs, or
// the server's (session_ref, source, ordinal) key stops being idempotent.
func TestEpisodeOrdinalsStableAcrossIncrementalParses(t *testing.T) {
	lines := fixtureLines()
	root, path := writeTranscript(t, lines[:6]) // mid-turn-2
	state := LoadState("")
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}
	oneShot, _ := collectFixture(t, lines)
	got, want := state.Sessions["sess-1"].EpisodeLog, oneShot.Sessions["sess-1"].EpisodeLog
	if mustJSON(got) != mustJSON(want) {
		t.Errorf("incremental episode log differs:\n got %s\nwant %s", mustJSON(got), mustJSON(want))
	}
}

func writeHookLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session-events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func compactLine(session, phase, trigger string) string {
	return mustJSON(map[string]any{
		"ts": "2026-09-20T10:00:00.000Z", "session_id": session,
		"event": "compact", "phase": phase, "trigger": trigger,
	})
}

func TestHookEventsCountPreCompactionsOnly(t *testing.T) {
	path := writeHookLog(t,
		compactLine("sess-1", "pre", "auto"),
		compactLine("sess-1", "post", "auto"), // never double-counted
		compactLine("sess-1", "pre", "manual"),
		compactLine("sess-1", "pre", "unknown"), // dropped, not guessed
		compactLine("sess-2", "pre", "auto"),
		`{"event":"tool_failure","session_id":"sess-1","tool":"Grep","class":"text-search","error_type":"timeout"}`,
		`{"event":"compact","phase":"pre"`, // malformed; tolerated
	)
	state := LoadState("")
	res, err := CollectHookEvents(path, state)
	if err != nil {
		t.Fatal(err)
	}
	if res.Compactions != 3 {
		t.Errorf("compactions = %d, want 3", res.Compactions)
	}
	if c := state.Compactions["sess-1"]; c == nil || c.Auto != 1 || c.Manual != 1 {
		t.Errorf("sess-1 compactions = %+v", c)
	}
	if c := state.Compactions["sess-2"]; c == nil || c.Auto != 1 {
		t.Errorf("sess-2 compactions = %+v", c)
	}

	// A re-run with nothing appended reads nothing and counts nothing.
	again, err := CollectHookEvents(path, state)
	if err != nil {
		t.Fatal(err)
	}
	if again.BytesRead != 0 || again.Compactions != 0 {
		t.Errorf("no-op run = %+v", again)
	}

	// Appended events are picked up from the watermark.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(compactLine("sess-1", "pre", "auto") + "\n" + `{"partial":`)
	f.Close()
	if _, err := CollectHookEvents(path, state); err != nil {
		t.Fatal(err)
	}
	if c := state.Compactions["sess-1"]; c.Auto != 2 {
		t.Errorf("after append auto = %d, want 2", c.Auto)
	}
}

// Rotation renames the log to .1; the tail written before the rename must
// still be counted, exactly once.
func TestHookEventsSurviveRotation(t *testing.T) {
	path := writeHookLog(t, compactLine("s", "pre", "auto"))
	state := LoadState("")
	if _, err := CollectHookEvents(path, state); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(compactLine("s", "pre", "manual") + "\n")
	f.Close()
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(compactLine("s", "pre", "auto")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectHookEvents(path, state); err != nil {
		t.Fatal(err)
	}
	if c := state.Compactions["s"]; c.Auto != 2 || c.Manual != 1 {
		t.Errorf("after rotation = %+v, want auto 2 manual 1", c)
	}
}

func TestHookEventsMissingLogIsNotAnError(t *testing.T) {
	state := LoadState("")
	if _, err := CollectHookEvents(filepath.Join(t.TempDir(), "absent.jsonl"), state); err != nil {
		t.Errorf("missing log: %v", err)
	}
}
