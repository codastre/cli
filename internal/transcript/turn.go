package transcript

import "time"

// turnState is the open turn's search footprint; closeTurn books it as one
// episode.
type turnState struct {
	mode string

	codastreCalls  int
	codastreBytes  int64
	codastreFailed bool
	textCalls      int
	textBytes      int64
	readCalls      int
	readBytes      int64
	// fallback records that a text search or a file read happened *after* a
	// codastre call in the same turn — the two-sided signal.
	fallback bool

	startedAt time.Time
	endedAt   time.Time
}

// countCall records a call against the open turn, opening one if a tool ran
// before any user record did (a resumed transcript starts mid-turn).
func (p *parser) countCall(class string) {
	if !p.turnOpen {
		p.openTurn()
	}
	switch class {
	case ClassCodastre:
		p.turn.codastreCalls++
	case ClassTextSearch:
		p.turn.textCalls++
		if p.turn.codastreCalls > 0 {
			p.turn.fallback = true
		}
	case ClassRead:
		p.turn.readCalls++
		if p.turn.codastreCalls > 0 {
			p.turn.fallback = true
		}
	}
}

func (p *parser) countBytes(class string, size int64, isErr bool) {
	switch class {
	case ClassCodastre:
		p.turn.codastreBytes += size
		if isErr {
			p.turn.codastreFailed = true
		}
	case ClassTextSearch:
		p.turn.textBytes += size
	case ClassRead:
		p.turn.readBytes += size
	}
}

// touchTurn extends the open turn's end to the current line. Only assistant
// and tool-result lines count: the prompt that opens the next turn must not
// stretch the previous one.
func (p *parser) touchTurn() {
	if p.turnOpen && p.lineTS.After(p.turn.endedAt) {
		p.turn.endedAt = p.lineTS
	}
}

func (p *parser) openTurn() {
	p.turnOpen = true
	p.turn = turnState{startedAt: p.lineTS, endedAt: p.lineTS}
	p.pending = newSession(p.session.SessionID)
	p.pending.Turns = 1
	p.work = p.ledger.clone()
}

// closeTurn books the open turn's outcome. A turn that ran no search at all is
// `unclassified` and belongs in no rate — it is volume, not evidence.
func (p *parser) closeTurn() {
	if !p.turnOpen {
		return
	}
	t := p.turn
	outcome := OutcomeUnclassified
	switch {
	case t.codastreFailed:
		outcome = OutcomeCodastreFailed
	case t.codastreCalls > 0 && t.fallback:
		outcome = OutcomeFallbackAfter
	case t.codastreCalls > 0:
		outcome = OutcomeCodastreOnly
	case t.textCalls > 0:
		outcome = OutcomeTextSearchOnly
	}
	if outcome == OutcomeUnclassified {
		// Reads in a turn that ran no search are not an episode; their cost
		// stays in the class totals only.
		ordinal := p.episodeOrdinal()
		p.work.forget(ordinal)
		delete(p.pending.EpisodeCost, ordinal)
	}
	st := p.pending.episode(outcome)
	st.Episodes++
	st.CodastreCalls += t.codastreCalls
	st.CodastreBytes += t.codastreBytes
	st.TextSearchCalls += t.textCalls
	st.TextSearchBytes += t.textBytes
	st.ReadCalls += t.readCalls
	st.ReadBytes += t.readBytes
	if outcome != OutcomeUnclassified {
		// Ordinal 0 within the one-turn increment; Merge rebases it onto the
		// turns already counted.
		p.pending.EpisodeLog = append(p.pending.EpisodeLog, Episode{
			Outcome:         outcome,
			StartedAt:       t.startedAt,
			EndedAt:         t.endedAt,
			CodastreCalls:   t.codastreCalls,
			CodastreBytes:   t.codastreBytes,
			TextSearchCalls: t.textCalls,
			TextSearchBytes: t.textBytes,
			ReadCalls:       t.readCalls,
			ReadBytes:       t.readBytes,
			Mode:            t.mode,
		})
	}
	p.turnOpen = false
	p.closedHere = true
	p.session.Merge(p.pending)
	p.pending = nil
	p.ledger, p.work = p.work, nil
}

// abandonTurn drops an unfinished turn at EOF. Its counters stay uncommitted
// and the resume offset points at its first line, so the next run replays it
// whole.
func (p *parser) abandonTurn() {
	p.turnOpen = false
	p.pending = nil
	p.work = nil
}
