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

// Result is the counters-only summary of one upload run.
type Result struct {
	ReportSince           time.Time `json:"report_since"`
	ConsentRecorded       bool      `json:"consent_recorded"`
	SessionsEligible      int       `json:"sessions_eligible"`
	SessionsBeforeConsent int       `json:"sessions_before_consent"`
	SessionsWithoutCost   int       `json:"sessions_without_cost"`
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

// Run uploads whatever is new since the last run. The first run records the
// consent boundary (state.ReportSince = now); a session that started before
// it is never sent — earlier history is an explicit, per-run backfill (D7),
// not something an upload flag reaches back for.
//
// Marks are advanced per successful batch, so a failure part-way leaves the
// state consistent: the caller saves it, and the next run resumes. The
// session key is fetched only when there is something to send.
func Run(ctx context.Context, c *Client, state *transcript.State, now time.Time) (Result, error) {
	var res Result
	if state.ReportSince.IsZero() {
		state.ReportSince = now.UTC()
		res.ConsentRecorded = true
	}
	res.ReportSince = state.ReportSince

	ids := make([]string, 0, len(state.Sessions))
	for id := range state.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var eps []pendingEpisode
	var sessions []pendingSession
	for _, id := range ids {
		s := state.Sessions[id]
		if s.StartedAt.IsZero() || s.StartedAt.Before(state.ReportSince) {
			res.SessionsBeforeConsent++
			continue
		}
		res.SessionsEligible++
		for _, row := range EpisodeRows(s, "", s.Upload.EpisodesThrough) {
			eps = append(eps, pendingEpisode{sess: s, row: row})
		}
		row, ok := BuildSessionRow(s, "", state.Compactions[s.SessionID])
		if !ok {
			res.SessionsWithoutCost++
			continue
		}
		if sig := signature(row); sig != s.Upload.SessionSig {
			sessions = append(sessions, pendingSession{sess: s, row: row, sig: sig})
		}
	}
	if len(eps) == 0 && len(sessions) == 0 {
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

	for start := 0; start < len(eps); start += MaxBatch {
		batch := eps[start:min(start+MaxBatch, len(eps))]
		rows := make([]EpisodeRow, len(batch))
		for i, p := range batch {
			rows[i] = p.row
			rows[i].SessionRef = ref(p.sess)
		}
		ack, err := c.PostEpisodes(ctx, rows)
		if err != nil {
			return res, err
		}
		res.Episodes.add(ack)
		for _, p := range batch {
			if next := p.row.EpisodeOrdinal + 1; next > p.sess.Upload.EpisodesThrough {
				p.sess.Upload.EpisodesThrough = next
			}
		}
	}

	for start := 0; start < len(sessions); start += MaxBatch {
		batch := sessions[start:min(start+MaxBatch, len(sessions))]
		rows := make([]SessionRow, len(batch))
		for i, p := range batch {
			rows[i] = p.row
			rows[i].SessionRef = ref(p.sess)
		}
		ack, err := c.PostSessions(ctx, rows)
		if err != nil {
			return res, err
		}
		res.Sessions.add(ack)
		for _, p := range batch {
			p.sess.Upload.SessionSig = p.sig
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
