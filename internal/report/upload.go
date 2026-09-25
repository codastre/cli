package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/codastre/cli/internal/transcript"
)

// Scope names which sessions an upload run reaches.
type Scope string

const (
	// ScopeReport is the standing upload: sessions that started at or after
	// the consent boundary, plus sessions claimed for a study assignment
	// (joining a study is the affirmative per-session consent).
	ScopeReport Scope = "report"
	// ScopeBackfill is the explicit, per-run upload of history (D7):
	// untagged sessions that started before the consent boundary.
	ScopeBackfill Scope = "backfill"
)

// Result is the counters-only summary of one upload run.
type Result struct {
	Scope                 Scope     `json:"scope"`
	ReportSince           time.Time `json:"report_since"`
	ConsentRecorded       bool      `json:"consent_recorded"`
	SessionsEligible      int       `json:"sessions_eligible"`
	SessionsBeforeConsent int       `json:"sessions_before_consent"`
	SessionsAfterBoundary int       `json:"sessions_after_boundary,omitempty"`
	SessionsWithoutCost   int       `json:"sessions_without_cost"`
	StudySessions         int       `json:"study_sessions,omitempty"`
	Episodes              Ack       `json:"episodes"`
	Sessions              Ack       `json:"sessions"`
}

type pendingEpisode struct {
	sess *transcript.Session
	row  EpisodeRow
}

type pendingSession struct {
	sess *transcript.Session
	row  SessionRow
	sig  string
}

// Plan is what one run would send, selected but not yet keyed or sent. Rows
// carry no session_ref: the key is fetched only when there is something to
// send, and a preview never fetches it.
type Plan struct {
	Result
	episodes []pendingEpisode
	sessions []pendingSession
}

// Empty reports whether the plan sends nothing.
func (p *Plan) Empty() bool { return len(p.episodes) == 0 && len(p.sessions) == 0 }

// Select picks what a run of the given scope would send. The boundary is
// state.ReportSince; for a backfill with no boundary recorded yet it is now,
// so a backfill can never reach a session the standing upload would send.
func Select(state *transcript.State, scope Scope, now time.Time) Plan {
	p := Plan{Result: Result{Scope: scope, ReportSince: state.ReportSince}}
	boundary := state.ReportSince
	if boundary.IsZero() {
		boundary = now.UTC()
	}

	ids := make([]string, 0, len(state.Sessions))
	for id := range state.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		s := state.Sessions[id]
		tag := state.Study[id]
		before := s.StartedAt.IsZero() || s.StartedAt.Before(boundary)
		switch scope {
		case ScopeBackfill:
			// A tagged session belongs to the standing upload, which carries
			// its study block; a backfilled row never does.
			if !before || tag != nil || s.StartedAt.IsZero() {
				p.SessionsAfterBoundary++
				continue
			}
		default:
			if before && tag == nil {
				p.SessionsBeforeConsent++
				continue
			}
			if tag != nil {
				p.StudySessions++
			}
		}
		p.SessionsEligible++
		for _, row := range EpisodeRows(s, "", s.Upload.EpisodesThrough) {
			p.episodes = append(p.episodes, pendingEpisode{sess: s, row: row})
		}
		row, ok := BuildSessionRow(s, "", state.Compactions[s.SessionID])
		if !ok {
			p.SessionsWithoutCost++
			continue
		}
		if scope == ScopeReport && tag != nil {
			row.Study = &StudyRef{AssignmentID: tag.AssignmentID, PromptSHA256: tag.PromptSHA256}
		}
		if sig := signature(row); sig != s.Upload.SessionSig {
			p.sessions = append(p.sessions, pendingSession{sess: s, row: row, sig: sig})
		}
	}
	return p
}

// Run uploads whatever is new since the last run. The first run records the
// consent boundary (state.ReportSince = now); an untagged session that
// started before it is never sent here — earlier history is an explicit,
// per-run backfill (D7), not something an upload flag reaches back for.
func Run(ctx context.Context, c *Client, state *transcript.State, now time.Time) (Result, error) {
	consent := false
	if state.ReportSince.IsZero() {
		state.ReportSince = now.UTC()
		consent = true
	}
	p := Select(state, ScopeReport, now)
	p.ConsentRecorded = consent
	return Send(ctx, c, p)
}

// Send keys and uploads a plan. Marks are advanced per successful batch, so a
// failure part-way leaves the state consistent: the caller saves it, and the
// next run resumes. The session key is fetched only when there is something
// to send.
func Send(ctx context.Context, c *Client, p Plan) (Result, error) {
	res := p.Result
	if p.Empty() {
		return res, nil
	}
	key, err := c.SessionKey(ctx)
	if err != nil {
		return res, err
	}
	refs := map[*transcript.Session]string{}
	ref := func(s *transcript.Session) string {
		r, ok := refs[s]
		if !ok {
			r = SessionRef(key, s.SessionID)
			refs[s] = r
		}
		return r
	}

	for start := 0; start < len(p.episodes); start += MaxBatch {
		batch := p.episodes[start:min(start+MaxBatch, len(p.episodes))]
		rows := make([]EpisodeRow, len(batch))
		for i, pe := range batch {
			rows[i] = pe.row
			rows[i].SessionRef = ref(pe.sess)
		}
		ack, err := c.PostEpisodes(ctx, rows)
		if err != nil {
			return res, err
		}
		res.Episodes.add(ack)
		for _, pe := range batch {
			if next := pe.row.EpisodeOrdinal + 1; next > pe.sess.Upload.EpisodesThrough {
				pe.sess.Upload.EpisodesThrough = next
			}
		}
	}

	for start := 0; start < len(p.sessions); start += MaxBatch {
		batch := p.sessions[start:min(start+MaxBatch, len(p.sessions))]
		rows := make([]SessionRow, len(batch))
		for i, ps := range batch {
			rows[i] = ps.row
			rows[i].SessionRef = ref(ps.sess)
		}
		ack, err := c.PostSessions(ctx, rows)
		if err != nil {
			return res, err
		}
		res.Sessions.add(ack)
		for _, ps := range batch {
			ps.sess.Upload.SessionSig = ps.sig
		}
	}
	return res, nil
}

// signature fingerprints a session row (built without its session_ref) so an
// unchanged session is not re-sent.
func signature(row SessionRow) string {
	blob, _ := json.Marshal(row)
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}
