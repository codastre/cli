package mcpshim

import (
	"encoding/json"
	"strings"
	"testing"
)

// A traversal with the two shapes that matter: a fan-out from one source file
// (what a traversal usually is, and what grouping exists for) and a cross-repo
// edge (what the graph exists for). Edges from the two repos interleave, so a
// renderer that only groups adjacent edges would print a repo header twice.
const graphRenderFixture = `{
  "edges": [
    {"edge": {"edge_id": "e1", "kind": "calls", "confidence": 0.95, "resolution": "heuristic"},
     "src": {"repo_id": "repo-go", "path_token": "internal/pay/charge.go", "real_path": "internal/pay/charge.go",
             "line_start": 12, "line_end": 40},
     "dst": {"repo_id": "repo-go", "path_token": "internal/pay/card.go", "real_path": "internal/pay/card.go",
             "line_start": 5, "line_end": 15},
     "count": 3},
    {"edge": {"edge_id": "e2", "kind": "kafka", "confidence": 0.4, "resolution": "dynamic_unresolved"},
     "src": {"repo_id": "repo-go", "path_token": "internal/pay/charge.go", "real_path": "internal/pay/charge.go",
             "line_start": 12, "line_end": 40},
     "dst": {"repo_id": "repo-ios", "path_token": "App/Orders/Consumer.swift", "real_path": "App/Orders/Consumer.swift",
             "line_start": 8, "line_end": 20},
     "count": 1},
    {"edge": {"edge_id": "e3", "kind": "imports", "confidence": 0.6, "resolution": "heuristic"},
     "src": {"repo_id": "repo-ios", "path_token": "App/Orders/ConsumerTests.swift", "real_path": "App/Orders/ConsumerTests.swift",
             "line_start": 1, "line_end": 9, "path_class": "test"},
     "dst": {"repo_id": "repo-ios", "path_token": "App/Orders/Consumer.swift", "real_path": "App/Orders/Consumer.swift",
             "line_start": 8, "line_end": 20, "path_class": "app"},
     "count": 1},
    {"edge": {"edge_id": "e4", "kind": "calls", "confidence": 0.9, "resolution": "heuristic"},
     "src": {"repo_id": "repo-go", "path_token": "internal/pay/charge.go", "real_path": "internal/pay/charge.go",
             "line_start": 60, "line_end": 72},
     "dst": {"repo_id": "repo-go", "path_token": "internal/pay/audit.go", "real_path": "internal/pay/audit.go",
             "line_start": 3, "line_end": 9},
     "count": 1}
  ],
  "repos": {
    "repo-go": {"masking_scheme": "none", "mask_key_rev": 0, "remote_url": "github.com/acme/api"},
    "repo-ios": {"masking_scheme": "hmac", "mask_key_rev": 2, "remote_url": "github.com/acme/ios"}
  }
}`

func renderGraph(t *testing.T, payload string, opts RenderOptions) string {
	t.Helper()
	out, ok := renderGraphText([]byte(payload), opts)
	if !ok {
		t.Fatal("renderGraphText rejected the payload")
	}
	return out
}

// The saving the format is for: a source path written once per file, however
// many edges leave it, and once per repo run rather than once per edge.
func TestRenderGraphText_GroupsEdgesBySourceFile(t *testing.T) {
	out := renderGraph(t, graphRenderFixture, RenderOptions{})

	if n := strings.Count(out, "\n  internal/pay/charge.go\n"); n != 1 {
		t.Errorf("source header printed %d times, want 1 (three edges share it)\n%s", n, out)
	}
	// Edges 1, 2 and 4 leave the same file and must all sit under that header.
	for _, span := range []string{"12-40", "60-72"} {
		if !strings.Contains(out, "    "+span+"  ") {
			t.Errorf("edge span %s missing\n%s", span, out)
		}
	}
	// Repo runs, not one header per edge: repo-go's edges interleave with
	// repo-ios's in the input.
	if n := strings.Count(out, "github.com/acme/api\n"); n != 1 {
		t.Errorf("source repo header printed %d times, want 1\n%s", n, out)
	}
}

