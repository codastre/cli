package study

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ArmMetrics is one arm's measured session, or nil while it has not run.
type ArmMetrics struct {
	AssignmentID string   `json:"assignment_id"`
	CostUSD      *float64 `json:"cost_usd"`
	DurationMS   *int64   `json:"duration_ms"`
	OutputTokens *int64   `json:"output_tokens"`
}

// Pair is one row of the study view.
type Pair struct {
	PairRef   string                 `json:"pair_ref"`
	Task      string                 `json:"task"`
	TaskShape string                 `json:"task_shape"`
	Order     *string                `json:"order"`
	Complete  bool                   `json:"complete"`
	Judged    bool                   `json:"judged"`
	Arms      map[string]*ArmMetrics `json:"arms"`
	Correct   map[string]*bool       `json:"correct"`
}

// Tally is a per-pair count split by arm.
type Tally struct {
	Tool   int  `json:"tool"`
	NoTool int  `json:"no_tool"`
	Tie    *int `json:"tie"`
}

// Headline is the pair counts over the pre-registered n, present only once
// the study is publishable.
type Headline struct {
	Of      int   `json:"of"`
	Correct Tally `json:"correct"`
	Cheaper Tally `json:"cheaper"`
	Faster  Tally `json:"faster"`
}

// Receipt is the transparency block, rendered verbatim.
type Receipt struct {
	Formula    string `json:"formula"`
	N          *int   `json:"n"`
	Provenance string `json:"provenance"`
	Caveat     string `json:"caveat"`
}

// View is the subset of `GET /v1/usage/study` the CLI renders.
type View struct {
	Study struct {
		Slug        string `json:"slug"`
		Design      string `json:"design"`
		ToolArmMode string `json:"tool_arm_mode"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
	} `json:"study"`
	Source       string         `json:"source"`
	Pairs        []Pair         `json:"pairs"`
	N            int            `json:"n"`
	TargetN      int            `json:"target_n"`
	Publishable  bool           `json:"publishable"`
	OrderBalance map[string]int `json:"order_balance"`
	ShapeMix     map[string]int `json:"shape_mix"`
	Headline     *Headline      `json:"headline"`
	HeadlineNote string         `json:"headline_note"`
	Receipt      Receipt        `json:"receipt"`
}

// ParseView decodes a study view body.
func ParseView(body []byte) (View, error) {
	var v View
	if err := json.Unmarshal(body, &v); err != nil {
		return View{}, fmt.Errorf("decode study view: %w", err)
	}
	return v, nil
}

// RenderView prints the study as a study: pairs as rows, n against the
// pre-registered target, order balance and task mix, then either the
// headline counts or the server's note on why they are withheld. Nothing is
// averaged here — the server publishes counts of pairs, and so does this.
func RenderView(w io.Writer, v View) {
	fmt.Fprintf(w, "Study %s — %s design, tool arm %s, %s (registered %s)\n",
		v.Study.Slug, v.Study.Design, v.Study.ToolArmMode, v.Study.Status, v.Study.CreatedAt)
	fmt.Fprintf(w, "source: %s\n\n", v.Source)

	if len(v.Pairs) == 0 {
		fmt.Fprintln(w, "  no pairs yet")
	} else {
		fmt.Fprintf(w, "  %-10s %-18s %-11s %-14s %-24s %-24s\n", "pair", "task", "shape", "order", "tool", "no_tool")
		for _, p := range v.Pairs {
			order := "—"
			if p.Order != nil {
				order = *p.Order
			}
			fmt.Fprintf(w, "  %-10s %-18s %-11s %-14s %-24s %-24s\n", short(p.PairRef), p.Task, p.TaskShape,
				order, armCell(p.Arms["tool"], p.Correct["tool"]), armCell(p.Arms["no_tool"], p.Correct["no_tool"]))
		}
	}

	fmt.Fprintf(w, "\nn = %d of %d pre-registered pairs complete and judged\n", v.N, v.TargetN)
	fmt.Fprintf(w, "order balance: %s\n", mapLine(v.OrderBalance))
	fmt.Fprintf(w, "task shapes:   %s\n", mapLine(v.ShapeMix))
	if v.Headline != nil {
		h := v.Headline
		fmt.Fprintf(w, "\nOf %d pairs:\n", h.Of)
		fmt.Fprintf(w, "  correct   tool %d · no_tool %d\n", h.Correct.Tool, h.Correct.NoTool)
		fmt.Fprintf(w, "  cheaper   %s\n", tallyLine(h.Cheaper))
		fmt.Fprintf(w, "  faster    %s\n", tallyLine(h.Faster))
	} else {
		note := v.HeadlineNote
		if note == "" {
			note = "withheld until the pre-registered n is reached"
		}
		fmt.Fprintf(w, "\nheadline: %s\n", note)
	}

	r := v.Receipt
	fmt.Fprintln(w)
	if r.Formula != "" {
		fmt.Fprintf(w, "  formula:    %s\n", r.Formula)
	}
	if r.Provenance != "" {
		fmt.Fprintf(w, "  provenance: %s\n", r.Provenance)
	}
	if r.Caveat != "" {
		fmt.Fprintf(w, "  caveat:     %s\n", r.Caveat)
	}
}

func armCell(a *ArmMetrics, correct *bool) string {
	if a == nil {
		return "not run"
	}
	parts := []string{}
	if a.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("$%.3f", *a.CostUSD))
	}
	if a.DurationMS != nil {
		parts = append(parts, (time.Duration(*a.DurationMS) * time.Millisecond).Round(time.Second).String())
	}
	switch {
	case correct == nil:
		parts = append(parts, "unjudged")
	case *correct:
		parts = append(parts, "correct")
	default:
		parts = append(parts, "wrong")
	}
	return strings.Join(parts, " ")
}

func tallyLine(t Tally) string {
	s := fmt.Sprintf("tool %d · no_tool %d", t.Tool, t.NoTool)
	if t.Tie != nil {
		s += fmt.Sprintf(" · tie %d", *t.Tie)
	}
	return s
}

func mapLine(m map[string]int) string {
	if len(m) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, " · ")
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// RenderBlind prints the judge's queue. It has no arm to print, by design.
func RenderBlind(w io.Writer, b Blind) {
	fmt.Fprintf(w, "Blind queue for %s — judge each diff against its acceptance criterion, not knowing the arm\n", b.Study)
	if len(b.Items) == 0 {
		fmt.Fprintln(w, "  nothing to judge yet")
		return
	}
	for _, it := range b.Items {
		state := "pending"
		switch {
		case !it.Bound:
			state = "not run yet"
		case it.Correct != nil && *it.Correct:
			state = "judged correct"
		case it.Correct != nil:
			state = "judged incorrect"
		}
		fmt.Fprintf(w, "\n  %s  task %s  [%s]\n", it.AssignmentID, it.Task, state)
		fmt.Fprintf(w, "    acceptance (sha256 %s):\n", short(it.AcceptanceSHA256))
		for _, line := range strings.Split(strings.TrimSpace(it.Acceptance), "\n") {
			fmt.Fprintf(w, "      %s\n", line)
		}
	}
}
