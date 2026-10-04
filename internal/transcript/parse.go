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
	return ParseFrom(r, offset, nil)
}

// ParseFrom is Parse resuming an already collected session: `prev` supplies
// the turn count (so episode ordinals are absolute) and the context ledger
// (so results ingested in an earlier run keep being carried). prev is only
// read; the increment is returned for the caller to Merge.
func ParseFrom(r io.Reader, offset int64, prev *Session) (*Session, int64, error) {
	s := newSession("")
	p := &parser{session: s, offset: offset, commit: offset, ledger: &Ledger{}}
	if prev != nil {
		p.baseTurns = prev.Turns
		if prev.Ledger != nil {
			p.ledger = prev.Ledger.clone()
		}
	}
	defer func() { s.Ledger = p.ledger }()

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
	s.usageAfterCost = p.usageAfterCost
	s.Cost.Stale = s.Cost.Present && p.usageAfterCost
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
	// lineTS is the timestamp of the line being processed (zero if absent);
	// it bounds the open turn's start and end.
	lineTS time.Time
	// toolNames maps a tool_use id to its class/plane/name, so the result
	// record that arrives later can be attributed without re-reading the call.
	toolNames map[string]toolRef

	// baseTurns is the turn count already collected before offset, which
	// makes an episode's ordinal absolute while the turn is still open.
	baseTurns int
	// seen holds the API message ids already counted in this parse.
	seen map[string]bool
	// ledger is the committed context window; work is the open turn's copy,
	// adopted when the turn closes and dropped if it never does.
	ledger *Ledger
	work   *Ledger
	// usageAfterCost: an API request was seen after the last cost-state
	// record (or with none yet), so that snapshot undercounts the session.
	usageAfterCost bool
}

type toolRef struct {
	name  string
	class string
	// noMatchExit: the command's status comes from a search tool for which
	// exit 1 means "nothing matched".
	noMatchExit bool
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
	p.lineTS = time.Time{}
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		p.lineTS = ts
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
	case "attachment":
		if mode := searchMode(rec.Attachment); mode != ModeUnknown && p.turnOpen {
			p.turn.mode = mode
		}
	}
}

// led is the ledger that the line being processed mutates.
func (p *parser) led() *Ledger {
	if p.turnOpen {
		return p.work
	}
	return p.ledger
}

// episodeOrdinal is the open turn's absolute ordinal, or -1 outside a turn.
func (p *parser) episodeOrdinal() int {
	if !p.turnOpen {
		return -1
	}
	return p.baseTurns + p.session.Turns
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
	for name, m := range rec.ModelUsage {
		c.InputTokens += m.InputTokens
		c.OutputTokens += m.OutputTokens
		c.ThinkingTokens += m.ThinkingTokens
		c.CacheReadTokens += m.CacheReadInputTokens
		c.CacheCreationTokens += m.CacheCreationInputTokens
		if c.Models == nil {
			c.Models = map[string]ModelUsage{}
		}
		c.Models[name] = ModelUsage{
			CostUSD:             m.CostUSD,
			InputTokens:         m.InputTokens,
			OutputTokens:        m.OutputTokens,
			CacheReadTokens:     m.CacheReadInputTokens,
			CacheCreationTokens: m.CacheCreationInputTokens,
		}
	}
	p.session.Cost = c
	p.usageAfterCost = false
}

func (p *parser) assistant(rec record) {
	if rec.Message == nil {
		return
	}
	p.touchTurn()
	if u := rec.Message.Usage; u != nil && p.firstSighting(rec.Message.ID) {
		p.usageAfterCost = true
		m := &p.target().Messages
		m.Messages++
		m.InputTokens += u.InputTokens
		m.OutputTokens += u.OutputTokens
		m.ThinkingTokens += u.OutputTokensDetails.ThinkingTokens
		m.CacheReadTokens += u.CacheReadInputTokens
		m.CacheCreationTokens += u.CacheCreationInputTokens
		// A subagent's requests run in their own context window; only the
		// main thread's prompt is the one search results sit in.
		if !rec.IsSidechain {
			cw5, cw1 := cacheWriteSplit(u)
			w := p.target().cacheWrites(rec.Message.Model)
			w.Ephemeral5m += cw5
			w.Ephemeral1h += cw1
			p.led().request(rec.Message.Model, u, p.target())
		}
	}
	for _, b := range blocks(rec.Message.Content) {
		if b.Type != "tool_use" {
			continue
		}
		class, _ := Classify(b.Name, b.Input.Command)
		if p.toolNames == nil {
			p.toolNames = map[string]toolRef{}
		}
		p.toolNames[b.ID] = toolRef{
			name:        b.Name,
			class:       class,
			noMatchExit: b.Name == "Bash" && exitsOneOnNoMatch(b.Input.Command),
		}
		if b.Name == "Bash" {
			p.target().bash(class).Calls++
		}
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
		p.toolResult(b, rec.ToolUseResult, rec.IsSidechain)
	}
	// A user record carrying tool results is the transcript's plumbing, not a
	// human turn. A meta record (system reminders, hook output) is not one
	// either. Anything else starts a new episode.
	if results == 0 && !rec.IsMeta && !rec.IsSidechain {
		p.closeTurn()
		p.openTurn()
	}
}

func (p *parser) toolResult(b contentBlock, fallbackPayload json.RawMessage, sidechain bool) {
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
	failed := b.IsError
	noMatch := failed && ref.noMatchExit && isExitOne(b.Content)
	if noMatch {
		failed = false
	}
	shellErr := failed && isShellError(b.Content)
	stats := []*ToolStat{p.target().tool(name), p.target().class(classOr(ref.class))}
	if name == "Bash" {
		stats = append(stats, p.target().bash(classOr(ref.class)))
	}
	for _, st := range stats {
		st.ResultBytes += size
		if failed {
			st.Errors++
		}
		if shellErr {
			st.ShellErrors++
		}
		if noMatch {
			st.NoMatch++
		}
	}
	p.countBytes(ref.class, size, failed)
	if !sidechain {
		episode := -1
		if isSearchClass(ref.class) {
			episode = p.episodeOrdinal()
		}
		p.led().result(classOr(ref.class), episode, size)
	}
	p.touchTurn()
	delete(p.toolNames, b.ToolUseID)
}

func isSearchClass(class string) bool {
	return class == ClassCodastre || class == ClassTextSearch || class == ClassRead
}

// firstSighting reports whether an API message id is new to this parse. An
// empty id (an older transcript) always counts.
func (p *parser) firstSighting(id string) bool {
	if id == "" {
		return true
	}
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	if p.seen[id] {
		return false
	}
	p.seen[id] = true
	return true
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
