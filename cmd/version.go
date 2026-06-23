package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Build metadata, injected at release time via the linker (-ldflags -X). The
// defaults make a plain `go build` / `go install` produce a clearly-marked dev
// binary; GoReleaser overwrites them for tagged releases.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the codastre CLI version",
	RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Fprintf(cmd.OutOrStdout(), "codastre %s (commit %s, built %s)\n", version, commit, date)
		return nil
	},
}

func init() {
	// Also exposes a `--version` flag on the root command.
	rootCmd.Version = version
	rootCmd.AddCommand(versionCmd)
}
