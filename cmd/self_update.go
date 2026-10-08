package cmd

import (
	"fmt"
	"runtime"

	"github.com/codastre/cli/internal/buildinfo"
	"github.com/codastre/cli/internal/selfupdate"
	"github.com/spf13/cobra"
)

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Replace this binary with the latest codastre release (checksum-verified)",
	Long: `Download the latest release from github.com/codastre/cli, verify it against the
release's checksums.txt, and atomically replace the running binary.

  --check            report whether an update is available; change nothing
  --version X.Y.Z    install that release instead of the latest (downgrades allowed)
  --force            reinstall even when up to date, or replace a development build

A running ` + "`codastre serve`" + ` keeps the old binary until it restarts — restart your
agent session (or its MCP server) to pick up the new version.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSelfUpdate,
}

var (
	selfUpdateCheck   bool
	selfUpdateVersion string
	selfUpdateForce   bool

	// Seams for tests.
	newSelfUpdateClient  = selfupdate.NewClient
	selfUpdateExecutable = selfupdate.ExecutablePath
	selfUpdateCurrent    = func() string { v, _, _ := buildinfo.Resolve(); return v }
)

func init() {
	selfUpdateCmd.Flags().BoolVar(&selfUpdateCheck, "check", false, "Only report whether an update is available")
	selfUpdateCmd.Flags().StringVar(&selfUpdateVersion, "version", "", "Install this release version instead of the latest")
	selfUpdateCmd.Flags().BoolVar(&selfUpdateForce, "force", false, "Reinstall even if up to date, or replace a development build")
	rootCmd.AddCommand(selfUpdateCmd)
}

func runSelfUpdate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	client := newSelfUpdateClient()
	current := selfUpdateCurrent()

	rel, err := resolveSelfUpdateRelease(cmd, client)
	if err != nil {
		return err
	}
	target := rel.Version()
	status := selfupdate.Compare(current, target)

	if selfUpdateCheck {
		switch status {
		case selfupdate.Outdated:
			fmt.Fprintf(out, "Update available: %s → %s\nRun `codastre self-update` to upgrade.\n", current, target)
		case selfupdate.Unknown:
			fmt.Fprintf(out, "Running development build %s; latest release is %s.\n", current, target)
		default:
			fmt.Fprintf(out, "codastre %s is up to date (latest release %s).\n", current, target)
		}
		return nil
	}

	if !selfUpdateForce {
		switch {
		case status == selfupdate.Unknown:
			return fmt.Errorf("running development build %s; pass --force to replace it with release %s", current, target)
		case selfUpdateVersion == "" && status == selfupdate.UpToDate:
			fmt.Fprintf(out, "codastre %s is up to date.\n", current)
			return nil
		case selfUpdateVersion != "" && current == target:
			fmt.Fprintf(out, "codastre %s is already installed; pass --force to reinstall.\n", current)
			return nil
		}
	}

	exe, err := selfUpdateExecutable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	name, archive, err := client.DownloadArchive(ctx, rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	binary, err := selfupdate.ExtractBinary(name, archive)
	if err != nil {
		return err
	}
	if err := selfupdate.Replace(exe, binary); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	fmt.Fprintf(out, "Updated codastre %s → %s (%s)\n", current, target, exe)
	fmt.Fprintln(out, "Restart running `codastre serve` processes (agent sessions) to use the new version.")
	return nil
}

func resolveSelfUpdateRelease(cmd *cobra.Command, client *selfupdate.Client) (selfupdate.Release, error) {
	if selfUpdateVersion != "" {
		rel, err := client.ByVersion(cmd.Context(), selfUpdateVersion)
		if err != nil {
			return selfupdate.Release{}, fmt.Errorf("release %s: %w", selfUpdateVersion, err)
		}
		return rel, nil
	}
	rel, err := client.Latest(cmd.Context())
	if err != nil {
		return selfupdate.Release{}, fmt.Errorf("latest release: %w", err)
	}
	return rel, nil
}
