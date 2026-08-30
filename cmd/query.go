package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codastre/cli/internal/mcpclient"
	"github.com/codastre/cli/internal/mcpshim"
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
  • output is ranked locations by default. --snippets additionally reads each
    hit's body from your local checkout and prints it under the location with
    real file line numbers; --max-snippet-lines N caps long ones. The server
    never sends source — it only says where to look — so a hit from a repo you
    have no clone of shows a short reason instead of a body. --json is never
    hydrated.

Auth resolves in order: --key, $CODASTRE_API_KEY, then the OS keychain / file
fallback populated by 'codastre login'.

Examples:
  codastre query "where are masking keys rotated"          # this repo
  codastre query "stripe webhook handler" --all            # all visible repos
  codastre query "auth middleware" --repo-url github.com/acme/api
  codastre query "retry policy" --index-id 1f2e... --top-k 20
  codastre query "consumers lagging" --content-kinds runbook            # find the runbook
  codastre query "page fired" --alert-ids KAFKA-1024 --content-kinds runbook  # exact alert lookup
  codastre query "parse config" --language go --format agent  # compact text for agents
  codastre query "recall service" --snippets                  # print the bodies too`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runQuery,
}

var (
	queryServerURL       string
	queryKey             string
	queryIndexID         string
	queryRepoURL         string
	queryAll             bool
	queryRef             string
	queryTopK            int
	queryLanguage        string
	queryPathPrefix      string
	queryContentKinds    []string
	queryAlertIDs        []string
	queryErrorCodes      []string
	queryJSON            bool
	queryFormat          string
	queryNoUnmask        bool
	queryRepoPath        string
	querySnippets        bool
	queryMaxSnippetLines int
	queryCorpora         bool
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
	f.StringVar(&queryFormat, "format", "human", "Output format: human | json | agent (compact text for agents)")
	f.BoolVar(&queryNoUnmask, "no-unmask", false, "Show raw masked path_tokens; skip unmasking to real paths")
	f.StringVar(&queryRepoPath, "repo-path", "", "Local checkout of the queried repo to unmask against and hydrate from (when run outside it)")
	f.BoolVar(&querySnippets, "snippets", defaultQuerySnippets(),
		"Read each hit's body from your local checkout and print it under the "+
			"location [$CODASTRE_QUERY_SNIPPETS]")
	f.IntVar(&queryMaxSnippetLines, "max-snippet-lines", defaultMaxSnippetLines(),
		"With --snippets, cap each body at N lines; truncated ones say where the "+
			"rest is (0 = built-in default) [$CODASTRE_MAX_SNIPPET_LINES]")
	// Discovery-shaped alias. It lives on `query` because that is the command a
	// caller reaches for first, and the whole failure this addresses is not
	// knowing that chunk ranking was the wrong unit for the question being
	// asked — a caller who already knew would have typed `codastre corpora`.
	f.BoolVar(&queryCorpora, "corpora", false,
		"Rank CORPORA instead of chunks — same as 'codastre corpora'. Use for "+
			"ticket prose or a name, when the question is what to open. Always "+
			"federated, so it rejects --index-id/--repo-url/--all and the "+
			"per-result filters rather than ignoring them")
	rootCmd.AddCommand(queryCmd)
}

// corporaFlagConflicts partitions the `query` flags that the corpus path does
// not implement into the ones that must fail and the ones worth a warning.
//
// The split is by consequence, not by taste: a flag that would have changed
// WHICH corpora rank is an error, because dropping it silently returns a
// confident answer to a different question. A flag that only affects how the
// results are printed is a warning — the ranking is still the one that was
// asked for.
func corporaFlagConflicts(changed func(string) bool) (errs []string, warns []string) {
	// Target selection: corpus ranking surveys everything visible by design.
	// Scoping it to one repo is not a narrower version of the question, it is a
	// different question — that is what `codastre query` already answers.
	for _, f := range []string{"index-id", "repo-url", "all"} {
		if changed(f) {
			errs = append(errs, "--"+f)
		}
	}
	// Result filters with no CORPUS_SEARCH equivalent. They would have excluded
	// evidence, so ignoring them changes the ranking.
	for _, f := range []string{"ref", "path-prefix", "alert-ids", "error-codes"} {
		if changed(f) {
			errs = append(errs, "--"+f)
		}
	}
	// Presentation only: a corpus answer is locations, never bodies.
	for _, f := range []string{"snippets", "max-snippet-lines"} {
		if changed(f) {
			warns = append(warns, "--"+f)
		}
	}
	return errs, warns
}

