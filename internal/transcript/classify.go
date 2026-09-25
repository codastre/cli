// Package transcript reads Claude Code session transcripts
// (~/.claude/projects/<slug>/<session>.jsonl) and turns them into local
// counters: exact per-session token/cost totals, per-tool result bytes, and
// per-turn search-episode outcomes.
//
// This is source 1 of the measurement design
// (docs/plans/per-developer-value-metrics.md) and the only one that is exact
// and retrospective at once. The parse happens locally and only locally: the
// transcript holds prompts, source code and full tool output, and none of that
// is stored, printed or uploaded by anything here. What survives a parse is
// counts, byte totals and timestamps.
package transcript

import "regexp"

// Tool classes, matching the plugin hook's vocabulary (adapters/claude/hooks/lib.js)
// so the transcript source and the JSONL-log source name the same things the
// same way — even though they are never summed together.
const (
	ClassCodastre   = "codastre"
	ClassTextSearch = "text-search"
	ClassRead       = "read"
	ClassOther      = "other"
)

// Planes a codastre call can arrive on.
const (
	PlaneMCP = "mcp"
	PlaneCLI = "cli"
)

// These mirror lib.js verbatim. A divergence would make the two sources
// disagree about what a "text search" is, which is worse than either
// definition being wrong.
var (
	codastreTool = regexp.MustCompile(`(?i)codastre.*__(QUERY|GRAPH|REGISTER|SYNC)$`)
	codastreCLI  = regexp.MustCompile("(?i)(?:^|[|;&(`]\\s*)(?:[^\\s|;&()`]*[/\\\\])?codastre(?:\\.exe)?\\s+(query|graph)\\b")
	bashSearch   = regexp.MustCompile("(?i)" + `(?:^|[|;&(` + "`" + `]\s*)(?:grep|rg|ag|ack|fd|findstr)\b` +
		`|\bgit\s+grep\b` +
		`|\bxargs\b[^|;&]*\bgrep\b` +
		`|(?:^|[|;&(` + "`" + `]\s*)find\s+[^|;&]*-name\b`)
)

// Classify maps a tool call to its class and, for codastre, its plane.
// A `codastre query … | grep x` pipeline is a codastre call, not a grep: the
// CLI plane is checked first, for the same reason the hook checks it first.
func Classify(toolName, bashCommand string) (class, plane string) {
	switch {
	case codastreTool.MatchString(toolName):
		return ClassCodastre, PlaneMCP
	case toolName == "Grep" || toolName == "Glob":
		return ClassTextSearch, ""
	case toolName == "Bash":
		if codastreCLI.MatchString(bashCommand) {
			return ClassCodastre, PlaneCLI
		}
		if bashSearch.MatchString(bashCommand) {
			return ClassTextSearch, ""
		}
		return ClassOther, ""
	case toolName == "Read" || toolName == "NotebookRead":
		return ClassRead, ""
	default:
		return ClassOther, ""
	}
}

// Episode outcomes. The enum is the design's, including `unclassified` (D2).
// The transcript records every Read regardless of whether an A/B mode was
// active, so — unlike the JSONL log — a fallback here is observable and
// `unclassified` is reached only when a turn ran no search at all.
const (
	OutcomeCodastreOnly   = "codastre_only"
	OutcomeFallbackAfter  = "fallback_after_codastre"
	OutcomeCodastreFailed = "codastre_failed"
	OutcomeTextSearchOnly = "text_search_only"
	OutcomeUnclassified   = "unclassified"
)

// Outcomes lists the enum in report order.
var Outcomes = []string{
	OutcomeCodastreOnly,
	OutcomeFallbackAfter,
	OutcomeCodastreFailed,
	OutcomeTextSearchOnly,
	OutcomeUnclassified,
}

// OutcomeLabel is the human name for each outcome.
var OutcomeLabel = map[string]string{
	OutcomeCodastreOnly:   "codastre only",
	OutcomeFallbackAfter:  "fallback after codastre",
	OutcomeCodastreFailed: "codastre failed",
	OutcomeTextSearchOnly: "text search only",
	OutcomeUnclassified:   "no search this turn",
}
