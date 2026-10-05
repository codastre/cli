package transcript

import "strings"

// label applies one of the records Claude Code writes to name a session in
// its resume picker. They repeat as the session goes on; the latest wins.
func (s *Session) label(rec record) {
	switch rec.Type {
	case "ai-title":
		if t := strings.TrimSpace(rec.AITitle); t != "" {
			s.AITitle = t
		}
	case "custom-title":
		if t := strings.TrimSpace(rec.CustomTitle); t != "" {
			s.CustomTitle = t
		}
	case "pr-link":
		if rec.PRNumber > 0 {
			s.PRNumber = rec.PRNumber
		}
	}
}
