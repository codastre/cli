package transcript

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Render prints the transcript receipt. It leads with what is exact (cost,
// tokens, amplification), then the episode breakdown with its denominator
// showing, and it never prints a savings figure.
func Render(w io.Writer, s Summary) {
	fmt.Fprintf(w, "codastre savings — local, %s window (source: Claude Code transcripts)\n", s.Window)
	fmt.Fprintf(w, "state: %s\n", s.StatePath)

	if s.Sessions == 0 {
		fmt.Fprintln(w, "\nNo collected sessions in this window. Run `codastre collect` first.")
		return
	}

	fmt.Fprintf(w, "\nSessions\n  %s · %s · %s · %s\n",
		countLabel(s.Sessions, "session"),
		countLabel(s.Turns, "turn"),
		countLabel(s.Projects, "project"),
		countLabel(s.Days, "active day"))
	if s.CostedSess > 0 {
		fmt.Fprintf(w, "  $%.2f measured spend · %s wall clock · %s in tools (%s priced)\n",
			s.CostUSD, duration(s.DurationMS), duration(s.ToolTimeMS),
			countLabel(s.CostedSess, "session"))
	}

	fmt.Fprintf(w, "\nContext\n  input %s · output %s · thinking %s\n  cache read %s · cache write %s\n",
		thousands64(s.Tokens.Input), thousands64(s.Tokens.Output), thousands64(s.Tokens.Thinking),
		thousands64(s.Tokens.CacheRead), thousands64(s.Tokens.CacheCreation))
	fmt.Fprintf(w, "  re-read amplification %.0f%% = %s / (%s + %s + %s)\n",
		s.ContextAmplification*100,
		thousands64(s.Tokens.CacheRead), thousands64(s.Tokens.CacheRead),
		thousands64(s.Tokens.CacheCreation), thousands64(s.Tokens.Input))

	if len(s.Classes) > 0 {
		fmt.Fprintln(w, "\nTool results by class (exact bytes)")
		for _, c := range s.Classes {
			fmt.Fprintf(w, "  %-14s %6s calls   %9s\n", c.Class, thousands(c.Calls), bytesLabel(c.ResultBytes))
		}
	}

	renderOutcomes(w, s)
	fmt.Fprintf(w, "\n%s\n", wrap(s.Caveat, 88, "  "))
	fmt.Fprintln(w, "No counterfactual is computed: this is what ran, not what it saved.")
}

// renderOutcomes prints the rate with both failure rows and the excluded
// episodes visible, mirroring the worked example in the design. A reader must
// be able to compute the bad news themselves.
func renderOutcomes(w io.Writer, s Summary) {
	if len(s.Outcomes) == 0 {
		return
	}
	fmt.Fprintln(w, "\nSearch episodes (one per turn)")
	for _, o := range s.Outcomes {
		marker := " "
		if o.Outcome == OutcomeCodastreOnly {
			marker = "←"
		}
		fmt.Fprintf(w, "  %-24s %6s %s\n", o.Label, thousands(o.Episodes), marker)
	}
	if s.EpisodesWithCodastre == 0 {
		fmt.Fprintln(w, "  no episode made a codastre call — no rate to report")
		return
	}
	fmt.Fprintf(w, "\n  answered without fallback: %s / %s = %.1f%%\n",
		thousands(s.EpisodesCodastreOnly), thousands(s.EpisodesWithCodastre),
		s.AnsweredWithoutFallback*100)
	if s.EpisodesUnclassified > 0 {
		fmt.Fprintf(w, "  %s excluded from both sides (no search ran that turn)\n",
			countLabel(s.EpisodesUnclassified, "turn"))
	}
}

func countLabel(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return thousands(n) + " " + noun + "s"
}

func duration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.1f h", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.0f min", d.Minutes())
	default:
		return fmt.Sprintf("%.0f s", d.Seconds())
	}
}

func bytesLabel(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func thousands(n int) string { return thousands64(int64(n)) }

func thousands64(n int64) string {
	s := strconv.FormatInt(n, 10)
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

// wrap breaks a long caveat onto indented lines so it stays readable in a
// terminal instead of running off the edge.
func wrap(text string, width int, indent string) string {
	words := strings.Fields(text)
	var lines []string
	line := indent
	for _, word := range words {
		if len(line)+len(word)+1 > width && len(line) > len(indent) {
			lines = append(lines, line)
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	if len(line) > len(indent) {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
