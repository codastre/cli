package transcript

import "time"

// ToolStat is one tool's footprint in a session: how often it ran, how many
// bytes of result it put back into the context window, and how often it
// failed. Bytes, not tokens — the transcript gives exact result payloads, and
// converting them through a ratio would throw away the one thing this source
// has that the JSONL log does not.
type ToolStat struct {
	Calls       int   `json:"calls"`
	ResultBytes int64 `json:"result_bytes"`
	// Errors are failed calls. A search that exited 1 because nothing matched
	// is not one — Claude Code flags it as an error, but it is counted in
	// NoMatch instead (see exitsOneOnNoMatch). ShellErrors is the subset of
	// Errors the shell raised itself (see isShellError).
	Errors      int `json:"errors"`
	ShellErrors int `json:"shell_errors,omitempty"`
	NoMatch     int `json:"no_match,omitempty"`
}

func (t *ToolStat) add(o *ToolStat) {
	t.Calls += o.Calls
	t.ResultBytes += o.ResultBytes
	t.Errors += o.Errors
	t.ShellErrors += o.ShellErrors
	t.NoMatch += o.NoMatch
}

// CostState is the transcript's own cumulative accounting record. It is a
// snapshot, not a delta, so an incremental re-parse replaces it rather than
// adding to it.
//
// CostUSD is measured — it is Claude Code's own figure, already priced with
// the cache discount. No pricing constant exists anywhere in this design.
//
// Claude Code writes one whenever its process exits, so a session that was
// restarted and is still running carries an early snapshot followed by more
// requests. Stale marks that: the snapshot is real but no longer the
// session's total, and nothing may use it as a denominator until the next
// one arrives. Its per-model prices (Finalize) are still valid.
type CostState struct {
	Present             bool    `json:"present"`
	Stale               bool    `json:"stale,omitempty"`
	CostUSD             float64 `json:"cost_usd"`
	DurationMS          int64   `json:"duration_ms"`
	APIDurationMS       int64   `json:"api_duration_ms"`
	ToolDurationMS      int64   `json:"tool_duration_ms"`
	LinesAdded          int     `json:"lines_added"`
	LinesRemoved        int     `json:"lines_removed"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ThinkingTokens      int64   `json:"thinking_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	// Models is the same record split per model, which is what lets an
	// attributed share be priced (Finalize).
	Models map[string]ModelUsage `json:"models,omitempty"`
}

