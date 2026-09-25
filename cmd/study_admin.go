package cmd

import (
	"fmt"

	"github.com/codastre/cli/internal/study"
	"github.com/spf13/cobra"
)

var studyShowCmd = &cobra.Command{
	Use:   "show <slug>",
	Short: "The study view (admin): pairs as rows, n against target, headline or why it is withheld",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := studyClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := studyCtx(cmd)
		defer cancel()
		body, err := c.View(ctx, args[0])
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if studyJSON {
			_, err := out.Write(append(body, '\n'))
			return err
		}
		v, err := study.ParseView(body)
		if err != nil {
			return err
		}
		study.RenderView(out, v)
		return nil
	},
}

var studyJudgeCmd = &cobra.Command{
	Use:   "judge <slug>",
	Short: "The blind judging queue (admin) — no arm, no order, no participant",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := studyClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := studyCtx(cmd)
		defer cancel()
		b, err := c.Blind(ctx, args[0])
		if err != nil {
			return err
		}
		study.RenderBlind(cmd.OutOrStdout(), b)
		return nil
	},
}

var studyVerdictCmd = &cobra.Command{
	Use:   "verdict <slug> <assignment_id> correct|incorrect",
	Short: "Record a blind verdict on one assignment (admin)",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		var correct bool
		switch args[2] {
		case "correct":
			correct = true
		case "incorrect":
			correct = false
		default:
			return fmt.Errorf("verdict must be `correct` or `incorrect`, got %q", args[2])
		}
		c, err := studyClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := studyCtx(cmd)
		defer cancel()
		if err := c.Verdict(ctx, args[0], args[1], correct); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "recorded: %s is %s\n", args[1], args[2])
		return nil
	},
}
