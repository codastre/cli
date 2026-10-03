package transcript

import "sort"

// Search-cost attribution.
//
// A tool result is paid for twice. It is *ingested* once — the next request
// takes it in as new, uncached prompt — and then *carried*: every later
// request re-reads it, usually from cache, until a compaction drops it. Carry
// usually dominates: a 50 KB grep early in a long session is re-read on every
// request after it.
//
// The transcript gives exact per-request usage but never prices a tool call,
// so the split is attributed, not measured:
//
//   - ingest tokens = the prompt's growth since the previous request, minus
//     that request's own output (which is now part of the prompt), shared
//     among the results that arrived in between by their byte sizes;
//   - carry tokens  = each result's ingest tokens, once per later request,
//     until the prompt shrinks (a compaction).
//
// Tokens become money through *price ratios*, not prices: relative to one
// uncached input token, a 5-minute cache write is 1.25×, a 1-hour write 2×,
// a cache read 0.1× and an output token 5×. Those ratios are the same for
// every Claude model. The absolute price per model is then recovered from the
// session's own measured cost-state, so attributed dollars always reconcile
// to Claude Code's figure and no price constant exists here.
const (
	weightInput        = 1.0
	weightCacheWrite5m = 1.25
	weightCacheWrite1h = 2.0
	weightCacheRead    = 0.1
	weightOutput       = 5.0

	// compactionShrink: a request whose prompt is smaller than this fraction
	// of the previous one has had its context rebuilt. Real tool loops never
	// shrink the prompt, so the threshold only has to separate "grew" from
	// "summarised".
	compactionShrink = 0.6
)

// Cost is one class's (or one episode's) attributed share of a session.
// Units are input-token equivalents per model; USD is derived from them in
// Finalize and is absent when the session has no cost-state yet.
type Cost struct {
	IngestTokens int64              `json:"ingest_tokens"`
	CarryTokens  int64              `json:"carry_tokens"`
	Units        map[string]float64 `json:"units,omitempty"`
	USD          *float64           `json:"usd,omitempty"`
}

func (c *Cost) charge(model string, units float64) {
	if c.Units == nil {
		c.Units = map[string]float64{}
	}
	c.Units[model] += units
}

func (c *Cost) add(o *Cost) {
	c.IngestTokens += o.IngestTokens
	c.CarryTokens += o.CarryTokens
	for m, u := range o.Units {
		c.charge(m, u)
	}
}

// CacheWrites tallies one model's cache writes by TTL, so the cost-state's
// undivided cache-creation total can be weighted with the right ratio.
type CacheWrites struct {
	Ephemeral5m int64 `json:"ephemeral_5m"`
	Ephemeral1h int64 `json:"ephemeral_1h"`
}

