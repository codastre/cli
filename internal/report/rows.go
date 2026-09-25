// Package report builds and sends the opt-in Plane 3 upload: per-turn search
// episodes (POST /v1/usage/episodes) and per-session counters
// (POST /v1/usage/sessions). See docs/plans/m3-plane3-wire-contract.md §7.
//
// The body is the column set and nothing else. Every string on the wire is a
// bounded enum, an HMAC, a timestamp, a version token or a tool name — there
// is no free-text field, so a prompt, a path or a raw session id cannot reach
// the server even by mistake. The raw session id is replaced by
// HMAC-SHA256(per-tenant usage key, id) before anything is serialized.
package report

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"time"

	"github.com/codastre/cli/internal/transcript"
)

// Wire limits. The server accepts ≤ 500 rows but caps the body at 256 KiB,
// which a full 500-episode batch approaches; 200 keeps well clear of the 413.
const (
	MaxBatch    = 200
	maxToolKeys = 200
)

var (
	toolKeyRE       = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)
	clientVersionRE = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,64}$`)
)

// EpisodeRow is one `search_episodes` row as the server accepts it. The
// transcript source carries exact result bytes and no token estimate, so the
// tokens are 0 and both mixes are absent (null), never empty.
type EpisodeRow struct {
	SessionRef       string         `json:"session_ref"`
	EpisodeOrdinal   int            `json:"episode_ordinal"`
	StartedAt        time.Time      `json:"started_at"`
	EndedAt          time.Time      `json:"ended_at"`
	Source           string         `json:"source"`
	Mode             string         `json:"mode"`
	Outcome          string         `json:"outcome"`
	CodastreCalls    int            `json:"codastre_calls"`
	TextSearchCalls  int            `json:"text_search_calls"`
	ReadCalls        int            `json:"read_calls"`
	CodastreTokens   int            `json:"codastre_tokens"`
	TextSearchTokens int            `json:"text_search_tokens"`
	ReadTokens       int            `json:"read_tokens"`
	CodastreBytes    *int64         `json:"codastre_bytes"`
	TextSearchBytes  *int64         `json:"text_search_bytes"`
	ReadBytes        *int64         `json:"read_bytes"`
	PlaneMix         map[string]int `json:"plane_mix"`
	BasisMix         map[string]int `json:"basis_mix"`
	ClientVersion    *string        `json:"client_version"`
}

// ToolUse is one tool's footprint in a session row.
type ToolUse struct {
	Calls       int   `json:"calls"`
	ResultBytes int64 `json:"result_bytes"`
	Errors      int   `json:"errors"`
}

// SessionRow is one `session_metrics` row. Only sessions with a cost-state
// record become one: cost is measured or absent, never modelled.
type SessionRow struct {
	SessionRef          string             `json:"session_ref"`
	Source              string             `json:"source"`
	StartedAt           time.Time          `json:"started_at"`
	EndedAt             time.Time          `json:"ended_at"`
	RepoID              *string            `json:"repo_id"`
	CostUSD             float64            `json:"cost_usd"`
	DurationMS          int64              `json:"duration_ms"`
	ToolDurationMS      *int64             `json:"tool_duration_ms"`
	InputTokens         int64              `json:"input_tokens"`
	OutputTokens        int64              `json:"output_tokens"`
	ThinkingTokens      int64              `json:"thinking_tokens"`
	CacheReadTokens     int64              `json:"cache_read_tokens"`
	CacheCreationTokens int64              `json:"cache_creation_tokens"`
	LinesAdded          *int               `json:"lines_added"`
	LinesRemoved        *int               `json:"lines_removed"`
	CompactionsAuto     int                `json:"compactions_auto"`
	CompactionsManual   int                `json:"compactions_manual"`
	ToolMix             map[string]ToolUse `json:"tool_mix"`
	ClientVersion       *string            `json:"client_version"`
	// Study tags a session claimed for a Plane 4 assignment. Omitted, not
	// null, when absent, so an untagged row is byte-identical to M3's and a
	// server predating studies never sees the key. The arm is not here: the
	// server takes it from the assignment, never from the client.
	Study *StudyRef `json:"study,omitempty"`
}

// StudyRef is the study block on a session row: two ids, no content.
type StudyRef struct {
	AssignmentID string `json:"assignment_id"`
	PromptSHA256 string `json:"prompt_sha256"`
}

// SessionRef hashes a raw session id under the per-tenant usage key.
func SessionRef(key []byte, sessionID string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(sessionID))
	return hex.EncodeToString(m.Sum(nil))
}

// EpisodeRows returns the session's episodes with ordinal >= from. An episode
// without a start time is skipped rather than sent with a zero timestamp.
func EpisodeRows(s *transcript.Session, ref string, from int) []EpisodeRow {
	var out []EpisodeRow
	cv := clientVersion(s.ClientVersion)
	for _, e := range s.EpisodeLog {
		if e.Ordinal < from || e.Outcome == transcript.OutcomeUnclassified || e.StartedAt.IsZero() {
			continue
		}
		ended := e.EndedAt
		if ended.Before(e.StartedAt) {
			ended = e.StartedAt
		}
		cb, tb, rb := e.CodastreBytes, e.TextSearchBytes, e.ReadBytes
		out = append(out, EpisodeRow{
			SessionRef:      ref,
			EpisodeOrdinal:  e.Ordinal,
			StartedAt:       e.StartedAt.UTC(),
			EndedAt:         ended.UTC(),
			Source:          transcript.SourceTranscript,
			Mode:            "unknown",
			Outcome:         e.Outcome,
			CodastreCalls:   e.CodastreCalls,
			TextSearchCalls: e.TextSearchCalls,
			ReadCalls:       e.ReadCalls,
			CodastreBytes:   &cb,
			TextSearchBytes: &tb,
			ReadBytes:       &rb,
			ClientVersion:   cv,
		})
	}
	return out
}

// BuildSessionRow returns the session's row, or false when it has no
// cost-state record (a live session, or one killed before writing it).
func BuildSessionRow(s *transcript.Session, ref string, comp *transcript.Compactions) (SessionRow, bool) {
	if !s.Cost.Present || s.StartedAt.IsZero() {
		return SessionRow{}, false
	}
	ended := s.EndedAt
	if ended.Before(s.StartedAt) {
		ended = s.StartedAt
	}
	c := s.Cost
	toolDur, added, removed := c.ToolDurationMS, c.LinesAdded, c.LinesRemoved
	row := SessionRow{
		SessionRef:          ref,
		Source:              transcript.SourceTranscript,
		StartedAt:           s.StartedAt.UTC(),
		EndedAt:             ended.UTC(),
		CostUSD:             c.CostUSD,
		DurationMS:          c.DurationMS,
		ToolDurationMS:      &toolDur,
		InputTokens:         c.InputTokens,
		OutputTokens:        c.OutputTokens,
		ThinkingTokens:      c.ThinkingTokens,
		CacheReadTokens:     c.CacheReadTokens,
		CacheCreationTokens: c.CacheCreationTokens,
		LinesAdded:          &added,
		LinesRemoved:        &removed,
		ToolMix:             toolMix(s.ToolMix),
		ClientVersion:       clientVersion(s.ClientVersion),
	}
	if comp != nil {
		row.CompactionsAuto, row.CompactionsManual = comp.Auto, comp.Manual
	}
	return row, true
}

// toolMix copies the per-tool footprint. A tool name outside the server's key
// pattern, and anything past the key cap (least-called first), is folded into
// "other" rather than dropped, so the call totals still add up.
func toolMix(in map[string]*transcript.ToolStat) map[string]ToolUse {
	names := make([]string, 0, len(in))
	for n := range in {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if in[names[i]].Calls != in[names[j]].Calls {
			return in[names[i]].Calls > in[names[j]].Calls
		}
		return names[i] < names[j]
	})
	out := map[string]ToolUse{}
	kept := 0 // distinct named keys; one slot is reserved for "other"
	for _, n := range names {
		st := in[n]
		key := "other"
		if n != "other" && toolKeyRE.MatchString(n) && kept < maxToolKeys-1 {
			key = n
			kept++
		}
		u := out[key]
		u.Calls += st.Calls
		u.ResultBytes += st.ResultBytes
		u.Errors += st.Errors
		out[key] = u
	}
	return out
}

func clientVersion(v string) *string {
	if !clientVersionRE.MatchString(v) {
		return nil
	}
	return &v
}
