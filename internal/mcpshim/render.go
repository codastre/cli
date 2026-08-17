package mcpshim

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Text rendering of a QUERY response (docs/plans/agent-response-format.md, C).
//
// Why text at all. JSON is a poor container for source: every quote becomes \",
// every newline \n, which inflates bytes and fragments the byte sequences a
// tokenizer packs well for code. It also repeats every key name on every result
// and, on a measured 5-hit response, repeated the same 100-char path four times
// — twice as path_token, twice as real_path.
//
// The bigger saving is structural. An MCP tool result carries the payload TWICE:
// content[0].text (a JSON string) and structuredContent. On that same response
// they were 10,290 B and 10,268 B of a 22,019 B line. QUERY declares an
// outputSchema, so structuredContent cannot simply be dropped — but nothing says
// it has to be a second copy of the JSON. In agent format both representations
// carry the same rendering, so the duplication costs a second copy of ~1 KB of
// text instead of a second copy of ~10 KB of JSON.
//
// This file is pure: envelope in, string out, no I/O. Hydration semantics stay
// in query.go; rendering only reads the fields it wrote.

// renderMaxScoreDigits is how many decimals a score keeps in the rendering.
// Fused scores are RRF-scale — 0.01639344, 0.01612903, 0.01587301, 0.015625 on
// the measured response — so the usual two decimals would print every rank as
// "0.02" and turn the ranking into noise.
const renderMaxScoreDigits = 4

type renderRepo struct {
	RemoteURL string `json:"remote_url"`
}

type renderEnvelope struct {
	Status            string                `json:"status"`
	Freshness         string                `json:"freshness"`
	SyncJobID         *string               `json:"sync_job_id"`
	SearchedRepos     []string              `json:"searched_repos"`
	SearchedRepoCount *int                  `json:"searched_repo_count"`
	Repos             map[string]renderRepo `json:"repos"`
	MaskKeyRev        int                   `json:"mask_key_rev"`
	MaskKeyRevs       map[string]int        `json:"mask_key_revs"`
	FilterMatched     *bool                 `json:"filter_matched"`
	Results           []renderResult        `json:"results"`
}

type renderResult struct {
	RepoID      string  `json:"repo_id"`
	PathToken   string  `json:"path_token"`
	RealPath    string  `json:"real_path"`
	LineStart   int     `json:"line_start"`
	LineEnd     int     `json:"line_end"`
	Score       float64 `json:"score"`
	ContentKind string  `json:"content_kind"`
	PathClass   string  `json:"path_class"`
	SymbolName  string  `json:"symbol_name"`
	BlobSHA     string  `json:"blob_sha"`
	ChunkID     string  `json:"chunk_id"`
	Title       string  `json:"title"`
	SourceRef   string  `json:"source_ref"`

	// Proxy-added (query.go).
	Snippet          string `json:"snippet"`
	SnippetTruncated bool   `json:"snippet_truncated"`
	SnippetLineEnd   int    `json:"snippet_line_end"`
	Stale            bool   `json:"stale"`
	Hydration        string `json:"hydration"`
}

// path is what the reader should open: the unmasked path when one is available,
// else the raw token. A token that is an HMAC digest is marked, so it is never
// mistaken for a path that simply does not exist locally — but a `none`-scheme
// repo's token IS the path, and labelling that "[masked]" would tell the reader
// the opposite of the truth. `codastre query` reaches here without the proxy's
// scheme lookup, so the shape of the token is what distinguishes them.
func (r renderResult) path() string {
	if r.RealPath != "" {
		return r.RealPath
	}
	if isMaskedToken(r.PathToken) {
		return r.PathToken + " [masked]"
	}
	return r.PathToken
}

