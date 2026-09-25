package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/codastre/cli/internal/transcript"
	"github.com/spf13/cobra"
)

var collectCmd = &cobra.Command{
	Use:   "collect",
	Short: "Parse local Claude Code transcripts into usage counters (local only)",
	Long: `Turn the Claude Code session transcripts already on this machine into counters.

Reads ~/.claude/projects/*/*.jsonl ($CLAUDE_PROJECTS_DIR overrides), newest
first, resuming each file from a watermark in ~/.config/codastre/collect-state.json.
Re-running with nothing appended reads no bytes and changes nothing.

What it extracts: exact per-session tokens, measured USD, wall-clock and tool
time, per-tool result bytes, and one search episode per turn with its outcome.
'codastre savings' reads the result.

The parse happens locally and only locally. A transcript holds prompts, source
code, file paths and full tool output; none of that is stored, printed or sent
anywhere by this command. What lands in the state file is counts, byte totals
and timestamps — nothing else, and there is no upload path in this command at
all.

Examples:
  codastre collect
  codastre collect --limit 20
  codastre collect --json`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runCollect,
}

var (
	collectLimit int
	collectJSON  bool
	collectRoot  string
	collectReset bool
)

func init() {
	f := collectCmd.Flags()
	f.IntVar(&collectLimit, "limit", 0, "Parse at most N transcripts, newest first (0 = all)")
	f.BoolVar(&collectJSON, "json", false, "Emit the run summary as JSON")
	f.StringVar(&collectRoot, "projects-dir", "", "Transcript root [$CLAUDE_PROJECTS_DIR]")
	f.BoolVar(&collectReset, "reset", false, "Discard collected state and re-parse from scratch")
	rootCmd.AddCommand(collectCmd)
}

func runCollect(cmd *cobra.Command, _ []string) error {
	root := collectRoot
	if root == "" {
		root = transcript.ProjectsDir()
	}
	if root == "" {
		return fmt.Errorf("cannot locate the transcript root: set --projects-dir or $CLAUDE_PROJECTS_DIR")
	}
	statePath := transcript.StatePath()
	if statePath == "" {
		return fmt.Errorf("cannot locate the state file: set $CODASTRE_COLLECT_STATE")
	}

	state := transcript.LoadState(statePath)
	if collectReset {
		state = transcript.LoadState("")
	}
	started := time.Now()
	res, err := transcript.Collect(root, state, collectLimit)
	if err != nil {
		return fmt.Errorf("collect %s: %w", root, err)
	}
	if err := state.Save(statePath); err != nil {
		return fmt.Errorf("write %s: %w", statePath, err)
	}

	out := cmd.OutOrStdout()
	if collectJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprintf(out, "%d transcripts seen, %d parsed, %d already current, %d unreadable\n",
		res.FilesSeen, res.FilesParsed, res.FilesSkipped, res.FilesFailed)
	fmt.Fprintf(out, "%d sessions and %d turns collected (%s read in %s)\n",
		res.Sessions, res.Turns, byteCount(res.BytesRead), time.Since(started).Round(time.Millisecond))
	fmt.Fprintf(out, "state: %s — local only, nothing uploaded\n", statePath)
	fmt.Fprintln(out, "Run `codastre savings` to read it back.")
	return nil
}

func byteCount(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