func runQuery(cmd *cobra.Command, args []string) error {
	if queryCorpora {
		// Flags that do not exist on the corpus path are rejected or reported,
		// never silently dropped. `--corpora --repo-url X` reads as "rank corpora
		// within this repo", which is not a question this mode can answer, and
		// honouring neither half of it quietly is how a caller ends up trusting
		// an answer to a question they did not ask.
		bad, ignored := corporaFlagConflicts(cmd.Flags().Changed)
		if len(bad) > 0 {
			return fmt.Errorf("--corpora cannot be combined with %s: corpus ranking "+
				"is always federated and takes no per-result filters — drop the flag, "+
				"or run the query without --corpora", strings.Join(bad, ", "))
		}
		for _, f := range ignored {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %s has no effect with --corpora (a corpus answer lists "+
					"locations, not bodies)\n", f)
		}

		// Carry over the flags the two commands share, so the alias behaves as
		// the flag it looks like rather than as a differently-configured command.
		corporaServerURL, corporaKey = queryServerURL, queryKey
		corporaLanguage, corporaContentKinds = queryLanguage, queryContentKinds
		corporaJSON, corporaFormat = queryJSON, queryFormat
		corporaNoUnmask, corporaRepoPath = queryNoUnmask, queryRepoPath
		// query's --top-k default is 6, tuned for chunk bodies; a corpus list is
		// one line plus a few locations per entry. Only an explicit value carries.
		corporaTopK = 10
		if cmd.Flags().Changed("top-k") {
			corporaTopK = queryTopK
		}
		return runCorpora(cmd, args)
	}

	format, err := resolveFormat(queryJSON, queryFormat)
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
	if wire, ok := wireFormatFor(format); ok {
		toolArgs["format"] = wire
	}

	// Server caps QUERY at 10s; allow margin for transport.
	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	cfg := mcpclient.Config{ServerURL: queryServerURL, APIKey: apiKey}
	payload, tgt, err := callWithRepoFallback(ctx, cfg, "QUERY", toolArgs, tgt)
	if err != nil {
		return queryErrorHint(err)
	}

	if format == formatJSON {
		// --json is a raw passthrough of the server envelope (path_tokens intact),
		// so agents that parse it can unmask themselves via the proxy or keychain.
		return printJSON(cmd.OutOrStdout(), payload)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "target: %s\n", tgt.describe())
	var unmask func(pathToken string, maskKeyRev int) (string, bool)
	if !queryNoUnmask {
		unmask = resolveUnmask(cmd.ErrOrStderr(), tgt, queryRepoPath, queryServerURL, apiKey)
	}
	// Hydration, on request. This command talks to the server directly, so no
	// proxy has enriched the envelope — the server ships (path_token, span,
	// score) and nothing else, because it never holds the source. Reading the
	// spans here is what turns a list of coordinates into an answer, and
	// --snippets is what asks for it: the default output is a ranked list to
	// scan, and six bodies is six screenfuls.
	//
	// --no-unmask suppresses it even when asked, and not as a shortcut:
	// unmasking is HOW a token becomes a path that can be opened. Hydrating
	// anyway would report "path_unmask_failed" on every hit of an hmac repo — a
	// failure to repair, when it was the caller's own choice.
	noSnippets := !querySnippets || queryNoUnmask
	if !noSnippets {
		payload = hydrateQuery(payload, tgt, apiKey, unmask)
	}

	if format == formatAgent {
		text, ok := mcpshim.RenderQueryText(payload, mcpshim.RenderOptions{
			NoSnippets: noSnippets,
			// Still passed: hydration writes real_path only where it could
			// resolve one, and the renderer unmasks what is left over.
			Unmask: unmask,
		})
		if !ok {
			return fmt.Errorf("decode QUERY response for --format agent")
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), text)
		return err
	}
	return renderQueryHuman(cmd.OutOrStdout(), payload, unmask, noSnippets)
}

