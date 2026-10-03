package transcript

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The table is shared verbatim with the plugin hook's test suite
// (codastre-claude adapters/claude/hooks/test/bash-classify-fixtures.json):
// the hook enforces the search mode and the collector counts what ran, so
// they must agree on every row.
func TestClassifyAgreesWithThePluginFixtures(t *testing.T) {
	blob, err := os.ReadFile("testdata/bash-classify-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Command string `json:"command"`
			Class   string `json:"class"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(blob, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("empty fixture table")
	}
	for _, c := range table.Cases {
		if got, _ := Classify("Bash", c.Command); got != c.Class {
			t.Errorf("Classify(%q) = %s, want %s", c.Command, got, c.Class)
		}
	}
}

func TestMaskQuotedBlanksArgumentsAndKeepsWhatTheShellRuns(t *testing.T) {
	cases := map[string]string{
		`git commit -m "a (grep)"`: `git commit -m "` + strings.Repeat(" ", 8) + `"`,
		`echo '$(rg x)'`:           `echo '` + strings.Repeat(" ", 7) + `'`,
		`echo "$(rg x)"`:           `echo "$(rg x)"`,
		`bash -c 'rg x'`:           `bash -c ;rg x;`,
		"cd a\nrg x":               "cd a;rg x",
	}
	for in, want := range cases {
		if got := maskQuoted(in); got != want {
			t.Errorf("maskQuoted(%q) = %q, want %q", in, got, want)
		}
	}
}

// A heredoc commit message ending in a grep-looking line is not a search
// whose exit 1 means "no match".
func TestNoMatchJudgementIgnoresHeredocBodies(t *testing.T) {
	if exitsOneOnNoMatch("git commit -F - <<'EOF'\nfeat: x\n| grep y\nEOF") {
		t.Error("heredoc body judged as the status-setting command")
	}
	if !exitsOneOnNoMatch(`bash -c 'grep x f'`) {
		t.Error("bash -c string should be judged by the commands it runs")
	}
}
