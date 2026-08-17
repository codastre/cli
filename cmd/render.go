package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/codastre/cli/internal/mcpshim"
)

// ── QUERY envelope ──────────────────────────────────────────────────────────

type queryEnvelope struct {
	Freshness     string   `json:"freshness"`
	SearchedRepos []string `json:"searched_repos"`
	// Federated responses scope the envelope to result repos and report the
	// searched count here instead of the full searched_repos list (API2).
	SearchedRepoCount *int                `json:"searched_repo_count"`
	Repos             map[string]repoInfo `json:"repos"`
	SyncJobID         *string             `json:"sync_job_id"`
	MaskKeyRev        int                 `json:"mask_key_rev"`
	Results           []queryResult       `json:"results"`
}

// searchedCount is the number of repos searched: the scoped federated count
// when present, else the length of the (single-repo / full) searched_repos list.
func (e queryEnvelope) searchedCount() int {
	if e.SearchedRepoCount != nil {
		return *e.SearchedRepoCount
	}
	return len(e.SearchedRepos)
}

type repoInfo struct {
	RemoteURL string `json:"remote_url"`
}

type queryResult struct {
	RepoID      string  `json:"repo_id"`
	PathToken   string  `json:"path_token"`
	LineStart   int     `json:"line_start"`
	LineEnd     int     `json:"line_end"`
	Score       float64 `json:"score"`
	Kind        string  `json:"kind"`
	SymbolName  *string `json:"symbol_name"`
	ContentKind string  `json:"content_kind"`
	// Document-corpus enrichment: a human title and the source ref (filename or
	// URL). Empty for code chunks. When present the human renderer leads with the
	// title instead of the opaque doc-id path_token.
	Title     *string `json:"title"`
	SourceRef *string `json:"source_ref"`

	// Written locally by mcpshim.HydrateQueryPayload, never by the server — it
	// holds no source. Absent on a run that could not hydrate (--no-snippets,
	// --no-unmask, no local checkout), in which case the renderer falls back to
	// the ranked-locations output this command emitted before hydration existed.
	RealPath         string `json:"real_path"`
	Snippet          string `json:"snippet"`
	SnippetTruncated bool   `json:"snippet_truncated"`
	SnippetLineEnd   int    `json:"snippet_line_end"`
	Stale            bool   `json:"stale"`
	Hydration        string `json:"hydration"`
}

// displayPath is what the reader should open: the real path hydration resolved,
// else a display-time unmask, else the raw token. Hydration wins because it read
// a file at that path — an unmask that disagrees with it would be the stale one.
func (r queryResult) displayPath(
	unmask func(pathToken string, maskKeyRev int) (string, bool),
	maskKeyRev int,
) string {
	if r.RealPath != "" {
		return r.RealPath
	}
	if unmask != nil {
		if real, ok := unmask(r.PathToken, maskKeyRev); ok {
			return real
		}
	}
	return r.PathToken
}

// repoLabel resolves a repo_id to its remote_url for display, falling back to
// the raw id when the repos map has no entry.
func (e queryEnvelope) repoLabel(repoID string) string {
	if r, ok := e.Repos[repoID]; ok && r.RemoteURL != "" {
		return r.RemoteURL
	}
	return repoID
}

// humanSnippetIndent lines the body's gutter up under the hit's detail line,
// which is itself indented four spaces from the numbered rank.
const humanSnippetIndent = "      "

