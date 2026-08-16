package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codastre/cli/internal/mcpclient"
	"github.com/spf13/cobra"
)

var queryCmd = &cobra.Command{
	Use:   "query <text>",
	Short: "Hybrid semantic + lexical code search (no MCP connection required)",
	Long: `Search indexed repositories with a single request to the codastre server.

This is the CLI equivalent of the MCP QUERY tool. What it searches depends on
the flags you pass and your current directory:

  --index-id <uuid>   search one specific index
  --repo-url <url>    search the latest ready index for that repo
  --all               search every repo you can see, merged

With no target flag:
  • inside a git repo → search THAT repo, resolved from its remotes (origin
    first, then upstream, then the rest); if the first isn't indexed the next
    is tried, so a clone whose 'origin' is unregistered still resolves.
  • otherwise (no repo / no remotes) → search every repo you can see

Reading the output:
  • status "ok" with an empty result list means "searched, nothing matched" —
    that is a valid answer, not an error (exit 0).
  • error RETRIEVAL_UNAVAILABLE means the data plane is down; fall back to grep.
  • error REPO_NOT_INDEXED means the repo has never been registered; register it
    over MCP (REGISTER) first.
  • freshness is the worst case across searched repos: fresh | syncing | degraded.
  • each result carries repo_id; the 'repos' map (in --json) resolves it to a
    remote_url. Paths are returned as path_token (cleartext unless the repo uses
    HMAC masking). For an HMAC-masked repo, human output unmasks tokens back to
    real paths by hashing a local checkout's files with the masking key (HMAC is
    one-way, so the key alone can't reverse a token — real paths are needed).
    When you run outside the queried repo, codastre uses a checkout remembered
    from a previous in-repo run, or one you point at with --repo-path <dir>.
    Best-effort: tokens for files not in that checkout stay masked. Pass
    --no-unmask to skip it, or --json for the raw envelope (always masked).

Auth resolves in order: --key, $CODASTRE_API_KEY, then the OS keychain / file
fallback populated by 'codastre login'.

Examples:
  codastre query "where are masking keys rotated"          # this repo
  codastre query "stripe webhook handler" --all            # all visible repos
  codastre query "auth middleware" --repo-url github.com/acme/api
  codastre query "retry policy" --index-id 1f2e... --top-k 20
  codastre query "consumers lagging" --content-kinds runbook            # find the runbook
  codastre query "page fired" --alert-ids KAFKA-1024 --content-kinds runbook  # exact alert lookup
  codastre query "parse config" --language go --json       # raw envelope for agents`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runQuery,
}

var (
	queryServerURL    string
	queryKey          string
	queryIndexID      string
	queryRepoURL      string
	queryAll          bool
	queryRef          string
	queryTopK         int
	queryLanguage     string
	queryPathPrefix   string
	queryContentKinds []string
	queryAlertIDs     []string
	queryErrorCodes   []string
	queryJSON         bool
	queryFormat       string
	queryNoUnmask     bool
	queryRepoPath     string
)

