package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSummariseRateAndAmplification(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	s := Summarise(state.SessionsSince(time.Time{}), "all", "/tmp/state", time.Time{})
	if s.Sessions != 1 || s.Turns != 3 {
		t.Errorf("summary = %+v", s)
	}
	// One fallback + one failure, no clean win.
	if s.EpisodesWithCodastre != 2 || s.EpisodesCodastreOnly != 0 {
		t.Errorf("codastre episodes = %d/%d", s.EpisodesCodastreOnly, s.EpisodesWithCodastre)
	}
	if s.AnsweredWithoutFallback != 0 {
		t.Errorf("rate = %v, want 0", s.AnsweredWithoutFallback)
	}
	if s.EpisodesUnclassified != 1 {
		t.Errorf("unclassified = %d, want 1", s.EpisodesUnclassified)
	}
	// 1600 / (1600 + 200 + 120)
	if got := s.ContextAmplification; got < 0.83 || got > 0.84 {
		t.Errorf("amplification = %v, want ~0.833", got)
	}
	if s.CostUSD != 1.25 {
		t.Errorf("cost = %v", s.CostUSD)
	}
}

func TestSummaryPublishesNoCounterfactual(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	blob, err := json.Marshal(Summarise(state.SessionsSince(time.Time{}), "all", "/s", time.Time{}))
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

func TestStateRoundTrip(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	path := filepath.Join(t.TempDir(), "state.json")
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state mode = %v, want 0600", info.Mode().Perm())
	}
	back := LoadState(path)
	if len(back.Sessions) != 1 || back.Sessions["sess-1"].Turns != 3 {
		t.Errorf("round trip lost data: %+v", back.Sessions)
	}
	// A future format must not be read as if it were this one.
	if err := os.WriteFile(path, []byte(`{"version":99,"sessions":{"x":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(LoadState(path).Sessions) != 0 {
		t.Error("a newer state version should be discarded, not parsed")
	}
}

func TestClassifyMatchesPluginVocabulary(t *testing.T) {
	cases := []struct {
		tool, cmd, class, plane string
	}{
		{"mcp__codastre__QUERY", "", ClassCodastre, PlaneMCP},
		{"mcp__plugin_codastre_codastre__GRAPH", "", ClassCodastre, PlaneMCP},
		{"Grep", "", ClassTextSearch, ""},
		{"Glob", "", ClassTextSearch, ""},
		{"Read", "", ClassRead, ""},
		// A codastre pipeline that ends in grep is a codastre call, not a grep.
		{"Bash", `codastre query "x" | grep foo`, ClassCodastre, PlaneCLI},
		{"Bash", "~/go/bin/codastre graph Foo", ClassCodastre, PlaneCLI},
		{"Bash", "rg --files-with-matches foo", ClassTextSearch, ""},
		{"Bash", "git grep -n foo", ClassTextSearch, ""},
		{"Bash", "find . -name '*.go'", ClassTextSearch, ""},
		{"Bash", "go test ./...", ClassOther, ""},
	}
	for _, tc := range cases {
		class, plane := Classify(tc.tool, tc.cmd)
		if class != tc.class || plane != tc.plane {
			t.Errorf("Classify(%q, %q) = %s/%s, want %s/%s", tc.tool, tc.cmd, class, plane, tc.class, tc.plane)
		}
	}
}

func TestDiscoverNewestFirst(t *testing.T) {
	root, path := writeTranscript(t, fixtureLines())
	older := filepath.Join(filepath.Dir(path), "sess-0.jsonl")
	if err := os.WriteFile(older, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}
	files, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != path {
		t.Fatalf("discover order wrong: %+v", files)
	}
	if files[0].Project != "-w-repo" {
		t.Errorf("project = %q", files[0].Project)
	}
}

func TestDiscoverMissingRootIsNotAnError(t *testing.T) {
	files, err := Discover(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(files) != 0 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}

func TestRenderShowsDenominatorAndCaveat(t *testing.T) {
	state, _ := collectFixture(t, fixtureLines())
	var sb strings.Builder
	Render(&sb, Summarise(state.SessionsSince(time.Time{}), "all", "/s", time.Time{}))
	out := sb.String()
	for _, want := range []string{
		"re-read amplification",
		"Search episodes",
		"answered without fallback: 0 / 2",
		"excluded from both sides",
		"No counterfactual is computed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{secretPrompt, secretQuery, secretResult} {
		if strings.Contains(out, secret) {
			t.Errorf("render leaked %q", secret)
		}
	}
}