// isMaskedToken reports whether a path_token is an HMAC digest (§8.3: 64
// lowercase hex chars) rather than a cleartext repo-relative path.
func isMaskedToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, c := range token {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// searchedCount mirrors the CLI's human renderer: the scoped federated count
// when present, else the length of the full searched_repos list.
func (e renderEnvelope) searchedCount() int {
	if e.SearchedRepoCount != nil {
		return *e.SearchedRepoCount
	}
	return len(e.SearchedRepos)
}

func (e renderEnvelope) repoLabel(repoID string) string {
	if r, ok := e.Repos[repoID]; ok && r.RemoteURL != "" {
		return r.RemoteURL
	}
	if repoID == "" {
		return "(unknown repo)"
	}
	return repoID
}

// fileGroup is the hits of one (repo, path, blob) triple. Blob is part of the
// key rather than hoisted per path because base and overlay can hold different
// versions of the same file, and one header claiming a blob for both would make
// the staleness anchor wrong for one of them.
type fileGroup struct {
	repoID  string
	path    string
	blobSHA string
	hits    []renderResult
}

// groupResults collapses results into per-file groups, ordered by each group's
// best-ranked hit and gathered under their repo, which likewise appears at its
// own best rank.
//
// Grouping only adjacent results would miss the common case — on the measured
// response two hits in one file were ranked 2nd and 4th, and its two repos
// interleaved — and gathering costs nothing that matters: a group sits at its
// best hit's position and every hit still prints its own score, so the ranking
// is legible without repeating a repo header three times.
func groupResults(results []renderResult) []fileGroup {
	// Pass 1: one group per file, in order of its best-ranked hit.
	var byRank []fileGroup
	index := map[string]int{}
	var repoOrder []string
	repoSeen := map[string]bool{}

	for _, r := range results {
		key := r.RepoID + "\x00" + r.path() + "\x00" + r.BlobSHA
		if i, ok := index[key]; ok {
			byRank[i].hits = append(byRank[i].hits, r)
			continue
		}
		index[key] = len(byRank)
		byRank = append(byRank, fileGroup{
			repoID:  r.RepoID,
			path:    r.path(),
			blobSHA: r.BlobSHA,
			hits:    []renderResult{r},
		})
		if !repoSeen[r.RepoID] {
			repoSeen[r.RepoID] = true
			repoOrder = append(repoOrder, r.RepoID)
		}
	}

	// Pass 2: gather each repo's groups into one run, repos in order of their
	// best hit. A stable partition rather than a sort — within a repo the groups
	// must stay in rank order.
	groups := make([]fileGroup, 0, len(byRank))
	for _, repo := range repoOrder {
		for _, g := range byRank {
			if g.repoID == repo {
				groups = append(groups, g)
			}
		}
	}
	return groups
}

// RenderOptions tunes the rendering. Unmask is for callers that have NOT been
// through the proxy's enrichment — `codastre query` holds the raw server
// envelope, where path_token is all there is — and is nil inside the proxy,
// which has already written real_path.
type RenderOptions struct {
	NoSnippets bool
	Unmask     func(pathToken string, maskKeyRev int) (string, bool)
}

// RenderQueryText renders a QUERY payload as agent text for callers outside this
// package (the `codastre query --format agent` path).
func RenderQueryText(payload []byte, opts RenderOptions) (string, bool) {
	return renderQueryText(payload, opts)
}

// renderQueryText renders an enriched QUERY payload as text. Returns ok=false
// when the payload does not decode as a QUERY envelope, so the caller can fall
// back to shipping the JSON rather than an empty result.
func renderQueryText(payload []byte, opts RenderOptions) (string, bool) {
	var env renderEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return "", false
	}
	if opts.Unmask != nil {
		for i, r := range env.Results {
			if r.RealPath != "" {
				continue
			}
			rev := env.MaskKeyRev
			if v, ok := env.MaskKeyRevs[r.RepoID]; ok {
				rev = v
			}
			if real, ok := opts.Unmask(r.PathToken, rev); ok {
				env.Results[i].RealPath = real
			}
		}
	}

	var b strings.Builder
	writeHeader(&b, env, opts.NoSnippets)
	if len(env.Results) == 0 {
		b.WriteString("no matches\n")
		// A prefix that matched nothing indexed is a different answer from "the
		// query found nothing", and only the envelope can tell them apart.
		if env.FilterMatched != nil && !*env.FilterMatched {
			b.WriteString("path_prefix matched nothing indexed — check the prefix\n")
		}
		return b.String(), true
	}

	lastRepo := ""
	for _, g := range groupResults(env.Results) {
		if g.repoID != lastRepo {
			b.WriteString(env.repoLabel(g.repoID))
			b.WriteByte('\n')
			lastRepo = g.repoID
		}
		writeFileHeader(&b, g)
		for _, hit := range g.hits {
			writeHit(&b, hit, opts.NoSnippets)
		}
	}
	return b.String(), true
}

func writeHeader(b *strings.Builder, env renderEnvelope, noSnippets bool) {
	mode := "snippets:on"
	if noSnippets {
		mode = "snippets:off"
	}
	fmt.Fprintf(b, "codastre · %d hits · %d repo(s) searched · %s · %s\n",
		len(env.Results), env.searchedCount(), env.Freshness, mode)
	// A sync in flight means these results may predate the working tree; the
	// job id is what a follow-up call would poll.
	if env.SyncJobID != nil && *env.SyncJobID != "" {
		fmt.Fprintf(b, "sync in progress: job %s\n", *env.SyncJobID)
	}
}

func writeFileHeader(b *strings.Builder, g fileGroup) {
	b.WriteString("  ")
	b.WriteString(g.path)
	// Full sha, not a prefix: it is the anchor an agent compares against
	// `git hash-object` (and codastre-fetch-source verifies), and those are
	// equality checks. Once per file rather than once per hit is the saving.
	if g.blobSHA != "" {
		b.WriteString(" @")
		b.WriteString(g.blobSHA)
	}
	b.WriteByte('\n')
}

