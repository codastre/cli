package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codastre/cli/internal/mcpclient"
	"github.com/spf13/cobra"
)

var graphCmd = &cobra.Command{
	Use:   "graph <chunk_or_symbol>",
	Short: "Traverse the cross-repo relationship graph from a symbol or chunk",
	Long: `Walk the codastre knowledge graph from a seed, in a single server request.

This is the CLI equivalent of the MCP GRAPH tool. The seed is a symbol name
(e.g. a function or class) or a chunk_id. What it traverses matches 'query':

  --index-id <uuid>   traverse within one index
  --repo-url <url>    traverse the latest ready index for that repo
  --all               seed the symbol across all visible repos

With no target flag:
  • inside a git repo with an 'origin' remote → traverse THAT repo
  • otherwise                                  → seed across all visible repos

Direction:
  --direction outbound  edges FROM the seed ("what does A call / produce")
  --direction inbound   edges INTO the seed ("what calls A / consumes topic T")

Edge kinds (--kind): kafka, http, grpc, package, calls (omit for all). Prefer
--depth 1 for kind=calls — busy functions fan out fast.

Reading confidence:
  • cross-repo edges (kafka/http/grpc/package): resolution matters —
    'dynamic_unresolved' edges and confidence < 0.5 are hypotheses, not facts.
  • intra-repo 'calls' edges are always resolution=heuristic; trust the graded
    confidence (>=0.9 near-certain, 0.5-0.9 plausible, <0.5 weak).
Human output flags hypotheses with [hypothesis]; use --json for raw edges.

Paths: src/dst are path_tokens (cleartext unless the repo uses HMAC masking).
For an HMAC-masked repo, human output unmasks them back to real paths using the
local checkout + masking key (best-effort; tokens for files not checked out
locally stay masked). Pass --no-unmask to skip it, or --json for raw edges
(always masked).

Examples:
  codastre graph processPayment                         # this repo, outbound, depth 1
  codastre graph processPayment --direction inbound     # who calls it
  codastre graph OrderCreated --kind kafka --all        # cross-repo producers/consumers
  codastre graph chargeCard --kind calls --depth 2 --json`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runGraph,
}

var (
	graphServerURL string
	graphKey       string
	graphIndexID   string
	graphRepoURL   string
	graphAll       bool
	graphKind      string
	graphDepth     int
	graphDirection string
	graphJSON      bool
	graphFormat    string
	graphNoUnmask  bool
)

func init() {
	f := graphCmd.Flags()
	f.StringVar(&graphServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	f.StringVar(&graphKey, "key", "", "API key (overrides $CODASTRE_API_KEY and keychain)")
	f.StringVar(&graphIndexID, "index-id", "", "Traverse a single index by id")
	f.StringVar(&graphRepoURL, "repo-url", "", "Traverse a specific repo by URL")
	f.BoolVar(&graphAll, "all", false, "Traverse across all visible repos")
	f.StringVar(&graphKind, "kind", "", "Edge kind: kafka | http | grpc | package | calls (default all)")
	f.IntVar(&graphDepth, "depth", 1, "Traversal depth (1-3)")
	f.StringVar(&graphDirection, "direction", "outbound", "Traversal direction: outbound | inbound")
	f.BoolVar(&graphJSON, "json", false, "Emit the raw JSON envelope instead of human output")
	f.StringVar(&graphFormat, "format", "human", "Output format: human | json")
	f.BoolVar(&graphNoUnmask, "no-unmask", false, "Show raw masked path_tokens; skip unmasking to real paths")
	rootCmd.AddCommand(graphCmd)
}

func runGraph(cmd *cobra.Command, args []string) error {
	asJSON, err := wantJSON(graphJSON, graphFormat)
	if err != nil {
		return err
	}

	tgt, err := resolveTarget(graphIndexID, graphRepoURL, graphAll)
	if err != nil {
		return err
	}

	apiKey, warn, err := resolveAPIKey(graphServerURL, graphKey)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}

	toolArgs := map[string]any{
		"chunk_or_symbol": args[0],
		"depth":           graphDepth,
		"direction":       graphDirection,
	}
	tgt.apply(toolArgs)
	if graphKind != "" {
		toolArgs["kind"] = graphKind
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	payload, err := mcpclient.Call(ctx, mcpclient.Config{ServerURL: graphServerURL, APIKey: apiKey}, "GRAPH", toolArgs)
	if err != nil {
		return graphErrorHint(err)
	}

	if asJSON {
		return printJSON(cmd.OutOrStdout(), payload)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "target: %s\n", tgt.describe())
	var unmask func(pathToken string, maskKeyRev int) (string, bool)
	if !graphNoUnmask {
		unmask = cwdUnmask(graphServerURL, apiKey)
	}
	return renderGraphHuman(cmd.OutOrStdout(), payload, unmask)
}

// graphErrorHint augments known tool errors with an actionable next step.
func graphErrorHint(err error) error {
	var te *mcpclient.ToolError
	if errors.As(err, &te) {
		switch te.Code {
		case "REPO_NOT_INDEXED":
			return fmt.Errorf("%w — this repo is not indexed; register it over MCP (REGISTER) first, or use --all", te)
		case "GRAPH_UNAVAILABLE", "GRAPH_BUILDING":
			return fmt.Errorf("%w — the graph is not ready yet; retry shortly", te)
		}
	}
	return err
}
