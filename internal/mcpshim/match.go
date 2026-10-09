package mcpshim

import (
	"fmt"
	"regexp"
	"strings"
)

// MatchSymbol is QUERY's exact-definition mode (search-effectiveness D2): the
// wire value of the `match` argument and of the envelope's `match_used` echo.
const MatchSymbol = "symbol"

// symbolPattern mirrors server/retrieval/symbol_rank.py: a bare identifier, or
// one qualified with `.`, `::` or `#`. Checked client-side so a malformed symbol
// fails before a round trip; the server checks again and is authoritative.
var symbolPattern = regexp.MustCompile(
	`^[A-Za-z_$][A-Za-z0-9_$]*(?:(?:\.|::|#)[A-Za-z_$][A-Za-z0-9_$]*)*$`)

// SymbolSugarPrefix lets a query string ask for symbol mode: `symbol:X` is
// parsed here into the `match` argument and never sent as query text.
const SymbolSugarPrefix = "symbol:"

// ParseSymbolQuery resolves the --symbol flag and the `symbol:` sugar into the
// text to send and whether symbol mode was asked for. It errors on a symbol that
// is not an identifier rather than degrading to a hybrid search the caller would
// read as definitions.
func ParseSymbolQuery(text string, symbolFlag bool) (string, bool, error) {
	symbol := symbolFlag
	if rest, ok := strings.CutPrefix(text, SymbolSugarPrefix); ok {
		text, symbol = rest, true
	}
	if !symbol {
		return text, false, nil
	}
	text = strings.TrimSpace(text)
	if !symbolPattern.MatchString(text) {
		return "", false, fmt.Errorf("symbol mode needs a bare or qualified identifier "+
			"(e.g. isSearchStage, Session.Merge, pkg::Type); got %q", text)
	}
	return text, true, nil
}

// matchLines is the header tag and the notice lines for a QUERY envelope's match
// echo. The rule is the server's own (agent_render.py): trust match_used, never
// the request — except that a symbol request answered by an envelope with no
// match_used came from a server that predates symbol mode and silently ran
// hybrid, which only the client can say.
func matchLines(matchUsed *string, symbolFound *bool, requested bool, name string) (string, []string) {
	if matchUsed != nil && *matchUsed == MatchSymbol {
		return " · symbol definitions", nil
	}
	if symbolFound != nil && !*symbolFound {
		if name == "" {
			return "", []string{"no definition found — showing hybrid results"}
		}
		return "", []string{fmt.Sprintf("no definition named `%s` — showing hybrid results", name)}
	}
	if requested && matchUsed == nil {
		return "", []string{"hybrid results — this server does not support symbol mode"}
	}
	return "", nil
}

// MatchLines exposes matchLines for the human renderer in cmd.
func MatchLines(matchUsed *string, symbolFound *bool, requested bool, name string) (string, []string) {
	return matchLines(matchUsed, symbolFound, requested, name)
}
