package cmd

import (
	"fmt"

	"github.com/codastre/cli/internal/buildinfo"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the codastre CLI version",
	RunE: func(cmd *cobra.Command, _ []string) error {
		v, c, d := buildinfo.Resolve()
		fmt.Fprintf(cmd.OutOrStdout(), "codastre %s (commit %s, built %s)\n", v, c, d)
		return nil
	},
}

func init() {
	// Also exposes a `--version` flag on the root command.
	v, _, _ := buildinfo.Resolve()
	rootCmd.Version = v
	rootCmd.AddCommand(versionCmd)
}
