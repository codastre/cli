package cmd

import (
	"fmt"
	"os"

	"github.com/codastre/cli/internal/checkouts"
	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/mcpshim"
	"github.com/codastre/cli/internal/unmask"
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

	repoScheme, repoRootFor, cwdRepoID := federatedHydration(serveServerURL, apiKey, repoRoot)

	cfg := mcpshim.Config{
		ServerURL:   serveServerURL,
		APIKey:      apiKey,
		RepoRoot:    repoRoot,
		UnmaskPath:  setupUnmask(serveServerURL, apiKey, repoRoot, store),
		RepoScheme:  repoScheme,
		RepoRootFor: repoRootFor,
		CWDRepoID:   cwdRepoID,
	}
	return mcpshim.Run(cfg, os.Stdin, os.Stdout)
}

// federatedHydration builds the per-repo scheme/root lookups the proxy uses to
// hydrate snippets across ALL result repos, not just the CWD one. It snapshots
// GET /v1/repos into repo_id → {remote_url, masking_scheme}, so:
//   - `none`-scheme repos get identity-mapped real paths + snippets (the common
//     dev/eval setup, previously starved of snippets because it has no unmasker);
//   - a federated hit from another repo hydrates from that repo's local checkout
//     (the CWD repo, or one remembered in ~/.config/codastre/checkouts.json).
//
// It also returns the CWD repo's repo_id, the one repo for which the CWD
// checkout is a sound hydration root (see mcpshim.Config.rootFor).
//
// Best-effort: on any error it returns (nil, nil, "") and the proxy falls back to
// CWD-only, UnmaskPath-gated behavior. The snapshot is taken once at startup;
// repos registered mid-session won't hydrate until the next `serve`.
func federatedHydration(serverURL, apiKey, cwdRoot string) (
	func(repoID string) (string, bool),
	func(repoID string) (string, bool),
	string,
) {
	repos, err := unmask.ListRepos(serverURL, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list repos for federated snippet hydration: %v\n", err)
		return nil, nil, ""
	}
	byID := make(map[string]unmask.RepoInfo, len(repos))
	for _, r := range repos {
		byID[r.RepoID] = r
	}
	_, cwdURL := cwdRepo() // normalized origin of the CWD repo ("" when not in a repo)

	cwdRepoID := ""
	if cwdURL != "" {
		for _, r := range repos {
			if r.RemoteURL == cwdURL {
				cwdRepoID = r.RepoID
				break
			}
		}
	}

	scheme := func(repoID string) (string, bool) {
		r, ok := byID[repoID]
		if !ok {
			return "", false
		}
		return r.MaskingScheme, true
	}
	rootFor := func(repoID string) (string, bool) {
		r, ok := byID[repoID]
		if !ok {
			return "", false
		}
		if cwdRoot != "" && cwdURL != "" && r.RemoteURL == cwdURL {
			return cwdRoot, true // the CWD checkout, no registry lookup needed
		}
		return checkouts.Lookup(r.RemoteURL)
	}
	return scheme, rootFor, cwdRepoID
}
