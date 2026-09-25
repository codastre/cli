package usage

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Render prints the local receipt. The layout mirrors the plugin's
// `/codastre:receipt` on purpose — the CLI and the plugin must not be able to
// say different things about the same log.
func Render(w io.Writer, s Summary) {
	fmt.Fprintf(w, "codastre savings — local, %s window\n", s.Window)
	fmt.Fprintf(w, "source: %s\n", s.LogPath)

	if s.Records == 0 {
		fmt.Fprintln(w, "\nNo search or read tool calls logged in this window.")
		fmt.Fprintln(w, "Set CODASTRE_TRACK_TOKENS=1 and install the Claude Code plugin to start logging.")
		return
	}

	fmt.Fprintln(w)
	for _, g := range s.Groups {
		fmt.Fprintf(w, "  %-26s %5s calls   ~%s tok\n",
			g.Label, thousands(g.Calls), thousands(g.Tokens))
	}
	fmt.Fprintf(w, "  %-26s %5s calls   ~%s tok\n",
		"TOTAL", thousands(s.TotalCalls), thousands(s.TotalToken))

	r := s.Reliance
	fmt.Fprintf(w, "\nReliance\n  %s · %s · %s\n",
		countLabel(r.Sessions, "session"),
		countLabel(r.Workspaces, "workspace"),
		countLabel(r.Days, "active day"))

	// The unflattering number, printed by default. A tool that only reports
	// its wins is the thing this design exists to avoid: the gap it names is
	// reach, not efficiency.
	if line := reachLine(r); line != "" {
		fmt.Fprintf(w, "  %s\n", line)
	}

	fmt.Fprintf(w, "\n%s\n", s.Caveat)
	fmt.Fprintln(w, "No counterfactual is computed: this is what ran, not what it saved.")
}

// reachLine states codastre calls against text-search calls, in whichever
// direction the log actually points.
func reachLine(r Reliance) string {
	if r.CodastreCalls == 0 && r.TextSearchCall == 0 {
		return ""
	}
	base := fmt.Sprintf("search calls: %s codastre vs %s text search",
		thousands(r.CodastreCalls), thousands(r.TextSearchCall))
	switch {
	case r.CodastreCalls == 0:
		return base + " — codastre unused in this window"
	case r.TextSearchCall == 0:
		return base + " — no text-search fallback logged"
	case r.TextSearchCall > r.CodastreCalls:
		return fmt.Sprintf("%s — %.1f:1 against the tool",
			base, float64(r.TextSearchCall)/float64(r.CodastreCalls))
	default:
		return fmt.Sprintf("%s — %.1f:1 toward the tool",
			base, float64(r.CodastreCalls)/float64(r.TextSearchCall))
	}
}

func countLabel(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return thousands(n) + " " + noun + "s"
}

// thousands formats an int with comma separators, matching the receipt's
// toLocaleString() output for en-US.
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
