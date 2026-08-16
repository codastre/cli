package mcpshim

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Snippet line budgets (docs/bugs/query-defaults-token-budget.md, C1 and C3).
//
// Hydration is the whole cost of a QUERY response: the server ships only
// (path, span, score), and the proxy turns each span into source. Before this,
// the span was read unconditionally, so response size was a function of index
// quality rather than of top_k — a header chunk spanning a 1,218-line file
// billed the caller for the entire file (docs/bugs/header-chunk-snippet-bloat.md).
// The server now clamps every span to 240 lines, which stops the pathological
// case; these budgets address the ordinary one.
const (
	// defaultMaxSnippetLines caps a normal result. Measured healthy chunks span
	// 7-72 lines (p50 25), so 80 is above the working range and only bites spans
	// that were never going to be read in full anyway.
	defaultMaxSnippetLines = 80

	// maxSnippetLineChars bounds a single line. A line budget alone does not
	// bound a snippet: minified and one-line-JSON artifacts put the whole file on
	// line 1, so an 80-line cap can still deliver 64 KB from one line — the same
	// failure shape as the header-chunk bug, one level down. Real source lines are
	// rarely past ~200 chars, so 800 never truncates hand-written code.
	maxSnippetLineChars = 800

	// maxSnippetLineBytes is the scanner's per-line ceiling. bufio.Scanner's
	// 64 KB default made a one-line 5 MB fixture fail with ErrTooLong, which
	// surfaced as `hydration: "read_error"` — a diagnosis that sends the agent to
	// check permissions on a file that is merely one long line. Raising it lets
	// such a file hydrate, truncated to maxSnippetLineChars.
	maxSnippetLineBytes = 4 * 1024 * 1024

	// demotedSnippetLines is the budget for derived, machine-emitted content:
	// enough to identify what the file is, not enough to pay for it. In the
	// measured run, generated artifacts (an admin-framework schema dump and a SQL
	// structure dump) were 27% of the payload and answered nothing.
	demotedSnippetLines = 10
)

// demotedPathClasses are the server-assigned path_class values whose bodies are
// rarely worth reading. The server already de-ranks these
// (server/retrieval/path_class_rank.py) but keeps them reachable on purpose — a
// test IS the answer to "where is X tested". Demoting hydration rather than
// dropping the result keeps that property while cutting the bytes: the path,
// span and score still land, only the body is abbreviated.
//
// `test` is deliberately absent. Test bodies are frequently the answer.
var demotedPathClasses = map[string]bool{
	"generated": true,
	"vendored":  true,
	"fixture":   true,
	"doc_asset": true,
}

// derivedSuffixes match build artifacts and dumps that classify_path does not
// currently label — it keys on generator-marked source names (`*.pb.go`,
// `mock_*.go`) and vendored trees, not on data dumps. Extending the server's
// classifier would be the better home, but path_class is written at index time,
// so it would only take effect after a re-index; this list works against the
// already-deployed corpus today.
var derivedSuffixes = []string{
	"-schema.json",
	"_structure.sql",
	"-lock.json",
	".lock",
	".sum",
	".min.js",
	".min.css",
	".map",
	".snap",
}

// derivedSegments are path segments holding build output. Kept narrow: these are
// directories nothing hand-written lives in. Vendored trees are already covered
// by path_class.
var derivedSegments = map[string]bool{
	"dist":  true,
	"build": true,
	".next": true,
}

// isDerivedArtifact reports whether a real repo-relative path looks like build
// output or a generated data dump.
func isDerivedArtifact(realPath string) bool {
	lower := strings.ToLower(realPath)
	base := filepath.Base(lower)
	for _, suf := range derivedSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	dir := filepath.Dir(lower)
	if dir == "." {
		return false
	}
	for seg := range strings.SplitSeq(dir, "/") {
		if derivedSegments[seg] {
			return true
		}
	}
	return false
}

