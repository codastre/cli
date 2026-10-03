package transcript

import (
	"strings"
	"testing"
)

func TestExitsOneOnNoMatchJudgesTheStatusSettingCommand(t *testing.T) {
	cases := map[string]bool{
		`grep -rn foo src`:                         true,
		`rg --json foo`:                            true,
		`ag foo; ack bar`:                          true,
		`git grep -n foo`:                          true,
		`LC_ALL=C grep foo f`:                      true,
		`/usr/bin/grep foo f`:                      true,
		`grep -E "a|b" f`:                          true, // the | is quoted
		`grep -E 'a;b' f`:                          true,
		`cd x && rg foo`:                           true,
		`grep foo f | head -5`:                     false, // head sets the status
		`find . -name x`:                           false, // find: exit 1 is an error
		`fd foo`:                                   false,
		`find . | xargs grep foo`:                  false, // xargs remaps the status
		`grep foo f || echo none`:                  false,
		`codastre query "x" --format agent`:        false,
		`codastre query "x" --format agent | rg y`: true,
	}
	for cmd, want := range cases {
		if got := exitsOneOnNoMatch(cmd); got != want {
			t.Errorf("exitsOneOnNoMatch(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestIsExitOneMatchesOnlyStatusOne(t *testing.T) {
	cases := map[string]bool{
		`"Exit code 1"`:                          true,
		`"Exit code 1\nmore"`:                    true,
		`[{"type":"text","text":"Exit code 1"}]`: true,
		`"Exit code 127\nnope"`:                  false,
		`"Exit code 2"`:                          false,
		`"ok, then Exit code 1 later in a long payload that is not the banner"`: false,
	}
	for content, want := range cases {
		if got := isExitOne([]byte(content)); got != want {
			t.Errorf("isExitOne(%s) = %v, want %v", content, got, want)
		}
	}
}

// Three exit-1 results: a search that matched nothing, a find that failed,
// and a zsh-aborted line. Only the first is not an error; only the last is a
// shell error.
func TestBashExitsAreSplitIntoNoMatchErrorsAndShellErrors(t *testing.T) {
	lines := []string{
		userPrompt("2026-09-20T10:00:00.000Z", "q"),
		assistantToolUse("2026-09-20T10:00:01.000Z", "b1", "Bash", `rg -n "a|b" src`, 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:00:02.000Z", "b1", "Exit code 1", true),
		assistantToolUse("2026-09-20T10:00:03.000Z", "b2", "Bash", `find /nope -name x`, 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:00:04.000Z", "b2", "Exit code 1\nfind: /nope: No such file", true),
		assistantToolUse("2026-09-20T10:00:05.000Z", "b3", "Bash", `grep -rn x src | head; echo ==`, 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:00:06.000Z", "b3", "Exit code 1\nsrc/a\n(eval):1: = not found", true),
		assistantToolUse("2026-09-20T10:00:07.000Z", "b4", "Bash", `git status`, 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:00:08.000Z", "b4", "clean", false),
		costState(),
	}
	sess := parseLines(t, lines)

	bash := sess.ToolMix["Bash"]
	if bash.Calls != 4 || bash.Errors != 2 || bash.NoMatch != 1 || bash.ShellErrors != 1 {
		t.Errorf("Bash = %+v, want 4 calls, 2 errors (1 shell), 1 no-match", *bash)
	}
	search := sess.BashByClass[ClassTextSearch]
	if search == nil || search.Calls != 3 || search.NoMatch != 1 || search.Errors != 2 {
		t.Errorf("Bash/text-search = %+v", search)
	}
	if other := sess.BashByClass[ClassOther]; other == nil || other.Calls != 1 || other.Errors != 0 {
		t.Errorf("Bash/other = %+v", other)
	}
}

// A codastre CLI pipeline whose trailing grep matched nothing did not fail.
func TestCodastrePipelineNoMatchIsNotACodastreFailure(t *testing.T) {
	lines := []string{
		userPrompt("2026-09-20T10:00:00.000Z", "q"),
		assistantToolUse("2026-09-20T10:00:01.000Z", "c1", "Bash", `codastre query "x" | grep -c y`, 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:00:02.000Z", "c1", "Exit code 1", true),
		costState(),
	}
	sess := parseLines(t, lines)
	if len(sess.EpisodeLog) != 1 || sess.EpisodeLog[0].Outcome != OutcomeCodastreOnly {
		t.Errorf("episodes = %+v, want one codastre_only", sess.EpisodeLog)
	}
}

func TestEpisodeCarriesTheAnnouncedSearchMode(t *testing.T) {
	lines := []string{
		userPrompt("2026-09-20T10:00:00.000Z", "q"),
		hookContext("2026-09-20T10:00:00.100Z", "CODASTRE-FIRST (auto) search mode is ON. For any code search"),
		assistantToolUse("2026-09-20T10:00:01.000Z", "c1", "mcp__codastre__QUERY", "", 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:00:02.000Z", "c1", "hits", false),
		userPrompt("2026-09-20T10:01:00.000Z", "q2"),
		assistantToolUse("2026-09-20T10:01:01.000Z", "g1", "Grep", "", 1, 1, 0, 0, 0),
		toolResult("2026-09-20T10:01:02.000Z", "g1", "m", false),
		costState(),
	}
	sess := parseLines(t, lines)
	if len(sess.EpisodeLog) != 2 {
		t.Fatalf("episodes = %+v", sess.EpisodeLog)
	}
	if sess.EpisodeLog[0].Mode != ModeAuto || sess.EpisodeLog[1].Mode != ModeUnknown {
		t.Errorf("modes = %q, %q; want auto, unknown", sess.EpisodeLog[0].Mode, sess.EpisodeLog[1].Mode)
	}
}

func hookContext(ts, text string) string {
	return mustJSON(map[string]any{
		"type": "attachment", "sessionId": "sess-1", "timestamp": ts,
		"attachment": map[string]any{
			"type": "hook_additional_context", "hookEvent": "UserPromptSubmit",
			"content": []string{text},
		},
	})
}

func parseLines(t *testing.T, lines []string) *Session {
	t.Helper()
	sess, _, err := Parse(strings.NewReader(strings.Join(lines, "\n")+"\n"), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return sess
}
