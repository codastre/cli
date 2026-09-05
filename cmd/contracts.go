package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codastre/cli/internal/mcpclient"
	"github.com/spf13/cobra"
)

var contractsCmd = &cobra.Command{
	Use:   "contracts",
	Short: "Report cross-repo contracts — APIs and topics with nothing on the other end",
	Long: `Report the cross-repo boundaries you can see, and which of them are orphans.

A contract is a canonical boundary between repos: an HTTP route
(http::GET::/users/{}) or a Kafka topic (topic::kafka::orders), with the repos
that EXPOSE it and the repos that USE it. Status falls out of which sides are
populated:

  orphan_exposer  exposed, nothing indexed uses it — a route no client calls,
                  a topic nobody subscribes to
  orphan_user     used, nothing indexed exposes it — often a missing repo
  matched         exposed in one repo, used from another (the wired case)
  internal        both sides present but all in one repo — not cross-repo
  quarantined     every party is test/fixture/vendored/generated code

Contracts are DERIVED at read time from extracted endpoints — there is no
contract index to go stale, and no re-index is needed.

Default is the orphan report: with no --status you get orphan_exposer and
orphan_user only. 'counts' covers all five statuses either way, so the orphan
totals are always readable against the whole.

READ THE SCOPE LINE FIRST. A report over a single repo finds no matches no
matter how well the boundaries line up — every contract is an orphan by
construction. The scope line and its warnings (single_repo_scope,
endpoints_in_one_repo, no_endpoints, truncated) say when the scope, not the
code, produced an orphan-heavy answer. An empty list with a warning is a
scoping problem, not a clean bill of health.

Always federated: it reports across every repo you can see, which is the point.
--repo narrows that; it can only shrink what you already see.

Paths: parties carry path_tokens (cleartext unless the repo uses HMAC masking).
Human output unmasks them best-effort by hashing a local checkout's files —
the CWD repo, or one you point at with --repo-path. A report spans repos, so
tokens from repos you have no checkout of stay masked; --json is always masked.

Examples:
  codastre contracts                                  # the orphan report
  codastre contracts --kind kafka                     # unwired topics only
  codastre contracts --status matched --status internal
  codastre contracts --format agent
  codastre contracts --json | jq '.counts'`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runContracts,
}

var (
	contractsServerURL string
	contractsKey       string
	contractsStatus    []string
	contractsKind      []string
	contractsRepo      []string
	contractsJSON      bool
	contractsFormat    string
	contractsNoUnmask  bool
	contractsRepoPath  string
)

func init() {
	f := contractsCmd.Flags()
	f.StringVar(&contractsServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	f.StringVar(&contractsKey, "key", "", "API key (overrides $CODASTRE_API_KEY and keychain)")
	f.StringSliceVar(&contractsStatus, "status", nil,
		"Statuses to list: matched | internal | orphan_exposer | orphan_user | quarantined (repeatable; default: orphans)")
	f.StringSliceVar(&contractsKind, "kind", nil, "Contract kinds: http | kafka (repeatable; default: all)")
	f.StringSliceVar(&contractsRepo, "repo", nil, "Narrow to these repo UUIDs (repeatable; can only shrink ACL scope)")
	f.BoolVar(&contractsJSON, "json", false, "Emit the raw JSON envelope instead of human output")
	f.StringVar(&contractsFormat, "format", "human", "Output format: human | json | agent (compact text for agents)")
	f.BoolVar(&contractsNoUnmask, "no-unmask", false, "Show raw masked path_tokens in party locations")
	f.StringVar(&contractsRepoPath, "repo-path", "", "Local checkout to unmask party paths against")
	rootCmd.AddCommand(contractsCmd)
}

func runContracts(cmd *cobra.Command, _ []string) error {
	format, err := resolveFormat(contractsJSON, contractsFormat)
	if err != nil {
		return err
	}

	apiKey, warn, err := resolveAPIKey(contractsServerURL, contractsKey)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}

	// The server hard-caps at 10 s; the same margin for transport as query.
	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	cfg := mcpclient.Config{ServerURL: contractsServerURL, APIKey: apiKey}
	// No error-hint wrapper here: CONTRACTS is a pure Postgres read, so it never
	// returns RETRIEVAL_UNAVAILABLE. The one tool error it can return is
	// INVALID_REQUEST, whose `detail` names the bad value — ToolError.Error()
	// already prints it verbatim, and rewording it would only blur it.
	payload, err := mcpclient.Call(ctx, cfg, "CONTRACTS", contractsToolArgs())
	if err != nil {
		return err
	}

	if format == formatJSON {
		return printJSON(cmd.OutOrStdout(), payload)
	}

	var unmask func(pathToken string, maskKeyRev int) (string, bool)
	if !contractsNoUnmask {
		// A contract report spans repos by nature, so the CWD checkout (or
		// --repo-path) is the only knowable source of candidate paths — same
		// best-effort contract as `corpora` and `query --all`.
		unmask = resolveUnmask(cmd.ErrOrStderr(), target{federated: true}, contractsRepoPath, contractsServerURL, apiKey)
	}
	return renderContracts(cmd.OutOrStdout(), payload, unmask, format == formatAgent)
}

// contractsToolArgs builds the CONTRACTS arguments from the flag values. Every
// filter is omitted when unset rather than sent empty: the server's own default
// for absent `status` is the orphan report, and an empty list would ask for
// nothing at all.
func contractsToolArgs() map[string]any {
	args := map[string]any{}
	if len(contractsStatus) > 0 {
		args["status"] = contractsStatus
	}
	if len(contractsKind) > 0 {
		args["kind"] = contractsKind
	}
	if len(contractsRepo) > 0 {
		args["repo"] = contractsRepo
	}
	return args
}

// ── envelope ────────────────────────────────────────────────────────────────

type contractsEnvelope struct {
	Status    string                  `json:"status"`
	Scope     contractScope           `json:"scope"`
	Counts    map[string]int          `json:"counts"`
	Total     int                     `json:"total"`
	Contracts []contractEntry         `json:"contracts"`
	Repos     map[string]contractRepo `json:"repos"`
}

type contractScope struct {
	VisibleRepos       int      `json:"visible_repos"`
	ReposWithEndpoints int      `json:"repos_with_endpoints"`
	EndpointsRead      int      `json:"endpoints_read"`
	CrossRepoPossible  bool     `json:"cross_repo_possible"`
	Warnings           []string `json:"warnings"`
}

type contractEntry struct {
	ContractID string          `json:"contract_id"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Method     *string         `json:"method"`
	Status     string          `json:"status"`
	Exposers   []contractParty `json:"exposers"`
	Users      []contractParty `json:"users"`
}

type contractParty struct {
	RepoID      string  `json:"repo_id"`
	ChunkID     string  `json:"chunk_id"`
	PathToken   string  `json:"path_token"`
	Line        int     `json:"line"`
	Role        string  `json:"role"`
	Confidence  float64 `json:"confidence"`
	PathClass   *string `json:"path_class"`
	Host        *string `json:"host"`
	Quarantined bool    `json:"quarantined"`
}

type contractRepo struct {
	MaskingScheme string `json:"masking_scheme"`
	MaskKeyRev    int    `json:"mask_key_rev"`
}

func decodeContracts(payload json.RawMessage) (contractsEnvelope, bool) {
	var env contractsEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return contractsEnvelope{}, false
	}
	return env, true
}
