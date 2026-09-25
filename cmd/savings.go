package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/codastre/cli/internal/usage"
	"github.com/spf13/cobra"
)

var savingsCmd = &cobra.Command{
	Use:   "savings",
	Short: "What your search tooling actually cost, from your own local log",
	Long: `Summarise your own search-tool usage from the local plugin log.

Everything here is read from ~/.config/codastre/claude-token-log.jsonl
($CODASTRE_TOKEN_LOG overrides), written by the Claude Code plugin hook when
CODASTRE_TRACK_TOKENS=1 or a live A/B mode is active. No server call, no
upload, no opt-in — the number is yours before it is anyone else's.

What it reports:
  • result tokens and call counts per class — codastre (MCP and CLI planes
    separately), text search, file reads
  • reliance — sessions, workspaces and active days the log covers
  • the codastre-vs-text-search call ratio, in whichever direction it points

What it does not report, by design: tokens saved. There is no observable
counterfactual for a search that never ran, so none is computed, stored or
printed. Token figures are byte-ratio estimates (±20%), exclude reasoning
tokens, and are not billing-grade.

Examples:
  codastre savings
  codastre savings --window 7d
  codastre savings --window all --json`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSavings,
}

var (
	savingsWindow  string
	savingsJSON    bool
	savingsLogPath string
)

func init() {
	f := savingsCmd.Flags()
	f.StringVar(&savingsWindow, "window", "30d", "Window to summarise: 7d | 30d | all | <N>d")
	f.BoolVar(&savingsJSON, "json", false, "Emit the summary as JSON")
	f.StringVar(&savingsLogPath, "log", "", "Path to the token log [$CODASTRE_TOKEN_LOG]")
	rootCmd.AddCommand(savingsCmd)
}

func runSavings(cmd *cobra.Command, _ []string) error {
	since, window, err := usage.ParseWindow(savingsWindow, time.Now())
	if err != nil {
		return err
	}

	logPath := savingsLogPath
	if logPath == "" {
		logPath = usage.DefaultLogPath()
	}
	if logPath == "" {
		return fmt.Errorf("cannot locate the token log: no home directory and no --log/$CODASTRE_TOKEN_LOG")
	}

	records, err := usage.Load(logPath, since)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error condition: the common case is a developer who has
			// not turned tracking on yet, and the fix is one line.
			return noLogYet(cmd, logPath, window)
		}
		return fmt.Errorf("read %s: %w", logPath, err)
	}

	summary := usage.Aggregate(records, window, logPath, since)
	if savingsJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}
	usage.Render(cmd.OutOrStdout(), summary)
	return nil
}

// noLogYet reports an absent log in the requested format and exits 0. Nothing
// is broken — there is simply nothing recorded.
func noLogYet(cmd *cobra.Command, logPath, window string) error {
	summary := usage.Aggregate(nil, window, logPath, time.Time{})
	if savingsJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "No token log at %s.\n", logPath)
	fmt.Fprintln(out, "Install the Claude Code plugin and set CODASTRE_TRACK_TOKENS=1 to start logging.")
	return nil
}
