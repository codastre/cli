package cmd

import (
	"encoding/json"
	"fmt"
	"io"
)

// ── QUERY envelope ──────────────────────────────────────────────────────────

type queryEnvelope struct {
	Freshness     string              `json:"freshness"`
	SearchedRepos []string            `json:"searched_repos"`
	Repos         map[string]repoInfo `json:"repos"`
	SyncJobID     *string             `json:"sync_job_id"`
	MaskKeyRev    int                 `json:"mask_key_rev"`
	Results       []queryResult       `json:"results"`
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
}

// repoLabel resolves a repo_id to its remote_url for display, falling back to
// the raw id when the repos map has no entry.
func (e queryEnvelope) repoLabel(repoID string) string {
	if r, ok := e.Repos[repoID]; ok && r.RemoteURL != "" {
		return r.RemoteURL
	}
	return repoID
}

// renderQueryHuman writes a human-readable QUERY result to w. When unmask is
// non-nil it converts each result's HMAC path_token back to the real path for
// display; an unmask miss (or nil unmask) falls back to showing the raw token.
func renderQueryHuman(
	w io.Writer,
	payload json.RawMessage,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
) error {
	var env queryEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("decode QUERY response: %w", err)
	}

	fmt.Fprintf(w, "freshness: %s · searched %d repo(s)\n", env.Freshness, len(env.SearchedRepos))
	if env.SyncJobID != nil && *env.SyncJobID != "" {
		fmt.Fprintf(w, "sync in progress: job %s\n", *env.SyncJobID)
	}
	fmt.Fprintln(w)

	if len(env.Results) == 0 {
		fmt.Fprintln(w, "No matches.")
		return nil
	}

	for i, r := range env.Results {
		path := r.PathToken
		if unmask != nil {
			if real, ok := unmask(r.PathToken, env.MaskKeyRev); ok {
				path = real
			}
		}
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
			continue
		}

		// Code hit: path:lines and the symbol name.
		sym := ""
		if r.SymbolName != nil && *r.SymbolName != "" {
			sym = "  " + *r.SymbolName
		}
		fmt.Fprintf(w, "    %s:%d-%d%s  (%s)\n", path, r.LineStart, r.LineEnd, sym, r.ContentKind)
	}
	return nil
}

// ── GRAPH envelope ──────────────────────────────────────────────────────────

type graphEnvelope struct {
	Edges []graphEdge `json:"edges"`
}

type graphEdge struct {
	Edge       edgeMeta `json:"edge"`
	Src        edgeNode `json:"src"`
	Dst        edgeNode `json:"dst"`
	Confidence float64  `json:"confidence"`
	Resolution string   `json:"resolution"`
	Count      int      `json:"count"`
}

type edgeMeta struct {
	Kind string `json:"kind"`
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
		if e.Confidence < 0.5 || e.Resolution == "dynamic_unresolved" {
			hint = "  [hypothesis]"
		}
		// count > 1: several call sites between the same chunks, collapsed
		// server-side into one result.
		mult := ""
		if e.Count > 1 {
			mult = fmt.Sprintf("  (×%d)", e.Count)
		}
		fmt.Fprintf(w, " • %-8s %s → %s%s   conf %.2f  %s%s\n",
			kind, e.Src.ref(unmask), e.Dst.ref(unmask), mult, e.Confidence, e.Resolution, hint)
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
