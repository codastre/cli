package transcript

import (
	"sort"
	"time"
)

// ClassStat is one tool class's footprint across the window, in exact result
// bytes. No ratio is applied: the transcript hands over real payload sizes,
// and turning them into tokens through a divisor would trade a measurement
// for an estimate.
type ClassStat struct {
	Class       string `json:"class"`
	Calls       int    `json:"calls"`
	ResultBytes int64  `json:"result_bytes"`
	Errors      int    `json:"errors"`
}

// OutcomeStat is one outcome's row in the episode breakdown.
type OutcomeStat struct {
	Outcome string `json:"outcome"`
	Label   string `json:"label"`
	EpisodeStat
}

// Tokens are the exact per-session counters, summed over the window.
type Tokens struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	Thinking      int64 `json:"thinking"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
}

// Summary is the local transcript answer. As with the JSONL-log summary, no
// field here is a counterfactual: there is no `saved`, no `baseline`, and no
// figure that multiplies one plane by another.
type Summary struct {
	Window    string `json:"window"`
	Since     string `json:"since,omitempty"`
	Source    string `json:"source"`
	StatePath string `json:"state_path"`

	Sessions   int     `json:"sessions"`
	Projects   int     `json:"projects"`
	Days       int     `json:"days"`
	Turns      int     `json:"turns"`
	CostUSD    float64 `json:"cost_usd"`
	CostedSess int     `json:"costed_sessions"`
	DurationMS int64   `json:"duration_ms"`
	ToolTimeMS int64   `json:"tool_duration_ms"`

	Tokens Tokens `json:"tokens"`
	// ContextAmplification is cache_read / (cache_read + cache_creation +
	// input): the share of ingested context that was a re-read of context
	// already in the window. It is a fact about the session, not a claim
	// about codastre, which is exactly why it is worth printing beside one.
	ContextAmplification float64 `json:"context_amplification"`

	Classes  []ClassStat   `json:"classes"`
	Tools    []ClassStat   `json:"tools"`
	Outcomes []OutcomeStat `json:"outcomes"`

	// AnsweredWithoutFallback is codastre_only over every episode that made a
	// codastre call. Denominator included, deliberately: a rate that hides its
	// denominator reads as marketing.
	AnsweredWithoutFallback float64 `json:"answered_without_fallback"`
	EpisodesWithCodastre    int     `json:"episodes_with_codastre"`
	EpisodesCodastreOnly    int     `json:"episodes_codastre_only"`
	EpisodesUnclassified    int     `json:"episodes_unclassified"`

	Caveat string `json:"caveat"`
}

// CaveatText travels with every transcript figure. Unlike the JSONL log there
// is no ±20% band — these are first-party counts — so the caveat names the
// real limits instead: coverage, and the fact that byte totals are not tokens.
const CaveatText = "Exact first-party counts from Claude Code transcripts (tokens and USD are " +
	"measured, not estimated). Per-tool figures are result bytes, not tokens. " +
	"Claude Code sessions only — other clients are invisible here and are never summed in."

// Summarise folds collected sessions into the printable answer.
func Summarise(sessions []*Session, window, statePath string, since time.Time) Summary {
	s := Summary{
		Window:    window,
		Source:    SourceTranscript,
		StatePath: statePath,
		Sessions:  len(sessions),
		Caveat:    CaveatText,
	}
	if !since.IsZero() {
		s.Since = since.UTC().Format(time.RFC3339)
	}

	projects, days := map[string]bool{}, map[string]bool{}
	byClass := map[string]*ClassStat{}
	byTool := map[string]*ClassStat{}
	byOutcome := map[string]*EpisodeStat{}

	for _, sess := range sessions {
		s.Turns += sess.Turns
		if sess.Project != "" {
			projects[sess.Project] = true
		}
		for _, d := range spanDays(sess.StartedAt, sess.EndedAt) {
			days[d] = true
		}
		if sess.Cost.Present {
			s.CostUSD += sess.Cost.CostUSD
			s.CostedSess++
			s.DurationMS += sess.Cost.DurationMS
			s.ToolTimeMS += sess.Cost.ToolDurationMS
		}
		tok, _ := sess.Tokens()
		s.Tokens.Input += tok.InputTokens
		s.Tokens.Output += tok.OutputTokens
		s.Tokens.Thinking += tok.ThinkingTokens
		s.Tokens.CacheRead += tok.CacheReadTokens
		s.Tokens.CacheCreation += tok.CacheCreationTokens

		for name, st := range sess.ToolMix {
			add(byTool, name, st)
		}
		for class, st := range sess.ClassMix {
			add(byClass, class, st)
		}
		for outcome, st := range sess.Episodes {
			dst := byOutcome[outcome]
			if dst == nil {
				dst = &EpisodeStat{}
				byOutcome[outcome] = dst
			}
			dst.Episodes += st.Episodes
			dst.CodastreCalls += st.CodastreCalls
			dst.CodastreBytes += st.CodastreBytes
			dst.TextSearchCalls += st.TextSearchCalls
			dst.TextSearchBytes += st.TextSearchBytes
			dst.ReadCalls += st.ReadCalls
			dst.ReadBytes += st.ReadBytes
		}
	}

	s.Projects = len(projects)
	s.Days = len(days)
	if denom := s.Tokens.CacheRead + s.Tokens.CacheCreation + s.Tokens.Input; denom > 0 {
		s.ContextAmplification = float64(s.Tokens.CacheRead) / float64(denom)
	}
	s.Classes = flatten(byClass)
	s.Tools = flatten(byTool)

	for _, outcome := range Outcomes {
		st := byOutcome[outcome]
		if st == nil {
			continue
		}
		s.Outcomes = append(s.Outcomes, OutcomeStat{Outcome: outcome, Label: OutcomeLabel[outcome], EpisodeStat: *st})
		switch outcome {
		case OutcomeCodastreOnly:
			s.EpisodesCodastreOnly = st.Episodes
			s.EpisodesWithCodastre += st.Episodes
		case OutcomeFallbackAfter, OutcomeCodastreFailed:
			s.EpisodesWithCodastre += st.Episodes
		case OutcomeUnclassified:
			s.EpisodesUnclassified = st.Episodes
		}
	}
	if s.EpisodesWithCodastre > 0 {
		s.AnsweredWithoutFallback = float64(s.EpisodesCodastreOnly) / float64(s.EpisodesWithCodastre)
	}
	return s
}

func add(m map[string]*ClassStat, key string, st *ToolStat) {
	dst := m[key]
	if dst == nil {
		dst = &ClassStat{Class: key}
		m[key] = dst
	}
	dst.Calls += st.Calls
	dst.ResultBytes += st.ResultBytes
	dst.Errors += st.Errors
}

func flatten(m map[string]*ClassStat) []ClassStat {
	out := make([]ClassStat, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResultBytes != out[j].ResultBytes {
			return out[i].ResultBytes > out[j].ResultBytes
		}
		return out[i].Class < out[j].Class
	})
	return out
}

// spanDays lists the calendar days a session touched, so a long session is
// not counted as one day and a short one straddling midnight is not counted
// as two sessions.
func spanDays(start, end time.Time) []string {
	if start.IsZero() && end.IsZero() {
		return nil
	}
	if start.IsZero() {
		start = end
	}
	if end.Before(start) {
		end = start
	}
	var out []string
	// A runaway range (a clock jump, a malformed timestamp) must not spin
	// here; a month of days is far more than any real session.
	d, i := start.UTC(), 0
	for ; !d.After(end.UTC()) && i < 31; d, i = d.AddDate(0, 0, 1), i+1 {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}
