package transcript

import "time"

// Episode is one turn's search footprint, kept individually so it can be
// reported to the server as one `search_episodes` row. It holds counts, byte
// totals and two timestamps — no prompt, no tool input, no path.
//
// Ordinal is the turn's index within its session, counted across incremental
// parses (see Session.Merge), so the same turn gets the same ordinal on every
// run and the server's (session_ref, source, ordinal) key stays idempotent.
type Episode struct {
	Ordinal         int       `json:"ordinal"`
	Outcome         string    `json:"outcome"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	CodastreCalls   int       `json:"codastre_calls"`
	CodastreBytes   int64     `json:"codastre_bytes"`
	TextSearchCalls int       `json:"text_search_calls"`
	TextSearchBytes int64     `json:"text_search_bytes"`
	ReadCalls       int       `json:"read_calls"`
	ReadBytes       int64     `json:"read_bytes"`
}

// Compactions counts context compactions for one session, as observed by the
// PreCompact hook. A transcript records none of them, so this is the only
// source; a session with no hook listening reports zero, not unknown.
type Compactions struct {
	Auto   int `json:"auto"`
	Manual int `json:"manual"`
}

// UploadMark records what of a session has already been reported, so a
// re-run of `codastre collect --upload` sends only what is new. The server is
// idempotent regardless; this only saves the round trips.
type UploadMark struct {
	// EpisodesThrough is one past the highest ordinal uploaded.
	EpisodesThrough int `json:"episodes_through"`
	// SessionSig fingerprints the last uploaded session payload; a live
	// session that grew (or gained a compaction) differs and is re-sent.
	SessionSig string `json:"session_sig,omitempty"`
}

// appendEpisodes folds an increment's episodes into s, rebasing their
// ordinals onto the turns s had already counted.
func (s *Session) appendEpisodes(inc *Session, base int) {
	for _, e := range inc.EpisodeLog {
		e.Ordinal += base
		s.EpisodeLog = append(s.EpisodeLog, e)
	}
}