// renderQueryHuman writes a human-readable QUERY result to w. When unmask is
// non-nil it converts each result's HMAC path_token back to the real path for
// display; an unmask miss (or nil unmask) falls back to showing the raw token.
//
// Hydrated results (mcpshim.HydrateQueryPayload, applied by runQuery before it
// gets here) carry their body, which is printed under the hit with real file
// line numbers. noSnippets says hydration was switched off for this run, so the
// header can explain the missing bodies once instead of once per hit.
func renderQueryHuman(
	w io.Writer,
	payload json.RawMessage,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
	noSnippets bool,
) error {
	var env queryEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("decode QUERY response: %w", err)
	}

	fmt.Fprintf(w, "freshness: %s · searched %d repo(s)", env.Freshness, env.searchedCount())
	if noSnippets {
		// Names the flag, because this is the default state: hydration is
		// otherwise invisible, and a feature nobody can find is not a feature.
		fmt.Fprint(w, " · locations only (--snippets for bodies)")
	}
	fmt.Fprintln(w)
	if env.SyncJobID != nil && *env.SyncJobID != "" {
		fmt.Fprintf(w, "sync in progress: job %s\n", *env.SyncJobID)
	}
	fmt.Fprintln(w)

	if len(env.Results) == 0 {
		fmt.Fprintln(w, "No matches.")
		return nil
	}

	// Bodies turn a two-line entry into a screenful, and consecutive screenfuls
	// run together without a break. Only inserted after an entry that actually
	// printed one, so a locations-only listing keeps its compact shape.
	spaced := false
	for i, r := range env.Results {
		if spaced {
			fmt.Fprintln(w)
		}
		path := r.displayPath(unmask, env.MaskKeyRev)
		fmt.Fprintf(w, "%2d. [%.3f] %s\n", i+1, r.Score, env.repoLabel(r.RepoID))

		// Document hit: lead with the title (the opaque path_token is the doc-id,
		// kept on a detail line as the handle for the content endpoint).
		if r.Title != nil && *r.Title != "" {
			label := *r.Title
			if r.SourceRef != nil && *r.SourceRef != "" && *r.SourceRef != *r.Title {
				label += "  [" + *r.SourceRef + "]"
			}
			fmt.Fprintf(w, "    %s  (%s)\n", label, r.ContentKind)
			fmt.Fprintf(w, "    doc %s\n", path)
		} else {
			// Code hit: path:lines and the symbol name.
			sym := ""
			if r.SymbolName != nil && *r.SymbolName != "" {
				sym = "  " + *r.SymbolName
			}
			fmt.Fprintf(w, "    %s:%d-%d%s  (%s)%s\n",
				path, r.LineStart, r.LineEnd, sym, r.ContentKind, staleNote(r))
		}
		spaced = writeHumanSnippet(w, r, noSnippets)
	}
	return nil
}

// restOfBody names the lines a truncated body left behind. A budget that stops
// one line short of the span is common enough that "lines 840-840" would be a
// regular sight, and it reads as a mistake — so the singular case carries its
// own verb rather than being patched onto a plural sentence.
func restOfBody(from, to int) string {
	if from >= to {
		return fmt.Sprintf("line %d has the rest", from)
	}
	return fmt.Sprintf("lines %d-%d have the rest", from, to)
}

// staleNote marks a body read from a file that has changed since it was indexed:
// the span is a lead, not a quote, and a reader who copies it as gospel is
// quoting a version that no longer exists.
func staleNote(r queryResult) string {
	if !r.Stale {
		return ""
	}
	return "  [stale — local file differs from the indexed blob]"
}

// writeHumanSnippet prints a hit's hydrated body, or the reason there is none,
// and reports whether it printed a body. The reason matters: an absent body is
// otherwise indistinguishable from "that repo isn't checked out here", "the file
// moved" and "the read failed", which have three different fixes.
func writeHumanSnippet(w io.Writer, r queryResult, noSnippets bool) bool {
	if r.Snippet != "" {
		last := mcpshim.WriteSnippetLines(w, humanSnippetIndent, r.Snippet, r.LineStart)
		if r.SnippetTruncated {
			if r.SnippetLineEnd != 0 {
				last = r.SnippetLineEnd
			}
			// Where the rest is, not merely that something is missing. The path
			// is two lines up, so the range alone is enough here — unlike the
			// agent rendering, which hands over a whole Read argument.
			fmt.Fprintf(w, "%s⋯ truncated · %s\n",
				humanSnippetIndent, restOfBody(last+1, r.LineEnd))
		}
		return true
	}
	// Every reason except the one the header already gave — repeating "snippets
	// off" under each hit restates the mode instead of saying anything.
	if r.Hydration != "" && !(noSnippets && r.Hydration == mcpshim.HydrationSnippetsDisabled) {
		fmt.Fprintf(w, "%sno body: %s\n", humanSnippetIndent, r.Hydration)
	}
	return false
}