// A cross-repo edge is the one case a bare destination path cannot express —
// "app/consumer.py" in which repo? — so the destination repo is named exactly
// when it differs, and stays silent when it does not.
func TestRenderGraphText_NamesTheDestinationRepoOnlyWhenItDiffers(t *testing.T) {
	out := renderGraph(t, graphRenderFixture, RenderOptions{})

	if !strings.Contains(out, "kafka → [github.com/acme/ios] App/Orders/Consumer.swift:8-20") {
		t.Errorf("cross-repo edge did not name its destination repo\n%s", out)
	}
	if strings.Contains(out, "calls → [github.com/acme/api]") {
		t.Errorf("same-repo edge repeated its repo label\n%s", out)
	}
}

// Everything that changes how an edge should be read, and nothing that does not:
// defaults (resolution heuristic, path_class app, count 1) print nothing.
func TestRenderGraphText_AnnotatesEdges(t *testing.T) {
	out := renderGraph(t, graphRenderFixture, RenderOptions{})

	for _, want := range []string{
		"conf .95",           // confidence, leading zero dropped
		"×3",                 // collapsed call sites
		"dynamic_unresolved", // non-default resolution
		"hypothesis",         // conf < 0.5 or unresolved
		"src:test",           // noisy endpoint, and which end is noisy
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing annotation %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "heuristic") {
		t.Errorf("default resolution printed; absent means heuristic\n%s", out)
	}
	if strings.Contains(out, "×1") {
		t.Errorf("count 1 printed; it means nothing was collapsed\n%s", out)
	}
	if strings.Contains(out, ":app") {
		t.Errorf("default path_class printed\n%s", out)
	}
}

// The compact server shape omits edge_id, resolution at "heuristic" and count
// at 1 (server/api/graph_shape.py). The rendering must read those absences as
// their defaults rather than as missing data.
func TestRenderGraphText_ReadsCompactServerShape(t *testing.T) {
	const compact = `{
	  "edges": [
	    {"edge": {"kind": "calls", "confidence": 0.95},
	     "src": {"repo_id": "r1", "path_token": "a.go", "line_start": 1, "line_end": 9},
	     "dst": {"repo_id": "r1", "path_token": "b.go", "line_start": 2, "line_end": 4}}
	  ],
	  "repos": {"r1": {"masking_scheme": "none", "mask_key_rev": 0}}
	}`
	out := renderGraph(t, compact, RenderOptions{})

	if !strings.Contains(out, "1-9  calls → b.go:2-4 · conf .95") {
		t.Errorf("compact edge did not render\n%s", out)
	}
	if strings.Contains(out, "hypothesis") {
		t.Errorf("an absent resolution was read as unresolved\n%s", out)
	}
}

// Confidence is judged against thresholds, so the rendering must not round an
// edge across one — and must still print something for a zero.
func TestFormatConfidence(t *testing.T) {
	cases := map[float64]string{
		0.9500000000000001: ".95",
		0.499:              ".499",
		0.5:                ".5",
		1:                  "1",
		0:                  "0",
	}
	for in, want := range cases {
		if got := formatConfidence(in); got != want {
			t.Errorf("formatConfidence(%v) = %q, want %q", in, got, want)
		}
	}
}

// The whole point, measured: the rendering is smaller than the JSON it replaces,
// and it replaces BOTH copies an MCP tool result carries.
func TestRenderGraphText_IsSmallerThanTheJSON(t *testing.T) {
	out := renderGraph(t, graphRenderFixture, RenderOptions{})

	// Compare against the compacted JSON, not the indented fixture.
	var v any
	if err := json.Unmarshal([]byte(graphRenderFixture), &v); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	compactJSON, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if len(out) >= len(compactJSON) {
		t.Errorf("rendering %d B is not smaller than the JSON %d B\n%s",
			len(out), len(compactJSON), out)
	}
}

