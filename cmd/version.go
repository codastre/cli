package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Build metadata, injected at release time via the linker (-ldflags -X).
// GoReleaser overwrites these for tagged releases. When they keep their defaults
// (a plain `go build` or `go install …@version`), resolveBuildInfo fills them in
// from the Go build info embedded in the binary instead of reporting "dev".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the codastre CLI version",
	RunE: func(cmd *cobra.Command, _ []string) error {
		v, c, d := resolveBuildInfo()
		fmt.Fprintf(cmd.OutOrStdout(), "codastre %s (commit %s, built %s)\n", v, c, d)
		return nil
	},
}

func init() {
	// Also exposes a `--version` flag on the root command.
	v, _, _ := resolveBuildInfo()
	rootCmd.Version = v
	rootCmd.AddCommand(versionCmd)
}

// resolveBuildInfo returns the effective (version, commit, date), preferring the
// linker-injected values and otherwise falling back to the binary's Go build
// info (`go install …@vX.Y.Z` records a module version; `go build` from a git
// checkout records a VCS revision/time/dirty stamp).
func resolveBuildInfo() (string, string, string) {
	bi, _ := debug.ReadBuildInfo()
	return computeBuildInfo(version, commit, date, bi)
}

// computeBuildInfo is the pure core of resolveBuildInfo, taking the build info
// explicitly so it can be unit-tested without rebuilding the binary.
func computeBuildInfo(v, c, d string, bi *debug.BuildInfo) (string, string, string) {
	if bi == nil {
		return v, c, d
	}

	// Prefer the module version recorded by `go install …@version` (empty or
	// "(devel)" for a plain `go build`).
	if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}

	// Fill commit/date and detect a dirty tree from the VCS stamp `go build`
	// embeds for builds from a checkout.
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if c == "none" && s.Value != "" {
				c = s.Value
			}
		case "vcs.time":
			if d == "unknown" && s.Value != "" {
				d = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	// A plain `go build` from a checkout has no module version; use the short
	// commit so the binary still self-identifies rather than reporting "dev".
	if v == "dev" && c != "none" {
		v = shortCommit(c)
	}
	if dirty && v != "dev" {
		v += "-dirty"
	}
	return v, c, d
}

func shortCommit(c string) string {
	const short = 12
	if len(c) > short {
		return c[:short]
	}
	return c
}
