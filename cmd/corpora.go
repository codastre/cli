package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/codastre/cli/internal/mcpclient"
	"github.com/spf13/cobra"
)

var corporaCmd = &cobra.Command{
	Use:     "corpora <text>",
	Aliases: []string{"corpus"},
	Short:   "Rank CORPORA for free-form text — what should I open?",
	Long: `Rank corpora (repositories, document sets) for a ticket, incident, or description.

A corpus is a searchable unit. A git repository is one kind; a document set —
runbooks, docs — is another. Each result says which kind it is.

This answers a different question from 'codastre query'. Query ranks chunks:
you get the best-matching code, and you read the corpus off the hits. That
works when you already speak the code's vocabulary. It fails on ticket prose,
where the repos that literally contain words like "customer", "declined" and
"reported" are support tooling — chatbots, helpdesk connectors — not the
service that implements the behaviour. One lucky chunk there outranks a corpus
with twenty moderately-matching ones, because nothing aggregates per corpus.

'codastre corpora' aggregates. A corpus's score combines:
  • identity — does the query name it (its name, description, topics, README
    and shape, indexed as a repo card)
  • evidence — how many of its chunks matched, and how high they ranked
  • diversity — across how many distinct files

Reading the output:
  • each entry lists 'why': the specific files that put it there. Open those, or
    pass a git corpus's URL to 'codastre query --repo-url <url>' to search inside.
  • card high, evidence low  → the query NAMES this corpus.
  • evidence high, card low  → this corpus CONTAINS what you described.
  • "no card" means it has no identity document yet and placed on body matches
    alone — weaker evidence, and worth a reindex.
  • scores rank corpora within one answer only. They carry position, not
    similarity, so they are not comparable between two runs, and a top result is
    not by itself proof that anything fits.
  • status "ok" with nothing listed means "searched, nothing matched" (exit 0).

Always federated: it ranks across everything you can see, which is the point.

Examples:
  codastre corpora "customers report their virtual card was declined at checkout"
  codastre corpora "public api documentation"
  codastre corpora "$(gh issue view 4213 --json body -q .body)"`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runCorpora,
}

var (
	corporaServerURL    string
	corporaKey          string
	corporaTopK         int
	corporaLanguage     string
	corporaContentKinds []string
	corporaJSON         bool
	corporaFormat       string
	corporaNoUnmask     bool
	corporaRepoPath     string
)

func init() {
	f := corporaCmd.Flags()
	f.StringVar(&corporaServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	f.StringVar(&corporaKey, "key", "", "API key (overrides $CODASTRE_API_KEY and keychain)")
	// 10, not query's 6. A corpus list is one line plus a few locations per
	// entry, and the whole job is to survey what is out there — truncating it
	// early is how the right one gets missed, which is the failure this command
	// exists to fix.
	f.IntVar(&corporaTopK, "top-k", 10, "Maximum corpora to return (1-50)")
	f.StringVar(&corporaLanguage, "language", "", "Only draw evidence from this language")
	f.StringSliceVar(&corporaContentKinds, "content-kinds", nil, "Only draw evidence from these content kinds (repeatable)")
	f.BoolVar(&corporaJSON, "json", false, "Emit the raw JSON envelope instead of human output")
	f.StringVar(&corporaFormat, "format", "human", "Output format: human | json | agent (compact text for agents)")
	f.BoolVar(&corporaNoUnmask, "no-unmask", false, "Show raw masked path_tokens in the evidence list")
	f.StringVar(&corporaRepoPath, "repo-path", "", "Local checkout to unmask evidence paths against")
	rootCmd.AddCommand(corporaCmd)
}

func runCorpora(cmd *cobra.Command, args []string) error {
	format, err := resolveFormat(corporaJSON, corporaFormat)
	if err != nil {
		return err
	}

	apiKey, warn, err := resolveAPIKey(corporaServerURL, corporaKey)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
	}

	toolArgs := map[string]any{"query_text": args[0], "top_k": corporaTopK}
	if corporaLanguage != "" {
		toolArgs["language"] = corporaLanguage
	}
	if len(corporaContentKinds) > 0 {
		toolArgs["content_kinds"] = corporaContentKinds
	}

	// Corpus ranking fans out deeper than chunk QUERY, but the server's 10 s
	// hard cap covers it too — same margin for transport as query.
	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()

	cfg := mcpclient.Config{ServerURL: corporaServerURL, APIKey: apiKey}
	payload, err := mcpclient.Call(ctx, cfg, "CORPUS_SEARCH", toolArgs)
	if err != nil {
		return corporaErrorHint(err)
	}

	if format == formatJSON {
		return printJSON(cmd.OutOrStdout(), payload)
	}

	var unmask func(pathToken string, maskKeyRev int) (string, bool)
	if !corporaNoUnmask {
		// Federated by construction, so the CWD checkout (or --repo-path) is the
		// only knowable source of candidate paths — same contract as `query --all`.
		unmask = resolveUnmask(cmd.ErrOrStderr(), target{federated: true}, corporaRepoPath, corporaServerURL, apiKey)
	}
	return renderCorpora(cmd.OutOrStdout(), cmd.ErrOrStderr(), payload, unmask, format == formatAgent)
}

// corporaErrorHint translates the one tool error worth expanding.
// RETRIEVAL_UNAVAILABLE is the difference between "nothing matched" and "the
// data plane is down", and a caller that reads the first for the second stops
// looking when it should retry.
func corporaErrorHint(err error) error {
	var te *mcpclient.ToolError
	if errors.As(err, &te) && te.Code == "RETRIEVAL_UNAVAILABLE" {
		return fmt.Errorf("retrieval unavailable: the search data plane is down, so "+
			"corpora could not be ranked — this is NOT 'nothing matched': %w", err)
	}
	return err
}

// ── envelope ────────────────────────────────────────────────────────────────

type corpusSearchEnvelope struct {
	Status              string         `json:"status"`
	Freshness           string         `json:"freshness"`
	SearchedCorpusCount int            `json:"searched_corpus_count"`
	MaskKeyRevs         map[string]int `json:"mask_key_revs"`
	Corpora             []corpusHit    `json:"corpora"`
}

type corpusHit struct {
	CorpusID  string      `json:"corpus_id"`
	Kind      string      `json:"kind"`
	Name      string      `json:"name"`
	Score     float64     `json:"score"`
	CardScore float64     `json:"card_score"`
	Evidence  float64     `json:"evidence_score"`
	Diversity float64     `json:"diversity_score"`
	FileCount int         `json:"file_count"`
	HasCard   bool        `json:"has_card"`
	Why       []corpusWhy `json:"why"`
}

type corpusWhy struct {
	RepoID     string  `json:"repo_id"`
	PathToken  string  `json:"path_token"`
	LineStart  int     `json:"line_start"`
	LineEnd    int     `json:"line_end"`
	SymbolName *string `json:"symbol_name"`
}

func decodeCorpusSearch(payload json.RawMessage) (corpusSearchEnvelope, bool) {
	var env corpusSearchEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return corpusSearchEnvelope{}, false
	}
	return env, true
}