// MessageTotals sums the per-request `usage` blocks, once per API message id
// (Claude Code repeats a request's usage on every content-block record). It
// is additive across incremental parses, and it is the fallback for a session
// whose transcript carries no cost-state record yet (a live session, or one
// killed before it wrote one). It cannot produce USD — only the cost-state
// can, and nothing here models a price.
type MessageTotals struct {
	Messages            int   `json:"messages"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ThinkingTokens      int64 `json:"thinking_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

// EpisodeStat aggregates the turns that ended in one outcome. Individual
// episodes are not stored: the counts are what any report needs, and a
// per-episode row would grow the state file without answering a question.
type EpisodeStat struct {
	Episodes        int   `json:"episodes"`
	CodastreCalls   int   `json:"codastre_calls"`
	CodastreBytes   int64 `json:"codastre_bytes"`
	TextSearchCalls int   `json:"text_search_calls"`
	TextSearchBytes int64 `json:"text_search_bytes"`
	ReadCalls       int   `json:"read_calls"`
	ReadBytes       int64 `json:"read_bytes"`
}

// Session is everything one transcript contributes. `Source` is always
// "transcript" here; the field exists because the design forbids summing
// across sources and a row must say which world it came from.
type Session struct {
	SessionID     string               `json:"session_id"`
	Project       string               `json:"project"`
	Cwd           string               `json:"cwd"`
	GitBranch     string               `json:"git_branch"`
	ClientVersion string               `json:"client_version"`
	Source        string               `json:"source"`
	StartedAt     time.Time            `json:"started_at"`
	EndedAt       time.Time            `json:"ended_at"`
	Cost          CostState            `json:"cost_state"`
	Messages      MessageTotals        `json:"messages"`
	ToolMix       map[string]*ToolStat `json:"tool_mix"`
	// ClassMix is the same footprint grouped by class rather than by tool
	// name. It is kept separately because one tool name is not one class:
	// `Bash` is a codastre call, a grep, or neither, depending on the command
	// — which only the parse can see.
	ClassMix map[string]*ToolStat `json:"class_mix"`
	// BashByClass splits the Bash row of ToolMix by class, so a report can
	// tell a `codastre query` from a grep from a `git status`. Local only:
	// the uploaded tool_mix keeps plain tool names.
	BashByClass map[string]*ToolStat    `json:"bash_by_class,omitempty"`
	Episodes    map[string]*EpisodeStat `json:"episodes"`
	Turns       int                     `json:"turns"`
	// CostByClass and EpisodeCost are the attributed search cost (cost.go),
	// the latter keyed by episode ordinal. Both are local only.
	CostByClass map[string]*Cost `json:"cost_by_class,omitempty"`
	EpisodeCost map[int]*Cost    `json:"episode_cost,omitempty"`
	// PromptTokens sums every main-thread request's prompt (input + cache
	// read + cache write): the token total that CostByClass's ingest and carry
	// are slices of, so a class's share of it is a like-for-like ratio.
	// Subagent requests are excluded for the same reason they are not
	// attributed — their prompt is a different context window.
	PromptTokens int64 `json:"prompt_tokens,omitempty"`
	// CacheWrites is the per-model TTL split of cache writes, observed in the
	// transcript's own requests.
	CacheWrites map[string]*CacheWrites `json:"cache_writes,omitempty"`
	// Ledger is the context-window snapshot attribution resumes from.
	Ledger *Ledger `json:"ledger,omitempty"`
	// EpisodeLog holds the individual search episodes — turns whose outcome
	// is not `unclassified`. A turn that ran no search is volume, not an
	// episode, and is counted in Turns only.
	EpisodeLog []Episode `json:"episode_log,omitempty"`
	// Upload is what has already been reported to the server.
	Upload UploadMark `json:"upload"`

	// usageAfterCost records that this increment saw an API request after
	// its last cost-state (or saw one and no cost-state at all), which makes
	// the merged snapshot stale. Parse-time only; never persisted.
	usageAfterCost bool
}

// SourceTranscript is the `source` value every session parsed here carries.
const SourceTranscript = "transcript"

func newSession(id string) *Session {
	return &Session{
		SessionID: id,
		Source:    SourceTranscript,
		ToolMix:   map[string]*ToolStat{},
		ClassMix:  map[string]*ToolStat{},
		Episodes:  map[string]*EpisodeStat{},
	}
}

// tool returns the stat bucket for a tool name, creating it on first use.
func (s *Session) tool(name string) *ToolStat {
	st := s.ToolMix[name]
	if st == nil {
		st = &ToolStat{}
		s.ToolMix[name] = st
	}
	return st
}

// class returns the stat bucket for a tool class, creating it on first use.
func (s *Session) class(name string) *ToolStat {
	if s.ClassMix == nil {
		s.ClassMix = map[string]*ToolStat{}
	}
	st := s.ClassMix[name]
	if st == nil {
		st = &ToolStat{}
		s.ClassMix[name] = st
	}
	return st
}

// bash returns the Bash stat bucket for a class, creating it on first use.
func (s *Session) bash(class string) *ToolStat {
	if s.BashByClass == nil {
		s.BashByClass = map[string]*ToolStat{}
	}
	st := s.BashByClass[class]
	if st == nil {
		st = &ToolStat{}
		s.BashByClass[class] = st
	}
	return st
}

func (s *Session) classCost(class string) *Cost {
	if s.CostByClass == nil {
		s.CostByClass = map[string]*Cost{}
	}
	c := s.CostByClass[class]
	if c == nil {
		c = &Cost{}
		s.CostByClass[class] = c
	}
	return c
}

func (s *Session) episodeCost(ordinal int) *Cost {
	if s.EpisodeCost == nil {
		s.EpisodeCost = map[int]*Cost{}
	}
	c := s.EpisodeCost[ordinal]
	if c == nil {
		c = &Cost{}
		s.EpisodeCost[ordinal] = c
	}
	return c
}

func (s *Session) cacheWrites(model string) *CacheWrites {
	if s.CacheWrites == nil {
		s.CacheWrites = map[string]*CacheWrites{}
	}
	w := s.CacheWrites[model]
	if w == nil {
		w = &CacheWrites{}
		s.CacheWrites[model] = w
	}
	return w
}

// episode returns the stat bucket for an outcome, creating it on first use.
func (s *Session) episode(outcome string) *EpisodeStat {
	st := s.Episodes[outcome]
	if st == nil {
		st = &EpisodeStat{}
		s.Episodes[outcome] = st
	}
	return st
}

// Merge folds a newly parsed increment into an existing session. Additive
// fields add; the cost-state snapshot replaces, because it is cumulative and
// adding two snapshots would double-count a session's whole spend.
func (s *Session) Merge(inc *Session) {
	if inc == nil {
		return
	}
	if s.SessionID == "" {
		s.SessionID = inc.SessionID
	}
	if inc.Project != "" {
		s.Project = inc.Project
	}
	if inc.Cwd != "" {
		s.Cwd = inc.Cwd
	}
	if inc.GitBranch != "" {
		s.GitBranch = inc.GitBranch
	}
	if inc.ClientVersion != "" {
		s.ClientVersion = inc.ClientVersion
	}
	s.Source = SourceTranscript
	if !inc.StartedAt.IsZero() && (s.StartedAt.IsZero() || inc.StartedAt.Before(s.StartedAt)) {
		s.StartedAt = inc.StartedAt
	}
	if inc.EndedAt.After(s.EndedAt) {
		s.EndedAt = inc.EndedAt
	}
	if inc.Cost.Present {
		s.Cost = inc.Cost
	} else if inc.usageAfterCost && s.Cost.Present {
		s.Cost.Stale = true
	}

	s.Messages.Messages += inc.Messages.Messages
	s.Messages.InputTokens += inc.Messages.InputTokens
	s.Messages.OutputTokens += inc.Messages.OutputTokens
	s.Messages.ThinkingTokens += inc.Messages.ThinkingTokens
	s.Messages.CacheReadTokens += inc.Messages.CacheReadTokens
	s.Messages.CacheCreationTokens += inc.Messages.CacheCreationTokens
	s.appendEpisodes(inc, s.Turns)
	s.Turns += inc.Turns

	if s.ToolMix == nil {
		s.ToolMix = map[string]*ToolStat{}
	}
	for name, st := range inc.ToolMix {
		s.tool(name).add(st)
	}
	for name, st := range inc.ClassMix {
		s.class(name).add(st)
	}
	for class, st := range inc.BashByClass {
		s.bash(class).add(st)
	}
	for class, c := range inc.CostByClass {
		s.classCost(class).add(c)
	}
	s.PromptTokens += inc.PromptTokens
	for ordinal, c := range inc.EpisodeCost {
		s.episodeCost(ordinal).add(c)
	}
	for model, w := range inc.CacheWrites {
		dst := s.cacheWrites(model)
		dst.Ephemeral5m += w.Ephemeral5m
		dst.Ephemeral1h += w.Ephemeral1h
	}
	if inc.Ledger != nil {
		s.Ledger = inc.Ledger
	}
	if s.Episodes == nil {
		s.Episodes = map[string]*EpisodeStat{}
	}
	for outcome, st := range inc.Episodes {
		dst := s.episode(outcome)
		dst.Episodes += st.Episodes
		dst.CodastreCalls += st.CodastreCalls
		dst.CodastreBytes += st.CodastreBytes
		dst.TextSearchCalls += st.TextSearchCalls
		dst.TextSearchBytes += st.TextSearchBytes
		dst.ReadCalls += st.ReadCalls
		dst.ReadBytes += st.ReadBytes
	}
}

// Tokens returns the session's exact token totals, preferring the cost-state
// snapshot and falling back to the summed per-message usage. The second
// return names which one answered, because the two must never be mixed in a
// report that does not say so.
func (s *Session) Tokens() (MessageTotals, string) {
	if s.Cost.Present {
		return MessageTotals{
			InputTokens:         s.Cost.InputTokens,
			OutputTokens:        s.Cost.OutputTokens,
			ThinkingTokens:      s.Cost.ThinkingTokens,
			CacheReadTokens:     s.Cost.CacheReadTokens,
			CacheCreationTokens: s.Cost.CacheCreationTokens,
			Messages:            s.Messages.Messages,
		}, "cost_state"
	}
	return s.Messages, "messages"
}