// hydrateQuery reads each result's line span from a local checkout, returning
// the enriched envelope. Best-effort by construction: a repo with no checkout
// here keeps its ranked location and gains a `hydration` reason saying why there
// is no body.
func hydrateQuery(
	payload json.RawMessage,
	tgt target,
	apiKey string,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
) json.RawMessage {
	repoRoot, _ := findGitRoot(".")
	hy := queryHydration(queryServerURL, apiKey, queryRepoPath, tgt)
	return mcpshim.HydrateQueryPayload(mcpshim.Config{
		RepoRoot:        repoRoot,
		UnmaskPath:      unmask,
		RepoScheme:      hy.Scheme,
		RepoRootFor:     hy.RootFor,
		CWDRepoID:       hy.CWDRepoID,
		MaxSnippetLines: queryMaxSnippetLines,
	}, payload)
}

// queryHydration builds the per-repo scheme/root lookups for a one-shot query,
// layering --repo-path on top of the CWD-and-registry roots `serve` uses.
//
// --repo-path is the one checkout the registry cannot know about, and this
// command accepts it precisely for the case of running outside the target repo.
// It is validated the same way unmasking validates it — a directory that is not
// a clone of the target is ignored rather than joined onto that repo's paths,
// which would read a real but wrong file from a correct-looking path. The
// mismatch goes unwarned here only because resolveUnmask has already said it.
func queryHydration(serverURL, apiKey, repoPath string, tgt target) hydrationLookups {
	cwdRoot, _ := findGitRoot(".")
	hy := federatedHydration(serverURL, apiKey, cwdRoot)
	if repoPath == "" || tgt.repoURL == "" || hy.RemoteURL == nil {
		return hy
	}
	if !dirMatchesRepo(repoPath, tgt.repoURL) {
		return hy
	}
	remoteURL, registryRoot := hy.RemoteURL, hy.RootFor
	hy.RootFor = func(repoID string) (string, bool) {
		if url, ok := remoteURL(repoID); ok && url == tgt.repoURL {
			return repoPath, true
		}
		if registryRoot == nil {
			return "", false
		}
		return registryRoot(repoID)
	}
	return hy
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

// Output formats for `codastre query` and `codastre graph`. `agent` is a third
// value of the existing --format enum rather than a new mechanism: the same
// rendering the MCP proxy emits (mcpshim/render.go, render_graph.go), so a
// person and an agent reading the CLI see the same two shapes the MCP path
// offers.
const (
	formatHuman = "human"
	formatJSON  = "json"
	formatAgent = "agent"
)

// wireFormatFor maps an output format to the shape to ask the server for, and
// reports whether to send one at all.
//
// This is the server-side rung of the compaction ladder the MCP proxy already
// forwards (mcpshim/overrides.go). This command talks to the server directly,
// so nothing forwards it on our behalf: without this the most compact output
// the CLI can print is still rendered from the most verbose envelope the server
// can send.
//
// Only `agent` opts in, and the reason is per-format, not caution:
//
//   - `json` is documented as a raw passthrough of the server envelope. A
//     caller parsing it chose the unabridged shape.
//   - `human` prints content_kind unconditionally (render.go), and compact
//     omits it at its default value — the output would read "()".
//   - `agent` reads nothing compact drops. Its `seed:` tag fires only where
//     symbol_name is absent, which is exactly where the server keeps chunk_id,
//     and it prints content_kind/path_class only at non-default values. The
//     two conditions are mirror images by construction.
//
// A server predating the parameter ignores it rather than rejecting it
// (verified against production), so no version negotiation is needed.
func wireFormatFor(format string) (string, bool) {
	if format == formatAgent {
		return mcpshim.WireFormatCompact, true
	}
	return "", false
}

// resolveFormat resolves the --json / --format flags into one format, for every
// command that renders. It is tri-state because a bool cannot express three: the
// legacy --json bool folds in as a synonym for --format json when no explicit
// format was given.
func resolveFormat(jsonFlag bool, format string) (string, error) {
	switch format {
	case formatJSON, formatAgent:
		return format, nil
	case formatHuman, "":
		if jsonFlag {
			return formatJSON, nil
		}
		return formatHuman, nil
	default:
		return "", fmt.Errorf("invalid --format %q: want human, json or agent", format)
	}
}
