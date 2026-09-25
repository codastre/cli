package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/codastre/cli/internal/report"
	"github.com/codastre/cli/internal/transcript"
	"github.com/codastre/cli/internal/usage"
	"github.com/spf13/cobra"
)

// stdinIsTerminal reports whether the confirmation prompt can be answered by
// a person. A var so tests can stand in for a terminal.
var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// runBackfill is the D7 path: history from before the consent boundary,
// previewed by default and uploaded only on an affirmative, per-run answer.
// It never moves state.ReportSince and never tags a row with a study block.
func runBackfill(cmd *cobra.Command, state *transcript.State, statePath string) error {
	out := cmd.OutOrStdout()
	plan := report.Select(state, report.ScopeBackfill, time.Now())
	preview := plan.Preview()

	if !collectUpload {
		if collectJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(preview); err != nil {
				return err
			}
		} else {
			renderBackfillPreview(out, preview)
			fmt.Fprintln(out, "Preview only: nothing left this machine. To send it, re-run with --upload "+
				"(needs CODASTRE_USAGE_REPORT=1).")
		}
		return state.Save(statePath)
	}

	if plan.Empty() {
		fmt.Fprintln(out, "backfill: nothing from before the consent boundary is left to upload")
		return state.Save(statePath)
	}
	if !collectYes {
		if !stdinIsTerminal() {
			return fmt.Errorf("backfill upload needs confirmation: not on a terminal, so pass --yes " +
				"(run `codastre collect --backfill` first to see exactly what would be sent)")
		}
		renderBackfillPreview(out, preview)
		fmt.Fprintf(out, "Upload %d sessions and %d episodes that predate your consent boundary (%s)? Type \"yes\": ",
			len(preview.SessionRows), len(preview.EpisodeRows), boundaryLabel(preview.ReportSince))
		answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if strings.TrimSpace(answer) != "yes" {
			fmt.Fprintln(out, "Not confirmed: nothing was uploaded.")
			return state.Save(statePath)
		}
	}

	apiKey, warn, err := resolveAPIKey(collectServerURL, collectKey)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	c := &report.Client{BaseURL: collectServerURL, APIKey: apiKey}
	res, upErr := report.Send(ctx, c, plan)
	// Saved even on failure: batches that landed advanced their marks.
	if err := state.Save(statePath); err != nil {
		return fmt.Errorf("write %s: %w", statePath, err)
	}
	if collectJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
		return upErr
	}
	fmt.Fprintf(out, "backfill: %d episodes sent (%d new, %d updated, %d not yours), "+
		"%d sessions sent (%d new, %d updated, %d not yours)\n",
		res.Episodes.Received, res.Episodes.Inserted, res.Episodes.Updated, res.Episodes.Skipped,
		res.Sessions.Received, res.Sessions.Inserted, res.Sessions.Updated, res.Sessions.Skipped)
	fmt.Fprintln(out, "This run only: the consent boundary did not move, and no later run backfills without asking again.")
	fmt.Fprintln(out, "Counters only: no prompt, path, query or tool output was sent.")
	return upErr
}

func boundaryLabel(t time.Time) string {
	if t.IsZero() {
		return "none recorded yet — everything collected so far"
	}
	return t.Format(time.RFC3339)
}

func renderBackfillPreview(w io.Writer, p report.Preview) {
	fmt.Fprintf(w, "backfill: sessions that started before the consent boundary (%s)\n",
		boundaryLabel(p.ReportSince))
	if len(p.Sessions) == 0 {
		fmt.Fprintln(w, "  nothing to send")
	}
	for _, s := range p.Sessions {
		cost := "no cost record (episodes only)"
		if s.HasSession {
			cost = fmt.Sprintf("$%.4f measured, %s", s.CostUSD, (time.Duration(s.DurationMS) * time.Millisecond).Round(time.Second))
		}
		fmt.Fprintf(w, "  %s  %d episodes  %s\n", s.StartedAt.Format(time.RFC3339), s.Episodes, cost)
	}
	fmt.Fprintf(w, "would send: %d session rows, %d episode rows (counters only; session ids as %s)\n",
		len(p.SessionRows), len(p.EpisodeRows), report.PlaceholderRef)
	if !usage.UploadEnabled() {
		fmt.Fprintln(w, "CODASTRE_USAGE_REPORT=1 is not set: an upload would be refused.")
	}
}
