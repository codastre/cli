package transcript

import (
	"bytes"
	"path/filepath"
	"strings"
)

// Claude Code flags every non-zero Bash exit as `is_error`, so a search that
// simply found nothing arrives looking like a failure. The search tools below
// use exit status 1 for exactly that — "ran fine, no match" — and ≥2 for real
// errors. find and fd are deliberately absent: they exit 0 on no match and 1
// on a genuine error (a bad path, a permission denial), so their exit 1 stays
// an error.
var noMatchExiters = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "zgrep": true,
	"rg": true, "ag": true, "ack": true, "pgrep": true,
}

// exitsOneOnNoMatch reports whether the command whose status the shell
// returns — the last one in the last list/pipeline element, absent pipefail —
// is a search tool for which exit 1 means "no match". It is a judgement about
// the command text only; the caller still requires the result to say
// "Exit code 1".
func exitsOneOnNoMatch(command string) bool {
	// Masked first, so the last segment is never inside a heredoc body and a
	// `bash -c '…'` string counts as the commands it runs.
	words := strings.Fields(lastSegment(strings.TrimRight(maskQuoted(command), " \t;")))
	// Skip leading VAR=value assignments and the wrappers that pass the
	// wrapped command's status through. xargs is not one: it turns a child's
	// exit 1 into 123.
	for len(words) > 0 && (strings.Contains(words[0], "=") || words[0] == "command" || words[0] == "sudo") {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	name := filepath.Base(words[0])
	if name == "git" {
		return len(words) > 1 && words[1] == "grep"
	}
	return noMatchExiters[name]
}

// lastSegment returns the text after the final top-level `|`, `;`, `&`
// (covering `&&`/`||`) or newline. Separators inside quotes do not count, so
// `grep -E "a|b" f` is one segment rather than ending at `b" f`.
func lastSegment(command string) string {
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '|' || c == ';' || c == '&' || c == '\n':
			start = i + 1
		}
	}
	return strings.TrimLeft(command[start:], " \t()")
}

// isExitOne reports whether a tool_result's content opens with Claude Code's
// "Exit code 1" banner (and not 10, 127, …). Content is raw JSON — a string
// or an array of text blocks — so only its first bytes are inspected.
func isExitOne(content []byte) bool {
	// The banner opens a JSON string: `"Exit code 1…` or, in block form,
	// `[{"type":"text","text":"Exit code 1…`.
	const banner = `"Exit code 1`
	i := bytes.Index(content, []byte(banner))
	if i < 0 || i > 32 {
		return false
	}
	rest := content[i+len(banner):]
	return len(rest) == 0 || rest[0] < '0' || rest[0] > '9'
}

// isShellError reports whether a failed result was raised by the shell
// itself rather than by the program it ran: zsh's eval wrapper prefixes its
// own errors with "(eval):N:" — `echo ==` (EQUALS expansion), an unquoted
// glob with no match (NOMATCH) — and a missing binary reads "command not
// found". Such a line aborts part-way, after some output already printed.
func isShellError(content []byte) bool {
	return bytes.Contains(content, []byte("(eval):")) || bytes.Contains(content, []byte("command not found"))
}

// Search modes, as announced by the plugin's UserPromptSubmit hook
// (adapters/claude/hooks/mode_prompt.js). ModeUnknown covers both `off` and a
// machine with no hook: the transcript cannot tell those apart.
const (
	ModeCodastre = "codastre"
	ModeGrep     = "grep"
	ModeAuto     = "auto"
	ModeUnknown  = ""
)

// searchMode reads the mode enum out of a hook-context attachment. Only the
// leading marker is compared; nothing of the text is kept.
func searchMode(a *attachment) string {
	if a == nil || a.Type != "hook_additional_context" || a.HookEvent != "UserPromptSubmit" {
		return ModeUnknown
	}
	for _, c := range a.Content {
		switch {
		case strings.HasPrefix(c, "CODASTRE-ONLY"):
			return ModeCodastre
		case strings.HasPrefix(c, "CODASTRE-FREE"):
			return ModeGrep
		case strings.HasPrefix(c, "CODASTRE-FIRST"):
			return ModeAuto
		}
	}
	return ModeUnknown
}