func init() {
	f := queryCmd.Flags()
	f.StringVar(&queryServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	f.StringVar(&queryKey, "key", "", "API key (overrides $CODASTRE_API_KEY and keychain)")
	f.StringVar(&queryIndexID, "index-id", "", "Search a single index by id")
	f.StringVar(&queryRepoURL, "repo-url", "", "Search a specific repo by URL")
	f.BoolVar(&queryAll, "all", false, "Search across all visible repos")
	f.StringVar(&queryRef, "ref", "", "Branch/ref to include its overlay (default: base only)")
	// 6, not the tool default of 10 (docs/bugs/query-defaults-token-budget.md, C4).
	// Measured on a lookup question, every correct answer came from ranks 1-3 and
	// ranks 4-10 were 70% of the payload and 0% of the answers. 6 rather than 4
	// keeps a recall margin for vaguer questions, where a second query would erase
	// the saving. Raise it (or pass --all-style survey flags) for exploration.
	f.IntVar(&queryTopK, "top-k", 6, "Maximum results (1-50)")
	f.StringVar(&queryLanguage, "language", "", "Filter by language")
	f.StringVar(&queryPathPrefix, "path-prefix", "", "Filter by path prefix")
	f.StringSliceVar(&queryContentKinds, "content-kinds", nil, "Filter by content kinds (repeatable)")
	f.StringSliceVar(&queryAlertIDs, "alert-ids", nil, "Exact-match runbooks carrying these alert ids, e.g. KAFKA-1024 (repeatable)")
	f.StringSliceVar(&queryErrorCodes, "error-codes", nil, "Exact-match runbooks carrying these error codes, e.g. ERR_CONSUMER_LAG (repeatable)")
	f.BoolVar(&queryJSON, "json", false, "Emit the raw JSON envelope instead of human output")
	f.StringVar(&queryFormat, "format", "human", "Output format: human | json")
	f.BoolVar(&queryNoUnmask, "no-unmask", false, "Show raw masked path_tokens; skip unmasking to real paths")
	f.StringVar(&queryRepoPath, "repo-path", "", "Local checkout of the queried repo to unmask against (when run outside it)")
	rootCmd.AddCommand(queryCmd)
}

func runQuery(cmd *cobra.Command, args []string) error {
	asJSON, err := wantJSON(queryJSON, queryFormat)
	if err != nil {
		return err
	}

	tgt, err := resolveTarget(queryIndexID, queryRepoURL, queryAll)
	if err != nil {
		return err
	}

	apiKey, warn, err := resolveAPIKey(queryServerURL, queryKey)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}

	toolArgs := map[string]any{"query_text": args[0], "top_k": queryTopK}
	if queryRef != "" {
		toolArgs["ref"] = queryRef
	}
	if queryLanguage != "" {
		toolArgs["language"] = queryLanguage
	}
	if queryPathPrefix != "" {
		toolArgs["path_prefix"] = queryPathPrefix
	}
	if len(queryContentKinds) > 0 {
		toolArgs["content_kinds"] = queryContentKinds
	}
	if len(queryAlertIDs) > 0 {
		toolArgs["alert_ids"] = queryAlertIDs
	}
	if len(queryErrorCodes) > 0 {
		toolArgs["error_codes"] = queryErrorCodes
	}

	// Server caps QUERY at 10s; allow margin for transport.
	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	cfg := mcpclient.Config{ServerURL: queryServerURL, APIKey: apiKey}
	payload, tgt, err := callWithRepoFallback(ctx, cfg, "QUERY", toolArgs, tgt)
	if err != nil {
		return queryErrorHint(err)
	}

	if asJSON {
		// --json is a raw passthrough of the server envelope (path_tokens intact),
		// so agents that parse it can unmask themselves via the proxy or keychain.
		return printJSON(cmd.OutOrStdout(), payload)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "target: %s\n", tgt.describe())
	var unmask func(pathToken string, maskKeyRev int) (string, bool)
	if !queryNoUnmask {
		unmask = resolveUnmask(cmd.ErrOrStderr(), tgt, queryRepoPath, queryServerURL, apiKey)
	}
	return renderQueryHuman(cmd.OutOrStdout(), payload, unmask)
}

// queryErrorHint augments known tool errors with an actionable next step.
func queryErrorHint(err error) error {
	var te *mcpclient.ToolError
	if errors.As(err, &te) {
		switch te.Code {
		case "REPO_NOT_INDEXED":
			return fmt.Errorf("%w — this repo is not indexed; register it over MCP (REGISTER) first, or use --all to search other repos", te)
		case "RETRIEVAL_UNAVAILABLE":
			return fmt.Errorf("%w — the search backend is down; fall back to local grep", te)
		case "INDEX_BUILDING":
			return fmt.Errorf("%w — the index is still building; retry shortly", te)
		}
	}
	return err
}

// wantJSON resolves the --json / --format flags into a single boolean.
func wantJSON(jsonFlag bool, format string) (bool, error) {
	switch format {
	case "json":
		return true, nil
	case "human", "":
		return jsonFlag, nil
	default:
		return false, fmt.Errorf("invalid --format %q: want human or json", format)
	}
}