// "no edges" is a real answer — the seed exists and nothing connects to it —
// and must not render as an empty response.
func TestRenderGraphText_EmptyEdges(t *testing.T) {
	out := renderGraph(t, `{"edges": [], "repos": {}}`, RenderOptions{})

	if !strings.Contains(out, "0 edge(s)") || !strings.Contains(out, "no edges") {
		t.Errorf("empty traversal rendered as %q", out)
	}
}

// `codastre graph` holds the raw server envelope, so the renderer unmasks what
// enrichment did not — and marks a token it could not unmask, so a digest is
// never mistaken for a path.
func TestRenderGraphText_UnmasksWhenAsked(t *testing.T) {
	const masked = `{
	  "edges": [
	    {"edge": {"kind": "calls", "confidence": 0.9, "resolution": "heuristic"},
	     "src": {"repo_id": "r1",
	             "path_token": "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
	             "line_start": 1, "line_end": 9},
	     "dst": {"repo_id": "r1",
	             "path_token": "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100",
	             "line_start": 2, "line_end": 4},
	     "count": 1}
	  ],
	  "repos": {"r1": {"masking_scheme": "hmac", "mask_key_rev": 0}}
	}`
	out := renderGraph(t, masked, RenderOptions{
		Unmask: func(token string, _ int) (string, bool) {
			if strings.HasPrefix(token, "aabb") {
				return "internal/pay/charge.go", true
			}
			return "", false
		},
	})

	if !strings.Contains(out, "internal/pay/charge.go") {
		t.Errorf("src was not unmasked\n%s", out)
	}
	if !strings.Contains(out, "[masked]") {
		t.Errorf("an unresolvable digest was printed as if it were a path\n%s", out)
	}
}

// A payload that is not a GRAPH envelope must be refused, so the proxy ships the
// JSON rather than an empty rendering.
func TestRenderGraphText_RejectsNonEnvelope(t *testing.T) {
	if _, ok := renderGraphText([]byte(`{"nope"`), RenderOptions{}); ok {
		t.Error("renderGraphText accepted a malformed payload")
	}
}

// GRAPH's repos map carries only masking metadata, so `codastre graph` supplies
// the names — without them a traversal is headed by a bare UUID.
func TestRenderGraphText_LabelsReposFromTheFallback(t *testing.T) {
	const noURLs = `{
	  "edges": [
	    {"edge": {"kind": "calls", "confidence": 0.9},
	     "src": {"repo_id": "547a88ae", "path_token": "a.go", "line_start": 1, "line_end": 9},
	     "dst": {"repo_id": "547a88ae", "path_token": "b.go", "line_start": 2, "line_end": 4}}
	  ],
	  "repos": {"547a88ae": {"masking_scheme": "none", "mask_key_rev": 0}}
	}`
	out := renderGraph(t, noURLs, RenderOptions{
		RepoLabel: func(repoID string) (string, bool) {
			return "github.com/acme/api", repoID == "547a88ae"
		},
	})

	if !strings.Contains(out, "github.com/acme/api") {
		t.Errorf("repo not named from the fallback\n%s", out)
	}
	if strings.Contains(out, "547a88ae") {
		t.Errorf("bare repo id still printed\n%s", out)
	}
}

// A server value beats the local snapshot, which can be stale — the same rule
// the remote_url backfill follows.
func TestRenderGraphText_ServerRepoURLWins(t *testing.T) {
	out := renderGraph(t, graphRenderFixture, RenderOptions{
		RepoLabel: func(string) (string, bool) { return "github.com/acme/stale", true },
	})

	if strings.Contains(out, "stale") {
		t.Errorf("local label overrode the envelope's remote_url\n%s", out)
	}
}
