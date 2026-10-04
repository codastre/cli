package transcript

import (
	"os"
	"strings"
	"testing"
)

// restartedLines is a session that was restarted: Claude Code wrote a
// cost-state on the first exit, then the session carried on.
func restartedLines() []string {
	all := fixtureLines()
	early := append([]string{}, all[:5]...) // turn 1 done
	early = append(early, costState())
	return append(early, all[5:len(all)-1]...) // turns 2–3, no final record
}

func TestCostStateFollowedByRequestsIsStale(t *testing.T) {
	state, _ := collectFixture(t, restartedLines())
	if c := state.Sessions["sess-1"].Cost; !c.Present || !c.Stale {
		t.Fatalf("restarted session cost = %+v, want present and stale", c)
	}

	fresh, _ := collectFixture(t, fixtureLines())
	if c := fresh.Sessions["sess-1"].Cost; !c.Present || c.Stale {
		t.Fatalf("finished session cost = %+v, want present and not stale", c)
	}
}

// Staleness must survive an incremental run that brings requests but no new
// record, and clear once the final record arrives.
func TestCostStateStalenessAcrossIncrementalRuns(t *testing.T) {
	lines := restartedLines()
	root, path := writeTranscript(t, lines[:6]) // up to and including the early record
	state := LoadState("")
	collect := func() {
		t.Helper()
		if _, err := Collect(root, state, 0); err != nil {
			t.Fatal(err)
		}
	}
	write := func(ls []string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(ls, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	collect()
	if c := state.Sessions["sess-1"].Cost; c.Stale {
		t.Fatalf("snapshot with nothing after it is stale: %+v", c)
	}

	write(append(lines, userPrompt("2026-09-20T10:03:00.000Z", "next")))
	collect()
	if c := state.Sessions["sess-1"].Cost; !c.Stale {
		t.Fatalf("requests after the snapshot did not mark it stale: %+v", c)
	}

	write(append(lines, costState()))
	collect()
	if c := state.Sessions["sess-1"].Cost; c.Stale {
		t.Fatalf("final record did not clear staleness: %+v", c)
	}
}
