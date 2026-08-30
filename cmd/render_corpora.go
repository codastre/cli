package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// renderCorpora prints a ranked corpus list. Two shapes, same data:
//
//   - human: one block per corpus — what it is, why it scored, and the files
//     that put it there.
//   - agent: the same ordering with the prose stripped, for a caller that will
//     act on the top entry rather than read the list.
//
// Both lead with the corpus name and its kind, because the next action differs
// by kind: a git corpus gets cloned and searched, a document set gets read.
func renderCorpora(
	w io.Writer,
	warnW io.Writer,
	payload json.RawMessage,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
	agent bool,
) error {
	env, ok := decodeCorpusSearch(payload)
	if !ok {
		return fmt.Errorf("decode CORPUS_SEARCH response")
	}

	if len(env.Corpora) == 0 {
		// Not an error: "searched, nothing matched" is a valid answer, and the
		// searched count is what makes it credible rather than suspicious.
		fmt.Fprintf(w, "no corpus matched (%d searched, %s)\n", env.SearchedCorpusCount, env.Freshness)
		return nil
	}

	fmt.Fprintf(warnW, "codastre · %d corpora ranked of %d searched · %s\n",
		len(env.Corpora), env.SearchedCorpusCount, env.Freshness)

	for i, c := range env.Corpora {
		label := c.Name
		if label == "" {
			label = c.CorpusID
		}
		if agent {
			fmt.Fprintf(w, "%d. %s [%s] %.4f %s\n", i+1, label, c.Kind, c.Score, scoreReason(c))
		} else {
			fmt.Fprintf(w, "\n%d. %s  (%s)\n", i+1, label, c.Kind)
			fmt.Fprintf(w, "   score %.4f — %s\n", c.Score, scoreReason(c))
		}
		for _, why := range c.Why {
			path := why.PathToken
			if unmask != nil {
				if real, ok := unmask(why.PathToken, env.MaskKeyRevs[why.RepoID]); ok {
					path = real
				}
			}
			sym := ""
			if why.SymbolName != nil && *why.SymbolName != "" {
				sym = "  " + *why.SymbolName
			}
			fmt.Fprintf(w, "     %s:%d-%d%s\n", path, why.LineStart, why.LineEnd, sym)
		}
	}
	return nil
}

// scoreReason states, in words, which term carried the corpus. The three
// components are not interchangeable — "the query names this" and "this
// contains what you described" lead to different next actions — so collapsing
// them into the single score would throw away the part a caller acts on.
func scoreReason(c corpusHit) string {
	var parts []string
	switch {
	case !c.HasCard:
		parts = append(parts, "no card (body matches only)")
	case c.CardScore >= 0.5 && c.Evidence < 0.2:
		parts = append(parts, "named by the query")
	case c.CardScore >= 0.5:
		parts = append(parts, "named, and corroborated")
	default:
		parts = append(parts, "content match")
	}
	if c.FileCount > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s)", c.FileCount))
	}
	return strings.Join(parts, ", ")
}
