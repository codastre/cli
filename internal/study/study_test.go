package study

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileRoundTripIs0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "study.json")
	f := FromAssignment(Assignment{AssignmentID: "a", Study: "s", Task: "t", Arm: "tool", Mode: "auto",
		ArmOrder: 2, Prompt: "p", PromptSHA256: strings.Repeat("b", 64)}, time.Now())
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AssignmentID != "a" || got.ArmOrder != 2 || got.SessionID != nil || got.ClaimedAt != nil {
		t.Errorf("round trip = %+v", got)
	}
	blob, _ := os.ReadFile(path)
	if !bytes.Contains(blob, []byte(`"session_id": null`)) {
		t.Errorf("unclaimed file must carry session_id null for the hook: %s", blob)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "none.json")); err != ErrNoFile {
		t.Errorf("missing file err = %v", err)
	}
}

func TestRenderViewPrintsHeadlineCountsWhenPublishable(t *testing.T) {
	tie := 1
	v := View{N: 12, TargetN: 12, Publishable: true, Headline: &Headline{Of: 12,
		Correct: Tally{Tool: 10, NoTool: 7}, Cheaper: Tally{Tool: 8, NoTool: 3, Tie: &tie}}}
	var buf bytes.Buffer
	RenderView(&buf, v)
	out := buf.String()
	for _, want := range []string{"Of 12 pairs", "correct   tool 10 · no_tool 7", "tie 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "%") {
		t.Errorf("headline must be counts, not a percentage:\n%s", out)
	}
}

func TestRenderBlindHasNoArm(t *testing.T) {
	var buf bytes.Buffer
	RenderBlind(&buf, Blind{Study: "s", Items: []BlindItem{{AssignmentID: "x", Task: "t", Acceptance: "passes", Bound: true}}})
	if strings.Contains(buf.String(), "no_tool") || strings.Contains(buf.String(), "arm ") {
		t.Errorf("blind queue leaks an arm:\n%s", buf.String())
	}
}
