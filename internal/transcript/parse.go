package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"time"
)

// Parse reads a transcript from `offset` and returns the committed increment
// and the offset to resume from.
//
// The resume offset stops at the first line of an unfinished turn, never past
// it: a turn's tool counters are additive, so committing half a turn and
// re-reading the rest would double-count it. Everything before that boundary
// is exactly-once. The cost-state snapshot is the one exception — it replaces
// rather than adds, so it is applied as soon as it is seen and re-applying it
// on the next run changes nothing.
func Parse(r io.Reader, offset int64) (*Session, int64, error) {
	s := newSession("")
	p := &parser{session: s, offset: offset, commit: offset}

	br := bufio.NewReaderSize(r, 128*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			lineStart := p.offset
			p.closedHere = false
			p.line(line)
			p.offset += int64(len(line))
			switch {
			case !p.turnOpen:
				// Nothing pending: everything read so far is committed.
				p.commit = p.offset
			case p.closedHere:
				// This line ended one turn and began the next, so the open
				// turn starts here. Resuming from this line replays a turn
				// that contributed nothing yet — never one already counted.
				p.commit = lineStart
			}
		} else if len(line) > 0 {
			// Trailing partial line: a transcript being appended to right
			// now. Leave it for the next run.
			break
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, 0, err
		}
	}
	p.abandonTurn()
	return s, p.commit, nil
}

type parser struct {
	session *Session
	// pending holds the open turn's additive counters. They are merged into
	// `session` only when the turn closes, so an unfinished turn at EOF costs
	// nothing and is replayed whole on the next run.
	pending *Session
	offset  int64
	commit  int64

	turnOpen bool
	// closedHere records that the line being processed closed a turn, which
	// is what moves the commit boundary forward.
	closedHere bool
	turn       turnState
	// toolNames maps a tool_use id to its class/plane/name, so the result
	// record that arrives later can be attributed without re-reading the call.
	toolNames map[string]toolRef
}

type toolRef struct {
	name  string
	class string
}

type turnState struct {
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
}

func (p *parser) line(raw []byte) {
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return // truncated or unknown line; never fatal
	}
	s := p.session
	if rec.SessionID != "" {
		s.SessionID = rec.SessionID
	}
	if rec.Cwd != "" {
		s.Cwd = rec.Cwd
	}
	if rec.GitBranch != "" {
		s.GitBranch = rec.GitBranch
	}
	if rec.Version != "" {
		s.ClientVersion = rec.Version
	}
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		if s.StartedAt.IsZero() || ts.Before(s.StartedAt) {
			s.StartedAt = ts
		}
		if ts.After(s.EndedAt) {
			s.EndedAt = ts
		}
	}

	switch rec.Type {
	case "cost-state":
		p.costState(rec)
	case "assistant":
		p.assistant(rec)
	case "user":
		p.user(rec)
	}
}

// target is where additive counters go: the open turn's buffer, or the
// session itself when no turn is open.
func (p *parser) target() *Session {
	if p.turnOpen {
		return p.pending
	}
	return p.session
}

func (p *parser) costState(rec record) {
	// A cost-state record is written when the session wraps up, so the turn in
	// flight is over: close it rather than stranding its counters behind a
	// boundary that will never arrive.
	p.closeTurn()
	c := CostState{
		Present:        true,
		CostUSD:        rec.TotalCostUSD,
		DurationMS:     rec.TotalDuration,
		APIDurationMS:  rec.TotalAPIDuration,
		ToolDurationMS: rec.TotalToolDuration,
		LinesAdded:     rec.TotalLinesAdded,
		LinesRemoved:   rec.TotalLinesRemoved,
	}
	for _, m := range rec.ModelUsage {
		c.InputTokens += m.InputTokens
		c.OutputTokens += m.OutputTokens
		c.ThinkingTokens += m.ThinkingTokens
		c.CacheReadTokens += m.CacheReadInputTokens
		c.CacheCreationTokens += m.CacheCreationInputTokens
	}
	p.session.Cost = c
}

func (p *parser) assistant(rec record) {
	if rec.Message == nil {
		return
	}
	if u := rec.Message.Usage; u != nil {
		m := &p.target().Messages
		m.Messages++
		m.InputTokens += u.InputTokens
		m.OutputTokens += u.OutputTokens
		m.ThinkingTokens += u.OutputTokensDetails.ThinkingTokens
		m.CacheReadTokens += u.CacheReadInputTokens
		m.CacheCreationTokens += u.CacheCreationInputTokens
	}
	for _, b := range blocks(rec.Message.Content) {
		if b.Type != "tool_use" {
			continue
		}
		class, _ := Classify(b.Name, b.Input.Command)
		if p.toolNames == nil {
			p.toolNames = map[string]toolRef{}
		}
		p.toolNames[b.ID] = toolRef{name: b.Name, class: class}
		p.countCall(class)
		p.target().tool(b.Name).Calls++
		p.target().class(class).Calls++
	}
}

func (p *parser) user(rec record) {
	blks := blocks(rec.Message.contentOrNil())
	results := 0
	for _, b := range blks {
		if b.Type != "tool_result" {
			continue
		}
		results++
		p.toolResult(b, rec.ToolUseResult)
	}
	// A user record carrying tool results is the transcript's plumbing, not a
	// human turn. A meta record (system reminders, hook output) is not one
	// either. Anything else starts a new episode.
	if results == 0 && !rec.IsMeta && !rec.IsSidechain {
		p.closeTurn()
		p.openTurn()
	}
}

func (p *parser) toolResult(b contentBlock, fallbackPayload json.RawMessage) {
	ref, known := p.toolNames[b.ToolUseID]
	name := ref.name
	if !known {
		// The call was in an earlier increment, or the transcript is a resume.
		// Count the bytes against an explicit bucket rather than guessing a tool.
		name = "unknown"
	}
	size := int64(len(b.Content))
	if size == 0 {
		size = int64(len(fallbackPayload))
	}
	st := p.target().tool(name)
	st.ResultBytes += size
	cst := p.target().class(classOr(ref.class))
	cst.ResultBytes += size
	if b.IsError {
		st.Errors++
		cst.Errors++
	}
	p.countBytes(ref.class, size, b.IsError)
	delete(p.toolNames, b.ToolUseID)
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

func (p *parser) openTurn() {
	p.turnOpen = true
	p.turn = turnState{}
	p.pending = newSession(p.session.SessionID)
	p.pending.Turns = 1
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
	st := p.pending.episode(outcome)
	st.Episodes++
	st.CodastreCalls += t.codastreCalls
	st.CodastreBytes += t.codastreBytes
	st.TextSearchCalls += t.textCalls
	st.TextSearchBytes += t.textBytes
	st.ReadCalls += t.readCalls
	st.ReadBytes += t.readBytes
	p.turnOpen = false
	p.closedHere = true
	p.session.Merge(p.pending)
	p.pending = nil
}

// abandonTurn drops an unfinished turn at EOF. Its counters stay uncommitted
// and the resume offset points at its first line, so the next run replays it
// whole.
func (p *parser) abandonTurn() {
	p.turnOpen = false
	p.pending = nil
}

// classOr names the bucket for a result whose call was never seen (a resumed
// transcript, or a call from before the watermark).
func classOr(class string) string {
	if class == "" {
		return ClassOther
	}
	return class
}

func blocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var out []contentBlock
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func (m *message) contentOrNil() json.RawMessage {
	if m == nil {
		return nil
	}
	return m.Content
}
