package report

import (
	"sort"
	"time"
)

// PlaceholderRef stands in for session_ref in a preview. The real value is
// HMAC-SHA256 under the tenant's usage key, which a preview does not fetch:
// fetching it is a server call, and a preview makes none.
const PlaceholderRef = "hmac-sha256(<tenant usage key>, <session id>)"

// PreviewSession is one session's footprint in a plan, for display.
type PreviewSession struct {
	StartedAt  time.Time `json:"started_at"`
	Episodes   int       `json:"episodes"`
	HasSession bool      `json:"session_row"`
	CostUSD    float64   `json:"cost_usd"`
	DurationMS int64     `json:"duration_ms"`
}

// Preview is exactly what a plan would put on the wire, with the session_ref
// placeholder in place of the HMAC.
type Preview struct {
	Result
	Sessions    []PreviewSession `json:"sessions"`
	EpisodeRows []EpisodeRow     `json:"episode_rows"`
	SessionRows []SessionRow     `json:"session_rows"`
}

// Preview renders the plan without keying or sending it.
func (p *Plan) Preview() Preview {
	out := Preview{Result: p.Result, EpisodeRows: []EpisodeRow{}, SessionRows: []SessionRow{}}
	bySession := map[string]*PreviewSession{}
	get := func(id string, started time.Time) *PreviewSession {
		ps := bySession[id]
		if ps == nil {
			ps = &PreviewSession{StartedAt: started}
			bySession[id] = ps
		}
		return ps
	}
	for _, pe := range p.episodes {
		row := pe.row
		row.SessionRef = PlaceholderRef
		out.EpisodeRows = append(out.EpisodeRows, row)
		get(pe.sess.SessionID, pe.sess.StartedAt).Episodes++
	}
	for _, ps := range p.sessions {
		row := ps.row
		row.SessionRef = PlaceholderRef
		out.SessionRows = append(out.SessionRows, row)
		v := get(ps.sess.SessionID, ps.sess.StartedAt)
		v.HasSession, v.CostUSD, v.DurationMS = true, row.CostUSD, row.DurationMS
	}
	for _, v := range bySession {
		out.Sessions = append(out.Sessions, *v)
	}
	sort.Slice(out.Sessions, func(i, j int) bool {
		return out.Sessions[i].StartedAt.Before(out.Sessions[j].StartedAt)
	})
	return out
}
