package mcpshim

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Text rendering of a GRAPH response (docs/plans/agent-response-format.md,
// open question 4).
//
// The plan deferred GRAPH because the argument it made for QUERY — JSON is a bad
// container for source bodies — does not apply: GRAPH returns edges and never
// bodies, so there is no escaping cost to avoid. What survives the deferral is
// the other half of the argument, and on GRAPH it is stronger. QUERY's per-result
// overhead only became the response once snippets were switched off; a GRAPH
// response is all metadata by construction, and it repeats the most expensive
// fields hardest: a fan-out of 20 `calls` edges out of one function repeats that
// function's 64-hex path_token and 36-char repo_id 20 times, once per edge, plus
// the JSON keys around them.
//
// So the rendering groups by source file exactly as the QUERY one does, and the
// same structural saving applies: an MCP tool result carries the payload twice
// (content[0].text and structuredContent), and in agent format both carry the
// rendering rather than two copies of the JSON.
//
// This file is pure: envelope in, string out, no I/O. Unmasking stays in
// graph.go; rendering only reads the real_path it wrote.

// graphConfidenceDigits is how many decimals a confidence keeps. Two would be
// enough to read, but confidence is judged against documented thresholds (≥ 0.9
// near-certain, ≥ 0.5 plausible), and .499 rounding up to .50 would print a weak
// edge as a plausible one.
const graphConfidenceDigits = 3

type graphRenderEnvelope struct {
	Edges []graphRenderEdge          `json:"edges"`
	Repos map[string]graphRenderRepo `json:"repos"`
}

type graphRenderRepo struct {
	RemoteURL string `json:"remote_url"`
}

type graphRenderEdge struct {
	Edge     graphRenderMeta      `json:"edge"`
	Src      graphRenderNode      `json:"src"`
	Dst      graphRenderNode      `json:"dst"`
	Count    int                  `json:"count"`
	Evidence *graphRenderEvidence `json:"evidence"`

	// Item-level mirrors emitted by servers predating the API3 compaction. The
	// edge object is canonical; these are read only as a fallback, the same way
	// `codastre graph`'s human renderer reads them.
	LegacyConfidence *float64 `json:"confidence"`
	LegacyResolution string   `json:"resolution"`
}

type graphRenderMeta struct {
	Kind       string   `json:"kind"`
	Confidence *float64 `json:"confidence"`
	Resolution string   `json:"resolution"`
}

type graphRenderNode struct {
	RepoID    string `json:"repo_id"`
	PathToken string `json:"path_token"`
	RealPath  string `json:"real_path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	PathClass string `json:"path_class"`
}

type graphRenderEvidence struct {
	RealFilePath  string `json:"real_file_path"`
	FilePathToken string `json:"file_path_token"`
	Line          int    `json:"line"`
	Expr          string `json:"expr"`
}

func (e graphRenderEdge) confidence() float64 {
	if e.Edge.Confidence != nil {
		return *e.Edge.Confidence
	}
	if e.LegacyConfidence != nil {
		return *e.LegacyConfidence
	}
	return 0
}

// resolution is the canonical edge.resolution, falling back to the legacy mirror
// and then to "heuristic" — which is what an absent value means in the compact
// server shape (server/api/graph_shape.py).
func (e graphRenderEdge) resolution() string {
	if e.Edge.Resolution != "" {
		return e.Edge.Resolution
	}
	if e.LegacyResolution != "" {
		return e.LegacyResolution
	}
	return "heuristic"
}

// hypothesis marks an edge that is a candidate rather than a fact (design
// §10.1): low confidence, or a dynamic reference the extractor could not resolve.
func (e graphRenderEdge) hypothesis() bool {
	return e.confidence() < 0.5 || e.resolution() == "dynamic_unresolved"
}

// path is what the reader should open, with the same masked-token marking as the
// QUERY rendering: a 64-hex token is an HMAC digest and is labelled, a cleartext
// token from a `none`-scheme repo is not.
func (n graphRenderNode) path() string {
	if n.RealPath != "" {
		return n.RealPath
	}
	if n.PathToken == "" {
		return "?"
	}
	if isMaskedToken(n.PathToken) {
		return n.PathToken + " [masked]"
	}
	return n.PathToken
}

func (n graphRenderNode) span() string {
	return fmt.Sprintf("%d-%d", n.LineStart, n.LineEnd)
}

func (e graphRenderEnvelope) repoLabel(repoID string, fallback func(string) (string, bool)) string {
	if r, ok := e.Repos[repoID]; ok && r.RemoteURL != "" {
		return r.RemoteURL
	}
	if fallback != nil {
		if url, ok := fallback(repoID); ok && url != "" {
			return url
		}
	}
	if repoID == "" {
		return "(unknown repo)"
	}
	return repoID
}

// srcGroup is the edges leaving one (repo, path) pair. Grouping by the source
// file is what pays: a traversal's edges overwhelmingly share a source — that is
// what a traversal is — so the path and repo that dominate the payload are
// written once per file instead of once per edge.
type srcGroup struct {
	repoID string
	path   string
	edges  []graphRenderEdge
}

// groupEdges collapses edges into per-source-file groups gathered under their
// repo, each appearing at the position of its first edge. Same two-pass shape as
// groupResults, and for the same reason: a repo's files must not be split into
// several runs by interleaved edges, but within a repo the server's ordering
// (confidence DESC) has to survive.
func groupEdges(edges []graphRenderEdge) []srcGroup {
	var byOrder []srcGroup
	index := map[string]int{}
	var repoOrder []string
	repoSeen := map[string]bool{}

	for _, e := range edges {
		key := e.Src.RepoID + "\x00" + e.Src.path()
		if i, ok := index[key]; ok {
			byOrder[i].edges = append(byOrder[i].edges, e)
			continue
		}
		index[key] = len(byOrder)
		byOrder = append(byOrder, srcGroup{
			repoID: e.Src.RepoID,
			path:   e.Src.path(),
			edges:  []graphRenderEdge{e},
		})
		if !repoSeen[e.Src.RepoID] {
			repoSeen[e.Src.RepoID] = true
			repoOrder = append(repoOrder, e.Src.RepoID)
		}
	}

	groups := make([]srcGroup, 0, len(byOrder))
	for _, repo := range repoOrder {
		for _, g := range byOrder {
			if g.repoID == repo {
				groups = append(groups, g)
			}
		}
	}
	return groups
}

// RenderGraphText renders a GRAPH payload as agent text for callers outside this
// package (the `codastre graph --format agent` path).
func RenderGraphText(payload []byte, opts RenderOptions) (string, bool) {
	return renderGraphText(payload, opts)
}

// renderGraphText renders an enriched GRAPH payload as text. Returns ok=false
// when the payload does not decode as a GRAPH envelope, so the caller can fall
// back to shipping the JSON rather than an empty result.
//
// opts.Unmask is for callers that have not been through enrichGraphResponse
// (`codastre graph`, which holds the raw server envelope). opts.NoSnippets is
// ignored: GRAPH carries no bodies to suppress.
func renderGraphText(payload []byte, opts RenderOptions) (string, bool) {
	var env graphRenderEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return "", false
	}
	if opts.Unmask != nil {
		for i := range env.Edges {
			unmaskGraphNode(&env.Edges[i].Src, opts.Unmask)
			unmaskGraphNode(&env.Edges[i].Dst, opts.Unmask)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "codastre · graph · %d edge(s) · %d repo(s)\n",
		len(env.Edges), len(env.Repos))
	if len(env.Edges) == 0 {
		b.WriteString("no edges\n")
		return b.String(), true
	}

	label := func(repoID string) string { return env.repoLabel(repoID, opts.RepoLabel) }
	lastRepo := ""
	for _, g := range groupEdges(env.Edges) {
		if g.repoID != lastRepo {
			b.WriteString(label(g.repoID))
			b.WriteByte('\n')
			lastRepo = g.repoID
		}
		fmt.Fprintf(&b, "  %s\n", g.path)
		for _, e := range g.edges {
			writeEdge(&b, e, label)
		}
	}
	return b.String(), true
}

// unmaskGraphNode fills in real_path where enrichment did not. GRAPH responses
// carry no top-level mask_key_rev, so rev 0 is passed — the CLI's resolver scans
// every loaded revision for a match.
func unmaskGraphNode(n *graphRenderNode, unmask func(string, int) (string, bool)) {
	if n.RealPath != "" || n.PathToken == "" {
		return
	}
	if real, ok := unmask(n.PathToken, 0); ok {
		n.RealPath = real
	}
}

// writeEdge renders one edge as a line under its source file:
//
//	12-40  kafka → app/consumer.py:5-15 · conf .95 · ×3
//
// The source span leads because it is what distinguishes the group's edges from
// each other; everything after the arrow describes the destination and how much
// the edge should be trusted.
func writeEdge(b *strings.Builder, e graphRenderEdge, label func(string) string) {
	kind := e.Edge.Kind
	if kind == "" {
		kind = "?"
	}
	fmt.Fprintf(b, "    %s  %s → ", e.Src.span(), kind)

	// The destination repo is named only when it differs from the source's,
	// which is exactly when the edge crosses a repo boundary — the case the
	// whole cross-repo graph exists for, and the one a bare path cannot express.
	if e.Dst.RepoID != "" && e.Dst.RepoID != e.Src.RepoID {
		fmt.Fprintf(b, "[%s] ", label(e.Dst.RepoID))
	}
	fmt.Fprintf(b, "%s:%s", e.Dst.path(), e.Dst.span())

	for _, tag := range edgeTags(e) {
		b.WriteString(" · ")
		b.WriteString(tag)
	}
	b.WriteByte('\n')
}

// edgeTags are the annotations that change how an edge should be read. Defaults
// print nothing: resolution "heuristic" and path_class "app" are what a reader
// already assumes, and count 1 is "nothing was collapsed".
func edgeTags(e graphRenderEdge) []string {
	tags := []string{"conf " + formatConfidence(e.confidence())}
	if e.Count > 1 {
		// Several call sites between the same two chunks, collapsed server-side.
		tags = append(tags, fmt.Sprintf("×%d", e.Count))
	}
	if r := e.resolution(); r != "heuristic" {
		tags = append(tags, r)
	}
	// Endpoint noise: an edge into a test or a vendored file is corpus noise
	// rather than topology, and saying which end is noisy is the whole use of
	// the field.
	for _, side := range []struct {
		label string
		node  graphRenderNode
	}{{"src", e.Src}, {"dst", e.Dst}} {
		if side.node.PathClass != "" && side.node.PathClass != "app" {
			tags = append(tags, side.label+":"+side.node.PathClass)
		}
	}
	if e.hypothesis() {
		// Design §10.1: a candidate awaiting curation, not a fact. Kept last so
		// it reads as the verdict on everything before it.
		tags = append(tags, "hypothesis")
	}
	if e.Evidence != nil {
		if ev := e.Evidence.ref(); ev != "" {
			tags = append(tags, "at "+ev)
		}
	}
	return tags
}

// ref is the evidence's call site, when the response carried one. Only the
// location is printed: the expression that produced the edge is the extractor's
// working, and a reader who wants it can open the line.
func (ev graphRenderEvidence) ref() string {
	path := ev.RealFilePath
	if path == "" {
		path = ev.FilePathToken
	}
	if path == "" {
		return ""
	}
	if path == ev.FilePathToken && isMaskedToken(path) {
		path += " [masked]"
	}
	if ev.Line > 0 {
		return fmt.Sprintf("%s:%d", path, ev.Line)
	}
	return path
}

// formatConfidence prints .95 rather than 0.9500000000000001, dropping the
// leading zero as the QUERY rendering does for scores.
func formatConfidence(c float64) string {
	s := strconv.FormatFloat(c, 'f', graphConfidenceDigits, 64)
	// Trailing zeros carry no information here: .900 and .9 are the same
	// confidence, and the band it falls in is what a reader is checking.
	s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	// Leading zero dropped only from a fraction — trimming it from "0" itself
	// would print no confidence at all for the weakest edges there are.
	if strings.HasPrefix(s, "0.") {
		return s[1:]
	}
	return s
}