// ModelUsage is one model's slice of the cost-state, kept so a per-model
// price can be recovered (see Finalize).
type ModelUsage struct {
	CostUSD             float64 `json:"cost_usd"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
}

type pendingResult struct {
	Class string `json:"class"`
	// Episode is the absolute turn ordinal for a search-class result, -1 for
	// any other.
	Episode int   `json:"episode"`
	Bytes   int64 `json:"bytes"`
}

// Ledger is what is in the context window right now, by class and by
// episode. It is a snapshot carried across incremental parses (a result
// ingested in one run is still carried in the next), so Merge replaces it.
type Ledger struct {
	Prompt   int64            `json:"prompt"`
	Output   int64            `json:"output"`
	Classes  map[string]int64 `json:"classes,omitempty"`
	Episodes map[int]int64    `json:"episodes,omitempty"`
	Pending  []pendingResult  `json:"pending,omitempty"`
}

func (l *Ledger) clone() *Ledger {
	out := &Ledger{Prompt: l.Prompt, Output: l.Output, Classes: map[string]int64{}, Episodes: map[int]int64{}}
	for k, v := range l.Classes {
		out.Classes[k] = v
	}
	for k, v := range l.Episodes {
		out.Episodes[k] = v
	}
	out.Pending = append(out.Pending, l.Pending...)
	return out
}

func (l *Ledger) result(class string, episode int, size int64) {
	if size > 0 {
		l.Pending = append(l.Pending, pendingResult{Class: class, Episode: episode, Bytes: size})
	}
}

// request books one API request: everything already live is carried at the
// request's blended prompt rate, then the results that arrived since the
// previous request are ingested at its uncached rate.
func (l *Ledger) request(model string, u *usage, sink *Session) {
	cw5, cw1 := cacheWriteSplit(u)
	prompt := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	if prompt == 0 {
		return
	}
	if l.Prompt > 0 && float64(prompt) < float64(l.Prompt)*compactionShrink {
		l.Classes, l.Episodes = map[string]int64{}, map[int]int64{}
	}
	fresh := float64(u.InputTokens)*weightInput + float64(cw5)*weightCacheWrite5m + float64(cw1)*weightCacheWrite1h
	rate := (fresh + float64(u.CacheReadInputTokens)*weightCacheRead) / float64(prompt)

	for class, tok := range l.Classes {
		c := sink.classCost(class)
		c.CarryTokens += tok
		c.charge(model, float64(tok)*rate)
	}
	for ep, tok := range l.Episodes {
		c := sink.episodeCost(ep)
		c.CarryTokens += tok
		c.charge(model, float64(tok)*rate)
	}

	if growth := prompt - l.Prompt - l.Output; len(l.Pending) > 0 && l.Prompt > 0 && growth > 0 {
		freshRate := rate
		if n := u.InputTokens + u.CacheCreationInputTokens; n > 0 {
			freshRate = fresh / float64(n)
		}
		var total int64
		for _, r := range l.Pending {
			total += r.Bytes
		}
		for _, r := range l.Pending {
			// A token is never smaller than a byte, which bounds the share a
			// tiny result can absorb from growth it did not cause (hook
			// context, reminders).
			tok := min(growth*r.Bytes/total, r.Bytes)
			c := sink.classCost(r.Class)
			c.IngestTokens += tok
			c.charge(model, float64(tok)*freshRate)
			if l.Classes == nil {
				l.Classes = map[string]int64{}
			}
			l.Classes[r.Class] += tok
			if r.Episode >= 0 {
				e := sink.episodeCost(r.Episode)
				e.IngestTokens += tok
				e.charge(model, float64(tok)*freshRate)
				if l.Episodes == nil {
					l.Episodes = map[int]int64{}
				}
				l.Episodes[r.Episode] += tok
			}
		}
	}
	l.Pending = nil
	l.Prompt = prompt
	l.Output = u.OutputTokens
}

// forget drops an episode that turned out not to be one (a turn that ran no
// search): its tokens stay in the class totals, which is where they belong.
func (l *Ledger) forget(episode int) { delete(l.Episodes, episode) }

// cacheWriteSplit divides a request's cache writes by TTL. Without the
// breakdown the API default (5 minutes) is assumed.
func cacheWriteSplit(u *usage) (cw5, cw1 int64) {
	if u.CacheCreation == nil {
		return u.CacheCreationInputTokens, 0
	}
	cw1 = u.CacheCreation.Ephemeral1h
	return max(u.CacheCreationInputTokens-cw1, 0), cw1
}

// Finalize derives the USD fields from the units. A model's price per unit
// is its measured cost-state cost over its weighted cost-state tokens. With
// no cost-state, or a unit charged to a model the cost-state does not price,
// USD stays absent rather than guessed.
func (s *Session) Finalize() {
	price := map[string]float64{}
	if s.Cost.Present {
		for model, m := range s.Cost.Models {
			cwWeight := weightCacheWrite5m
			if w := s.CacheWrites[model]; w != nil && w.Ephemeral5m+w.Ephemeral1h > 0 {
				cwWeight = (float64(w.Ephemeral5m)*weightCacheWrite5m + float64(w.Ephemeral1h)*weightCacheWrite1h) /
					float64(w.Ephemeral5m+w.Ephemeral1h)
			}
			units := float64(m.InputTokens)*weightInput + float64(m.CacheCreationTokens)*cwWeight +
				float64(m.CacheReadTokens)*weightCacheRead + float64(m.OutputTokens)*weightOutput
			if units > 0 && m.CostUSD > 0 {
				price[model] = m.CostUSD / units
			}
		}
	}
	for _, c := range s.CostByClass {
		c.USD = priced(c, price)
	}
	for _, c := range s.EpisodeCost {
		c.USD = priced(c, price)
	}
}

func priced(c *Cost, price map[string]float64) *float64 {
	if len(c.Units) == 0 {
		return nil
	}
	models := make([]string, 0, len(c.Units))
	for m := range c.Units {
		models = append(models, m)
	}
	sort.Strings(models)
	usd := 0.0
	for _, m := range models {
		p, ok := price[m]
		if !ok {
			return nil
		}
		usd += c.Units[m] * p
	}
	return &usd
}