func writeHit(b *strings.Builder, r renderResult, noSnippets bool) {
	fmt.Fprintf(b, "    %s  %d-%d", formatScore(r.Score), r.LineStart, r.LineEnd)
	if r.SymbolName != "" {
		b.WriteString("  ")
		b.WriteString(r.SymbolName)
	}
	for _, tag := range hitTags(r, noSnippets) {
		b.WriteString(" · ")
		b.WriteString(tag)
	}
	b.WriteByte('\n')
	if r.Snippet != "" {
		writeSnippet(b, r)
	}
}

// hitTags are the annotations that change how a hit should be read: what it is
// when it is not ordinary app code, why there is no body, and whether the body
// can be trusted. Defaults (content_kind "code", path_class "app") print
// nothing — they are the assumption a reader already holds.
func hitTags(r renderResult, noSnippets bool) []string {
	var tags []string
	if r.ContentKind != "" && r.ContentKind != "code" {
		tags = append(tags, r.ContentKind)
	}
	if r.PathClass != "" && r.PathClass != "app" {
		tags = append(tags, r.PathClass)
	}
	if r.Title != "" {
		title := r.Title
		if r.SourceRef != "" && r.SourceRef != r.Title {
			title += " [" + r.SourceRef + "]"
		}
		tags = append(tags, title)
	}
	if r.Stale {
		// The local file has changed since it was indexed: the span is a lead,
		// not a quote.
		tags = append(tags, "stale — local file differs from the indexed blob")
	}
	// Per-hit reasons, except the one the header already gave: repeating
	// "snippets_disabled" on every hit of a snippets:off response spends ~28 B a
	// line restating the mode. The other reasons vary per hit and are the whole
	// point of the field — they say what to fix.
	if r.Hydration != "" && !(noSnippets && r.Hydration == hydrationSnippetsDisabled) {
		tags = append(tags, "no body: "+r.Hydration)
	}
	// The exact GRAPH seed, carried only where it is the sole one: no symbol to
	// seed with, and a content kind the extractors actually produce edges for.
	// A 64-hex id on a JSON fixture chunk is 64 bytes for a traversal that has
	// no edges to find.
	if r.ChunkID != "" && r.SymbolName == "" && (r.ContentKind == "" || r.ContentKind == "code") {
		tags = append(tags, "seed:"+r.ChunkID)
	}
	return tags
}

// writeSnippet prints the body with each line's real file line number.
//
// The number is the point, not decoration. Citations drifting off the true line
// (F7 in the guidance doc: a declaration at :302 cited as :311 and :48) come
// from deriving a line from the chunk's range; a number per line removes the
// derivation. hydrateSnippet clamps a line_start below 1 to 1, so the first body
// line is max(line_start, 1) and the rest follow.
func writeSnippet(b *strings.Builder, r renderResult) {
	last := WriteSnippetLines(b, snippetIndent, r.Snippet, r.LineStart)
	if r.SnippetTruncated {
		if r.SnippetLineEnd != 0 {
			last = r.SnippetLineEnd
		}
		// Say where to resume rather than that something is missing: the agent's
		// next move is a Read, and this hands it the exact range.
		fmt.Fprintf(b, "%s⋯ truncated · Read %s:%d-%d for the rest\n",
			snippetIndent, r.path(), last+1, r.LineEnd)
	}
}

// snippetIndent is the gutter's left margin in the agent rendering: two levels
// in from the repo header, one past the hit line it belongs to.
const snippetIndent = "      "

// WriteSnippetLines writes a snippet to w with one real file line number per
// line, each row prefixed by indent, and returns the last line number written.
//
// Exported so `codastre query`'s human output numbers bodies through this code
// rather than its own copy. A line number derived in two places is a line number
// that drifts, and drifting citations (F7) are the whole reason the gutter
// exists — see writeSnippet. hydrateSnippet clamps a line_start below 1 to 1, so
// the first body line is max(lineStart, 1) and the rest follow.
func WriteSnippetLines(w io.Writer, indent, snippet string, lineStart int) int {
	if lineStart < 1 {
		lineStart = 1
	}
	lines := strings.Split(snippet, "\n")
	width := len(strconv.Itoa(lineStart + len(lines) - 1))
	for i, line := range lines {
		fmt.Fprintf(w, "%s%*d│ %s\n", indent, width, lineStart+i, line)
	}
	return lineStart + len(lines) - 1
}

// formatScore prints a score compactly: .0164 rather than 0.01639344262295082.
// Fractions below 1 (every fused score in practice) drop the leading zero.
func formatScore(score float64) string {
	s := strconv.FormatFloat(score, 'f', renderMaxScoreDigits, 64)
	return strings.TrimPrefix(s, "0")
}
