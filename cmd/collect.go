package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/codastre/cli/internal/report"
	"github.com/codastre/cli/internal/transcript"
	"github.com/codastre/cli/internal/usage"
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
time, per-tool result bytes, one search episode per turn with its outcome, and
context compactions from the plugin's hook event log
(~/.config/codastre/session-events.jsonl, $CODASTRE_SESSION_EVENTS_LOG
overrides). 'codastre savings' reads the result.

The parse happens locally and only locally. A transcript holds prompts, source
code, file paths and full tool output; none of that is stored, printed or sent
anywhere by this command. What lands in the state file is counts, byte totals
and timestamps — nothing else.

Upload is opt-in twice: it needs CODASTRE_USAGE_REPORT=1 *and* --upload. It
sends counters only — per-turn episode outcomes and per-session totals, with
the session id replaced by an HMAC under your tenant's usage key. The first
upload-enabled run records a consent boundary; sessions that started before it
are never uploaded.

Examples:
  codastre collect
  codastre collect --limit 20
  codastre collect --json
  CODASTRE_USAGE_REPORT=1 codastre collect --upload`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runCollect,
}

var (
	collectLimit int
	collectJSON  bool
	collectRoot  string
	collectReset bool

	collectUpload    bool
	collectServerURL string
	collectKey       string
)

func init() {
	f := collectCmd.Flags()
	f.IntVar(&collectLimit, "limit", 0, "Parse at most N transcripts, newest first (0 = all)")
	f.BoolVar(&collectJSON, "json", false, "Emit the run summary as JSON")
	f.StringVar(&collectRoot, "projects-dir", "", "Transcript root [$CLAUDE_PROJECTS_DIR]")
	f.BoolVar(&collectReset, "reset", false, "Discard collected state and re-parse from scratch")
	f.BoolVar(&collectUpload, "upload", false, "Upload counters to the server (requires CODASTRE_USAGE_REPORT=1)")
	f.StringVar(&collectServerURL, "server", defaultServerURL(), "Server URL, for --upload [$CODASTRE_SERVER]")
	f.StringVar(&collectKey, "key", "", "API key, for --upload (overrides $CODASTRE_API_KEY and keychain)")
	rootCmd.AddCommand(collectCmd)
}

func runCollect(cmd *cobra.Command, _ []string) error {
	if collectUpload && !usage.UploadEnabled() {
		return fmt.Errorf("--upload needs the opt-in CODASTRE_USAGE_REPORT=1: nothing is " +
			"uploaded unless you set it — the local collection works without it")
	}
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
		// The consent boundary is a decision, not a cache: it survives a reset.
		since := state.ReportSince
		state = transcript.LoadState("")
		state.ReportSince = since
	}
	started := time.Now()
	res, err := transcript.Collect(root, state, collectLimit)
	if err != nil {
		return fmt.Errorf("collect %s: %w", root, err)
	}
	hooks, err := transcript.CollectHookEvents(transcript.HookLogPath(), state)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: hook event log: %v\n", err)
	}

	var up *report.Result
	var upErr error
	if collectUpload {
		r, err := uploadCollected(cmd, state)
		up, upErr = &r, err
	}
	// Saved even when the upload failed: batches that did land advanced their
	// marks, and the consent boundary is recorded either way.
	if err := state.Save(statePath); err != nil {
		return fmt.Errorf("write %s: %w", statePath, err)
	}

	out := cmd.OutOrStdout()
	if collectJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		// The run summary stays at the top level, as before; hook and upload
		// counters ride alongside it.
		payload := struct {
			transcript.CollectResult
			Hooks  transcript.HookResult `json:"hooks"`
			Upload *report.Result        `json:"upload,omitempty"`
		}{res, hooks, up}
		if err := enc.Encode(payload); err != nil {
			return err
		}
		return upErr
	}
	fmt.Fprintf(out, "%d transcripts seen, %d parsed, %d already current, %d unreadable\n",
		res.FilesSeen, res.FilesParsed, res.FilesSkipped, res.FilesFailed)
	fmt.Fprintf(out, "%d sessions and %d turns collected (%s read in %s)\n",
		res.Sessions, res.Turns, byteCount(res.BytesRead), time.Since(started).Round(time.Millisecond))
	if hooks.Compactions > 0 {
		fmt.Fprintf(out, "%d compactions read from the hook event log\n", hooks.Compactions)
	}
	if up == nil {
		fmt.Fprintf(out, "state: %s — local only, nothing uploaded\n", statePath)
		fmt.Fprintln(out, "Run `codastre savings` to read it back.")
		return nil
	}
	fmt.Fprintf(out, "state: %s\n", statePath)
	renderUpload(out, *up)
	return upErr
}

// uploadCollected sends the new counters. Counters only — see package report.
func uploadCollected(cmd *cobra.Command, state *transcript.State) (report.Result, error) {
	apiKey, warn, err := resolveAPIKey(collectServerURL, collectKey)
	if err != nil {
		return report.Result{}, err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	c := &report.Client{BaseURL: collectServerURL, APIKey: apiKey}
	return report.Run(ctx, c, state, time.Now())
}

func renderUpload(w io.Writer, r report.Result) {
	if r.ConsentRecorded {
		fmt.Fprintf(w, "upload consent recorded: sessions started from %s on are reported\n",
			r.ReportSince.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "upload: %d episodes sent (%d new, %d updated, %d not yours), "+
		"%d sessions sent (%d new, %d updated, %d not yours)\n",
		r.Episodes.Received, r.Episodes.Inserted, r.Episodes.Updated, r.Episodes.Skipped,
		r.Sessions.Received, r.Sessions.Inserted, r.Sessions.Updated, r.Sessions.Skipped)
	fmt.Fprintf(w, "%d sessions eligible; %d started before the consent boundary (%s) and stay local",
		r.SessionsEligible, r.SessionsBeforeConsent, r.ReportSince.Format(time.RFC3339))
	if r.SessionsWithoutCost > 0 {
		fmt.Fprintf(w, "; %d have no cost record yet and wait", r.SessionsWithoutCost)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Counters only: no prompt, path, query or tool output was sent.")
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
