package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/codastre/cli/internal/study"
	"github.com/spf13/cobra"
)

var studyCmd = &cobra.Command{
	Use:   "study",
	Short: "Take part in (or read) a pre-registered paired study — tool vs no tool",
	Long: `Plane 4 of the value metrics: the same pre-registered task, run once with
Codastre and once without, measured exactly from both session transcripts.

A participant runs 'codastre study start <slug>'. The server assigns an arm
(tool or no_tool) and returns the pre-registered prompt; this command writes it
to ~/.config/codastre/study.json ($CODASTRE_STUDY_FILE overrides). The plugin
hook then claims the next NEW Claude Code session, injects the prompt verbatim
and enforces the arm — no_tool blocks Codastre on both the MCP and CLI planes.
Afterwards 'codastre collect --upload' reports that session's counters, tagged
with the assignment (needs CODASTRE_USAGE_REPORT=1).

Admins read the study with 'show', and judge correctness blind with 'judge'
and 'verdict' — the judge's queue carries no arm.`,
	SilenceUsage: true,
}

var (
	studyServerURL string
	studyKey       string
	studyTask      string
	studyJSON      bool
)

func init() {
	pf := studyCmd.PersistentFlags()
	pf.StringVar(&studyServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	pf.StringVar(&studyKey, "key", "", "API key (overrides $CODASTRE_API_KEY and keychain)")

	studyStartCmd.Flags().StringVar(&studyTask, "task", "", "Task to run (required when the study has several)")
	studyShowCmd.Flags().BoolVar(&studyJSON, "json", false, "Print the server's response verbatim")

	studyCmd.AddCommand(studyListCmd, studyStartCmd, studyStatusCmd, studyStopCmd,
		studyShowCmd, studyJudgeCmd, studyVerdictCmd)
	rootCmd.AddCommand(studyCmd)
}

func studyClient(cmd *cobra.Command) (*study.Client, error) {
	apiKey, warn, err := resolveAPIKey(studyServerURL, studyKey)
	if err != nil {
		return nil, err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}
	return &study.Client{BaseURL: studyServerURL, APIKey: apiKey}, nil
}

func studyCtx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cmd.Context(), 30*time.Second)
}

func studyFilePath() (string, error) {
	p := study.FilePath()
	if p == "" {
		return "", fmt.Errorf("cannot locate the study file: set $CODASTRE_STUDY_FILE")
	}
	return p, nil
}

var studyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the tenant's studies",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := studyClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := studyCtx(cmd)
		defer cancel()
		studies, err := c.List(ctx)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(studies) == 0 {
			fmt.Fprintln(out, "no studies registered")
			return nil
		}
		for _, s := range studies {
			fmt.Fprintf(out, "%s  %s  %s design, target n=%d, registered %s\n",
				s.Slug, s.Status, s.Design, s.TargetN, s.CreatedAt)
			for _, t := range s.Tasks {
				fmt.Fprintf(out, "    task %s (%s)  prompt sha256 %s\n", t.Task, t.Shape, t.PromptSHA256)
			}
		}
		return nil
	},
}

var studyStartCmd = &cobra.Command{
	Use:   "start <slug>",
	Short: "Get an arm assignment and arm the next new Claude Code session with the prompt",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := studyFilePath()
		if err != nil {
			return err
		}
		if cur, err := study.Load(path); err == nil {
			return fmt.Errorf("a study run is already active (assignment %s, arm %s): finish it and run "+
				"`codastre study stop` before starting another", cur.AssignmentID, cur.Arm)
		} else if !errors.Is(err, study.ErrNoFile) {
			return err
		}
		c, err := studyClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := studyCtx(cmd)
		defer cancel()
		a, err := c.Assign(ctx, args[0], studyTask)
		if err != nil {
			return err
		}
		if err := study.FromAssignment(a, time.Now()).Save(path); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		out := cmd.OutOrStdout()
		if a.Reused {
			fmt.Fprintln(out, "Re-using your unfinished assignment on this task.")
		}
		fmt.Fprintf(out, "Study %s, task %s — arm %s (search mode %s), run %d of the pair\n",
			a.Study, a.Task, a.Arm, a.Mode, a.ArmOrder)
		fmt.Fprintf(out, "assignment: %s\n", a.AssignmentID)
		fmt.Fprintf(out, "prompt sha256: %s\n", a.PromptSHA256)
		fmt.Fprintln(out, "Next: open a NEW Claude Code session in this repo and send any message — the "+
			"pre-registered prompt is injected; run `codastre collect --upload` afterwards "+
			"(needs CODASTRE_USAGE_REPORT=1).")
		fmt.Fprintln(out, "Do not rephrase or add to the task; hand the resulting diff to the judge "+
			"labelled with the assignment id, then run `codastre study stop`.")
		return nil
	},
}

var studyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the active local study run",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path, err := studyFilePath()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		f, err := study.Load(path)
		if errors.Is(err, study.ErrNoFile) {
			fmt.Fprintln(out, "no study run is active")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "study %s, task %s — arm %s (mode %s), run %d of the pair\n",
			f.Study, f.Task, f.Arm, f.Mode, f.ArmOrder)
		fmt.Fprintf(out, "assignment: %s\n", f.AssignmentID)
		if f.SessionID == nil || *f.SessionID == "" {
			fmt.Fprintln(out, "session: unclaimed — the next NEW Claude Code session will run it")
		} else {
			claimed := ""
			if f.ClaimedAt != nil {
				claimed = " at " + f.ClaimedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(out, "session: claimed%s\n", claimed)
		}
		return nil
	},
}

var studyStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "End the local study run (removes the study file)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path, err := studyFilePath()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(cmd.OutOrStdout(), "no study run is active")
				return nil
			}
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "study run ended; search mode is back to your own setting")
		return nil
	},
}
