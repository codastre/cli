package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const corpusEnvelope = `{
  "status": "ok",
  "freshness": "fresh",
  "searched_corpus_count": 494,
  "mask_key_revs": {"r1": 3},
  "corpora": [
    {"corpus_id": "r1", "kind": "git_repo", "name": "github.com/org/card-auth",
     "score": 1.2, "card_score": 0.9, "evidence_score": 0.1, "diversity_score": 1.0,
     "file_count": 4, "has_card": true,
     "why": [{"repo_id": "r1", "path_token": "internal/auth/token.go",
              "line_start": 40, "line_end": 88, "symbol_name": "ValidateToken"}]},
    {"corpus_id": "r2", "kind": "document_set", "name": "SRE runbooks",
     "score": 0.4, "card_score": 0.0, "evidence_score": 0.4, "diversity_score": 0.5,
     "file_count": 2, "has_card": false, "why": []}
  ]
}`

func renderToString(t *testing.T, body string, unmask func(string, int) (string, bool), agent bool) string {
	t.Helper()
	var out, warn bytes.Buffer
	if err := renderCorpora(&out, &warn, json.RawMessage(body), unmask, agent); err != nil {
		t.Fatalf("renderCorpora: %v", err)
	}
	return out.String()
}

func TestRenderCorpora_LeadsWithNameAndKind(t *testing.T) {
	got := renderToString(t, corpusEnvelope, nil, false)
	if !strings.Contains(got, "github.com/org/card-auth  (git_repo)") {
		t.Fatalf("missing repo label:\n%s", got)
	}
	// A document set must be distinguishable from a repo: the next action
	// differs, so the kind cannot be left for the reader to infer.
	if !strings.Contains(got, "SRE runbooks  (document_set)") {
		t.Fatalf("missing document-set label:\n%s", got)
	}
}

func TestRenderCorpora_EvidenceNamesFilesToOpen(t *testing.T) {
	got := renderToString(t, corpusEnvelope, nil, false)
	if !strings.Contains(got, "internal/auth/token.go:40-88") {
		t.Fatalf("missing evidence location:\n%s", got)
	}
	if !strings.Contains(got, "ValidateToken") {
		t.Fatalf("missing evidence symbol:\n%s", got)
	}
}

func TestRenderCorpora_ExplainsWhichTermCarried(t *testing.T) {
	got := renderToString(t, corpusEnvelope, nil, false)
	// card high + evidence low → the query NAMED it.
	if !strings.Contains(got, "named by the query") {
		t.Fatalf("missing identity reason:\n%s", got)
	}
	// No card at all is a weaker claim and must say so rather than silently
	// scoring like an identity match.
	if !strings.Contains(got, "no card (body matches only)") {
		t.Fatalf("missing no-card reason:\n%s", got)
	}
}

func TestRenderCorpora_UnmasksEvidencePaths(t *testing.T) {
	unmask := func(token string, rev int) (string, bool) {
		if token == "internal/auth/token.go" && rev == 3 {
			return "real/path/token.go", true
		}
		return "", false
	}
	got := renderToString(t, corpusEnvelope, unmask, false)
	if !strings.Contains(got, "real/path/token.go:40-88") {
		t.Fatalf("path not unmasked at the repo's own key rev:\n%s", got)
	}
}

func TestRenderCorpora_EmptyIsNotAnError(t *testing.T) {
	body := `{"status":"ok","freshness":"fresh","searched_corpus_count":494,"corpora":[]}`
	got := renderToString(t, body, nil, false)
	if !strings.Contains(got, "no corpus matched") || !strings.Contains(got, "494 searched") {
		t.Fatalf("empty answer must report the searched count:\n%s", got)
	}
}

func TestRenderCorpora_AgentFormatIsOneLinePerCorpus(t *testing.T) {
	got := renderToString(t, corpusEnvelope, nil, true)
	for _, want := range []string{"1. github.com/org/card-auth [git_repo]", "2. SRE runbooks [document_set]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestDecodeCorpusSearch_RejectsGarbage(t *testing.T) {
	if _, ok := decodeCorpusSearch(json.RawMessage(`not json`)); ok {
		t.Fatal("decode accepted non-JSON")
	}
}

// --corpora is an alias, not a mode with its own flag set: `query` carries
// flags that CORPUS_SEARCH has no equivalent for. Silently dropping them would
// answer a different question than the one typed.

func TestCorporaFlagConflicts_TargetFlagsAreErrors(t *testing.T) {
	for _, flag := range []string{"index-id", "repo-url", "all"} {
		errs, warns := corporaFlagConflicts(func(f string) bool { return f == flag })
		if len(errs) != 1 || errs[0] != "--"+flag {
			t.Fatalf("%s: errs = %v, want [--%s]", flag, errs, flag)
		}
		if len(warns) != 0 {
			t.Fatalf("%s: unexpected warns %v", flag, warns)
		}
	}
}

func TestCorporaFlagConflicts_ResultFiltersAreErrors(t *testing.T) {
	// These would have excluded evidence, so ignoring them changes the ranking.
	for _, flag := range []string{"ref", "path-prefix", "alert-ids", "error-codes"} {
		errs, _ := corporaFlagConflicts(func(f string) bool { return f == flag })
		if len(errs) != 1 || errs[0] != "--"+flag {
			t.Fatalf("%s: errs = %v, want [--%s]", flag, errs, flag)
		}
	}
}

func TestCorporaFlagConflicts_PresentationFlagsOnlyWarn(t *testing.T) {
	// A corpus answer is locations, never bodies — the ranking is unaffected.
	for _, flag := range []string{"snippets", "max-snippet-lines"} {
		errs, warns := corporaFlagConflicts(func(f string) bool { return f == flag })
		if len(errs) != 0 {
			t.Fatalf("%s: should not error, got %v", flag, errs)
		}
		if len(warns) != 1 || warns[0] != "--"+flag {
			t.Fatalf("%s: warns = %v, want [--%s]", flag, warns, flag)
		}
	}
}

func TestCorporaFlagConflicts_SharedFlagsPassThrough(t *testing.T) {
	// The flags both commands implement must not trip either list, or the alias
	// would reject its own supported surface.
	shared := map[string]bool{
		"server": true, "key": true, "language": true, "content-kinds": true,
		"json": true, "format": true, "no-unmask": true, "repo-path": true,
		"top-k": true, "corpora": true,
	}
	errs, warns := corporaFlagConflicts(func(f string) bool { return shared[f] })
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("shared flags tripped: errs=%v warns=%v", errs, warns)
	}
}

func TestCorporaFlagConflicts_NothingSetIsClean(t *testing.T) {
	errs, warns := corporaFlagConflicts(func(string) bool { return false })
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("errs=%v warns=%v", errs, warns)
	}
}
