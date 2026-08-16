package cmd

import (
	"fmt"
	"io"
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
var serveMaxSnippetLines int
var serveNoSnippets bool
var serveQuiet bool

func init() {
	serveCmd.Flags().BoolVar(&serveNoWatch, "no-watch", false, "Skip HEAD watcher")
	serveCmd.Flags().StringVar(&serveServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	serveCmd.Flags().IntVar(&serveMaxSnippetLines, "max-snippet-lines", defaultMaxSnippetLines(),
		"Cap each hydrated snippet at N lines; truncated results are marked "+
			"(0 = built-in default) [$CODASTRE_MAX_SNIPPET_LINES]")
	serveCmd.Flags().BoolVar(&serveNoSnippets, "no-snippets", defaultNoSnippets(),
		"Return ranked locations only, without hydrating snippet bodies [$CODASTRE_NO_SNIPPETS]")
	serveCmd.Flags().BoolVar(&serveQuiet, "quiet", false, "Suppress the per-QUERY cost line on stderr")
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

	hy := federatedHydration(serveServerURL, apiKey, repoRoot)

	// Cost log goes to stderr: stdout carries the JSON-RPC stream and any stray
	// byte on it corrupts the transport.
	var costLog io.Writer
	if !serveQuiet {
		costLog = os.Stderr
	}

	cfg := mcpshim.Config{
		ServerURL:       serveServerURL,
		APIKey:          apiKey,
		RepoRoot:        repoRoot,
		UnmaskPath:      setupUnmask(serveServerURL, apiKey, repoRoot, store),
		RepoScheme:      hy.Scheme,
		RepoRootFor:     hy.RootFor,
		RepoRemoteURL:   hy.RemoteURL,
		CWDRepoID:       hy.CWDRepoID,
		MaxSnippetLines: serveMaxSnippetLines,
		NoSnippets:      serveNoSnippets,
		Log:             costLog,
	}
	return mcpshim.Run(cfg, os.Stdin, os.Stdout)
}

// hydrationLookups carries the per-repo lookups the proxy needs. It is a struct
// rather than a tuple of returns because three of the four members share the
// type func(string) (string, bool): as positional results, swapping RootFor and
// RemoteURL at either end compiles cleanly and silently joins repo paths onto
// remote URLs — nothing hydrates and nothing errors.
type hydrationLookups struct {
	Scheme    func(repoID string) (string, bool)
	RootFor   func(repoID string) (string, bool)
	RemoteURL func(repoID string) (string, bool)
	CWDRepoID string
}

// federatedHydration builds the per-repo scheme/root lookups the proxy uses to
// hydrate snippets across ALL result repos, not just the CWD one. It snapshots
// GET /v1/repos into repo_id → {remote_url, masking_scheme}, so:
//   - `none`-scheme repos get identity-mapped real paths + snippets (the common
//     dev/eval setup, previously starved of snippets because it has no unmasker);
//   - a federated hit from another repo hydrates from that repo's local checkout
//     (the CWD repo, or one remembered in ~/.config/codastre/checkouts.json).
//
// It also returns a repo_id → remote_url lookup, used to backfill GRAPH's repos
// map (which the server sends without URLs), and the CWD repo's repo_id — the one
// repo for which the CWD checkout is a sound hydration root (see
// mcpshim.Config.rootFor).
//
// Best-effort: on any error it returns a zero hydrationLookups and the proxy
// falls back to CWD-only, UnmaskPath-gated behavior. The snapshot is taken once
// at startup; repos registered mid-session won't hydrate until the next `serve`.
func federatedHydration(serverURL, apiKey, cwdRoot string) hydrationLookups {
	repos, err := unmask.ListRepos(serverURL, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list repos for federated snippet hydration: %v\n", err)
		return hydrationLookups{}
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

	return hydrationLookups{
		Scheme: func(repoID string) (string, bool) {
			r, ok := byID[repoID]
			if !ok {
				return "", false
			}
			return r.MaskingScheme, true
		},
		RootFor: func(repoID string) (string, bool) {
			r, ok := byID[repoID]
			if !ok {
				return "", false
			}
			if cwdRoot != "" && cwdURL != "" && r.RemoteURL == cwdURL {
				return cwdRoot, true // the CWD checkout, no registry lookup needed
			}
			return checkouts.Lookup(r.RemoteURL)
		},
		RemoteURL: func(repoID string) (string, bool) {
			r, ok := byID[repoID]
			if !ok || r.RemoteURL == "" {
				return "", false
			}
			return r.RemoteURL, true
		},
		CWDRepoID: cwdRepoID,
	}
}
