package cmd

import (
	"os"

	"github.com/codastre/cli/internal/clientheader"
	"github.com/spf13/cobra"
)

// clientOverride backs the persistent --client flag, defaulting to
// $CODASTRE_CLIENT so an unset flag still honors the env var — matching the
// plan's precedence (--client > $CODASTRE_CLIENT > shim clientInfo >
// codastre-cli/<version>; client-attribution-plan.md §8d). The Bash path
// (`codastre query` run by a model) is the case this exists for:
// CLAUDE_PLUGIN_ROOT is not in that shell's env, so neither the shim's
// initialize-sniffing nor a plugin-set env var can attribute it — only an
// explicit --client on the command line can.
var clientOverride string

// defaultClientOverride reads $CODASTRE_CLIENT. Its own function — rather than
// an inline os.Getenv in init, like most other --server-style defaults in this
// package — only so it's independently testable.
func defaultClientOverride() string {
	return os.Getenv("CODASTRE_CLIENT")
}

func init() {
	rootCmd.PersistentFlags().StringVar(&clientOverride, "client", defaultClientOverride(),
		"Attribute this invocation as <type>/<version> in the server's audit log [$CODASTRE_CLIENT]")

	// Resolves the override once, before any subcommand's RunE fires a
	// request. No subcommand defines its own PersistentPreRunE, so this always
	// runs first (cobra would otherwise let a child's override shadow it).
	rootCmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		clientheader.SetOverride(clientOverride)
		return nil
	}
}
