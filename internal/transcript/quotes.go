package transcript

import (
	"path"
	"regexp"
	"strings"
)

// maskQuoted mirrors maskQuoted in the plugin's adapters/claude/hooks/lib.js
// (codastre-claude repo), and the two share a test table
// (testdata/bash-classify-fixtures.json): change both together.
//
// The classifier regexes look for a command at a boundary, so a quoted
// argument that merely mentions one — `git commit -m "fix (grep|rg)"`, an
// `echo "; rg"`, a Python heredoc — classified as a text search. maskQuoted
// blanks quoted text and heredoc bodies before matching, keeping what the
// shell actually executes:
//   - `$(…)` / backtick substitutions inside double quotes;
//   - the string handed to a shell (`sh|bash|zsh|dash|ksh … -c '…'`, `eval '…'`);
//   - a heredoc whose consumer is a shell (`bash <<EOF`).
//
// The result is only ever matched, never run, so it also normalises the
// separators the regexes don't list: an unquoted newline becomes `;`, and so
// do the quotes around a string a shell will run.
var (
	shellRunner = regexp.MustCompile("(?:^|[\\s|;&(`])(?:(?:ba|z|da|k)?sh|eval)(?:\\s+-[A-Za-z]+)*\\s+$")
	heredocOpen = regexp.MustCompile(`^<<-?[ \t]*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|([A-Za-z_][A-Za-z0-9_]*))`)
	shells      = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}
	segmentSep  = regexp.MustCompile(`[|;&\n(]`)
)

func blank(text string) string { return strings.Repeat(" ", len(text)) }

// closingQuote returns the index of the quote closing the one at open, or
// len(s) if it is unterminated. Inside double quotes a `"` within `$(…)` or
// backticks belongs to the substitution, not to the string.
func closingQuote(s string, open int) int {
	if s[open] == '\'' {
		if end := strings.IndexByte(s[open+1:], '\''); end >= 0 {
			return open + 1 + end
		}
		return len(s)
	}
	depth, tick := 0, false
	for i := open + 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case c == '`':
			tick = !tick
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			depth++
			i++
		case c == ')' && depth > 0:
			depth--
		case c == '"' && depth == 0 && !tick:
			return i
		}
	}
	return len(s)
}

// maskDouble blanks a double-quoted body except its command substitutions.
func maskDouble(body string) string {
	var b strings.Builder
	depth, tick := 0, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '`':
			tick = !tick
			b.WriteByte(c)
		case c == '$' && i+1 < len(body) && body[i+1] == '(':
			depth++
			b.WriteString("$(")
			i++
		case c == ')' && depth > 0:
			depth--
			b.WriteByte(c)
		case depth > 0 || tick:
			b.WriteByte(c)
		default:
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// feedsShell reports whether the command a heredoc feeds (the first word of
// its segment) is a shell.
func feedsShell(before string) bool {
	parts := segmentSep.Split(before, -1)
	words := strings.Fields(parts[len(parts)-1])
	return len(words) > 0 && shells[path.Base(words[0])]
}

type heredoc struct {
	delim string
	keep  bool
}

func maskQuoted(s string) string {
	var out strings.Builder
	var pending []heredoc
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			end := min(i+2, len(s))
			out.WriteString(s[i:end])
			i = end
		case c == '\'' || c == '"':
			end := closingQuote(s, i)
			body := s[i+1 : end]
			closed := end < len(s)
			if shellRunner.MatchString(s[:i]) {
				out.WriteByte(';')
				out.WriteString(strings.ReplaceAll(body, "\n", ";"))
				if closed {
					out.WriteByte(';')
				}
			} else {
				out.WriteByte(c)
				if c == '"' {
					out.WriteString(maskDouble(body))
				} else {
					out.WriteString(blank(body))
				}
				if closed {
					out.WriteByte(c)
				}
			}
			i = end + 1
		case c == '<' && strings.HasPrefix(s[i:], "<<") && !strings.HasPrefix(s[i:], "<<<") && heredocOpen.MatchString(s[i:]):
			m := heredocOpen.FindStringSubmatch(s[i:])
			pending = append(pending, heredoc{delim: m[1] + m[2] + m[3], keep: feedsShell(s[:i])})
			out.WriteString(m[0])
			i += len(m[0])
		case c == '\n':
			out.WriteByte(';')
			i++
			// Heredoc bodies follow the line that opened them, in order; each
			// runs to a line that is exactly its delimiter (leading tabs
			// allowed, for <<-).
			for _, h := range pending {
				for i < len(s) {
					lineEnd := strings.IndexByte(s[i:], '\n')
					if lineEnd < 0 {
						lineEnd = len(s)
					} else {
						lineEnd += i
					}
					line := s[i:lineEnd]
					sep := ""
					if lineEnd < len(s) {
						sep = ";"
					}
					i = lineEnd + 1
					if strings.TrimRight(strings.TrimLeft(line, "\t"), " \t\r") == h.delim {
						out.WriteString(line + sep)
						break
					}
					if h.keep {
						out.WriteString(line)
					} else {
						out.WriteString(blank(line))
					}
					out.WriteString(sep)
				}
			}
			pending = nil
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}