// ── GRAPH envelope ──────────────────────────────────────────────────────────

type graphEnvelope struct {
	Edges []graphEdge `json:"edges"`
}

type graphEdge struct {
	Edge edgeMeta `json:"edge"`
	Src  edgeNode `json:"src"`
	Dst  edgeNode `json:"dst"`
	// Legacy item-level mirrors, emitted by servers predating the corpus-
	// hygiene API3 compaction. The edge object is canonical; these are only a
	// fallback so the CLI renders old servers' responses correctly.
	LegacyConfidence *float64 `json:"confidence"`
	LegacyResolution string   `json:"resolution"`
	Count            int      `json:"count"`
}

// confidence returns the canonical edge.confidence, falling back to the legacy
// item-level mirror (pre-API3 servers). Reading only the removed mirror is the
// bug that rendered every edge as "conf 0.00 [hypothesis]".
func (e graphEdge) confidence() float64 {
	if e.Edge.Confidence != nil {
		return *e.Edge.Confidence
	}
	if e.LegacyConfidence != nil {
		return *e.LegacyConfidence
	}
	return 0
}

// resolution returns the canonical edge.resolution, falling back to the legacy
// item-level mirror.
func (e graphEdge) resolution() string {
	if e.Edge.Resolution != "" {
		return e.Edge.Resolution
	}
	return e.LegacyResolution
}

type edgeMeta struct {
	Kind       string   `json:"kind"`
	Confidence *float64 `json:"confidence"`
	Resolution string   `json:"resolution"`
}

type edgeNode struct {
	PathToken string `json:"path_token"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

// ref renders the node as path:lines. When unmask is non-nil it converts the
// HMAC path_token back to the real path; an unmask miss (or nil unmask) falls
// back to showing the raw token. GRAPH responses carry no top-level mask_key_rev,
// so rev 0 is passed — the resolver scans all loaded revisions for a match.
func (n edgeNode) ref(unmask func(pathToken string, maskKeyRev int) (string, bool)) string {
	if n.PathToken == "" {
		return "?"
	}
	path := n.PathToken
	if unmask != nil {
		if real, ok := unmask(n.PathToken, 0); ok {
			path = real
		}
	}
	return fmt.Sprintf("%s:%d-%d", path, n.LineStart, n.LineEnd)
}

// renderGraphHuman writes a human-readable GRAPH traversal to w. When unmask is
// non-nil each edge's src/dst HMAC path_tokens are converted back to real paths
// for display, falling back to the raw token on a miss.
func renderGraphHuman(
	w io.Writer,
	payload json.RawMessage,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
) error {
	var env graphEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("decode GRAPH response: %w", err)
	}

	if len(env.Edges) == 0 {
		fmt.Fprintln(w, "No edges.")
		return nil
	}

	fmt.Fprintf(w, "%d edge(s)\n\n", len(env.Edges))
	for _, e := range env.Edges {
		kind := e.Edge.Kind
		if kind == "" {
			kind = "?"
		}
		hint := ""
		// Hypotheses, not facts (design §10.1): low confidence or unresolved
		// dynamic edges await human curation.
		if e.confidence() < 0.5 || e.resolution() == "dynamic_unresolved" {
			hint = "  [hypothesis]"
		}
		// count > 1: several call sites between the same chunks, collapsed
		// server-side into one result.
		mult := ""
		if e.Count > 1 {
			mult = fmt.Sprintf("  (×%d)", e.Count)
		}
		fmt.Fprintf(w, " • %-8s %s → %s%s   conf %.2f  %s%s\n",
			kind, e.Src.ref(unmask), e.Dst.ref(unmask), mult, e.confidence(), e.resolution(), hint)
	}
	return nil
}

// printJSON pretty-prints a raw JSON payload to w (the --json output path).
func printJSON(w io.Writer, payload json.RawMessage) error {
	var buf any
	if err := json.Unmarshal(payload, &buf); err != nil {
		// Not decodable as a value — emit verbatim rather than fail.
		_, err = fmt.Fprintln(w, string(payload))
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(buf)
}