// snippetLineBudget returns the maximum number of lines to hydrate for one
// result. pathClass is the server-assigned class ("" when the point predates
// the field, or is an hmac-premasked overlay point with no real path).
func (cfg Config) snippetLineBudget(realPath, pathClass string) int {
	limit := cfg.MaxSnippetLines
	if limit <= 0 {
		limit = defaultMaxSnippetLines
	}
	if demotedPathClasses[pathClass] || isDerivedArtifact(realPath) {
		if demotedSnippetLines < limit {
			return demotedSnippetLines
		}
	}
	return limit
}

// clipRunes truncates s to at most n bytes without splitting a rune. Cutting
// mid-rune would emit invalid UTF-8, which JSON encoding silently replaces with
// U+FFFD — a corrupted character in what is supposed to be a verbatim quote of
// the developer's own file.
func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// snippetResult is hydrateSnippet's outcome. Lines counts the lines actually
// read from the requested range, so the caller can tell "the range yielded
// nothing" (missing/zero line_end, or a checkout where the file is shorter than
// line_start) from "the range is genuinely blank lines" — the former is a
// hydration failure that needs a reason, not an empty success.
type snippetResult struct {
	Text  string
	Lines int
	Stale bool
	// Truncated is set when either budget cut the snippet short — the line count
	// or an individual line's length. LastLine is the last line delivered, so the
	// agent can Read from there instead of re-deriving the boundary; a line
	// shortened by maxSnippetLineChars ends in "…", which is what distinguishes
	// "the range continues past LastLine" from "this line continues past its cut".
	// line_start/line_end on the result are left as the server sent them: they
	// describe the chunk the ranker scored, and rewriting them would make the
	// result disclaim a span it was ranked on.
	Truncated bool
	LastLine  int
}

// hydrateSnippet reads the inclusive, 1-based line range [lineStart, lineEnd]
// from the file and checks staleness, stopping after maxLines lines. The
// server's chunk ranges are 1-based (see the proxy_query_test fixtures); a stray
// line_start of 0 is clamped to 1 so the first line is included rather than
// skipped. (If a future server change switches to 0-based ranges, this is the
// one place to revisit.)
//
// maxLines <= 0 means unbounded, which no caller should pass — the budget is
// resolved by snippetLineBudget. It exists so the truncation logic has an
// explicit off switch rather than a magic large number.
func hydrateSnippet(absPath string, lineStart, lineEnd, maxLines int, blobSHA string) (snippetResult, error) {
	if lineStart < 1 {
		lineStart = 1
	}
	f, err := os.Open(absPath)
	if err != nil {
		return snippetResult{}, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxSnippetLineBytes)
	var lines []string
	lineNum := 1
	lastKept := 0
	truncated := false
	for sc.Scan() {
		if lineNum >= lineStart && lineNum <= lineEnd {
			if maxLines > 0 && len(lines) >= maxLines {
				// The range extends past the budget; stop reading and say so.
				// Detected here rather than from (lineEnd-lineStart) so that a
				// range overhanging the end of a shorter file is not reported
				// as truncated when every line it had was delivered.
				truncated = true
				break
			}
			line := sc.Text()
			if len(line) > maxSnippetLineChars {
				line = clipRunes(line, maxSnippetLineChars) + "…"
				truncated = true
			}
			lines = append(lines, line)
			lastKept = lineNum
		}
		if lineNum > lineEnd {
			break
		}
		lineNum++
	}
	if err := sc.Err(); err != nil {
		return snippetResult{}, err
	}
	if len(lines) == 0 {
		return snippetResult{}, nil
	}

	out := snippetResult{
		Text:      strings.Join(lines, "\n"),
		Lines:     len(lines),
		Truncated: truncated,
		LastLine:  lastKept,
	}

	// Staleness check: compare current blob hash to expected.
	if blobSHA != "" {
		current, err := currentBlobSHA(absPath)
		if err == nil && current != blobSHA {
			out.Stale = true
		}
	}

	return out, nil
}

func currentBlobSHA(absPath string) (string, error) {
	out, err := exec.Command("git", "hash-object", absPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
