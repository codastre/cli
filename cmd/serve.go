package cmd

import (
	"fmt"
	"os"

	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/mcpshim"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the stdio MCP proxy (and HEAD watcher unless --no-watch)",
	RunE:  runServe,
}

var serveNoWatch bool
var serveServerURL string

func init() {
	serveCmd.Flags().BoolVar(&serveNoWatch, "no-watch", false, "Skip HEAD watcher")
	serveCmd.Flags().StringVar(&serveServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	host := extractHost(serveServerURL)

	store, isFallback, err := keychain.Open()
	if err != nil {
		return fmt.Errorf("open keychain: %w", err)
	}
	if isFallback {
		fmt.Fprintln(os.Stderr, "warning: OS keychain unavailable; using file storage (~/.config/codastre/keys)")
	}

	apiKey, err := store.GetAPIKey(host)
	if err != nil {
		if v := os.Getenv("CODASTRE_API_KEY"); v != "" {
			apiKey = v
		} else {
			return fmt.Errorf("no API key for %s — run `codastre login` first: %w", host, err)
		}
	}

	repoRoot, _ := findGitRoot(".")

	// The HEAD watcher's doSync reads syncServerURL; align it with serve --server.
	syncServerURL = serveServerURL

	if !serveNoWatch && repoRoot != "" {
		go func() {
			if err := watchAndSync(); err != nil {
				fmt.Fprintf(os.Stderr, "watcher: %v\n", err)
			}
		}()
	}

	cfg := mcpshim.Config{
		ServerURL:  serveServerURL,
		APIKey:     apiKey,
		RepoRoot:   repoRoot,
		UnmaskPath: setupUnmask(serveServerURL, apiKey, repoRoot, store),
	}
	return mcpshim.Run(cfg, os.Stdin, os.Stdout)
}
