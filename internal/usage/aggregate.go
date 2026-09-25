package usage

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Group keys. Codastre is split by plane because the two are not
// interchangeable: the CLI plane carries one copy of the payload and reaches
// the `agent` rung that a structuredContent-preferring client swallows over
// MCP. Records written before the hook had a `plane` field stay under the
// plain `codastre` key rather than being guessed into one of the two.
const (
	GroupCodastre    = "codastre"
	GroupCodastreMCP = "codastre:mcp"
	GroupCodastreCLI = "codastre:cli"
	GroupTextSearch  = "text-search"
	GroupRead        = "read"
	GroupOther       = "other"
)

// Label is the human name for a group key, matching the plugin receipt so the
// CLI and `/codastre:receipt` say the same thing the same way.
var Label = map[string]string{
	GroupCodastre:    "Codastre (QUERY/GRAPH)",
	GroupCodastreMCP: "Codastre (MCP QUERY/GRAPH)",
	GroupCodastreCLI: "Codastre (CLI plane)",
	GroupTextSearch:  "Text search (grep/glob)",
	GroupRead:        "File reads",
	GroupOther:       "Other",
}

// Group is one row of the receipt: what ran, how often, and how many result
// tokens it put in the context window.
type Group struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Calls  int    `json:"calls"`
	Tokens int    `json:"tokens"`
}

// Reliance is the volume plane, local edition: how much the tool was used and
// how widely. Counts only — no rate, no counterfactual.
type Reliance struct {
	Sessions       int `json:"sessions"`
	Workspaces     int `json:"workspaces"`
	Days           int `json:"days"`
	CodastreCalls  int `json:"codastre_calls"`
	TextSearchCall int `json:"text_search_calls"`
}

// Summary is the whole local answer. Deliberately absent: any field named
// saved, baseline, avoided, or estimated-counterfactual. Adding one is a
// design regression, and usage_test.go fails the build over it.
type Summary struct {
	Window     string   `json:"window"`
	Since      string   `json:"since,omitempty"`
	Source     string   `json:"source"`
	LogPath    string   `json:"log_path"`
	Records    int      `json:"records"`
	Groups     []Group  `json:"groups"`
	TotalCalls int      `json:"total_calls"`
	TotalToken int      `json:"total_tokens"`
	Reliance   Reliance `json:"reliance"`
	Bases      []string `json:"token_bases"`
	Caveat     string   `json:"caveat"`
}

// GroupKeyOf maps a record to its receipt row.
func GroupKeyOf(r Record) string {
	switch r.Class {
	case GroupCodastre:
		if r.Plane == "cli" || r.Plane == "mcp" {
			return GroupCodastre + ":" + r.Plane
		}
		return GroupCodastre
	case GroupTextSearch, GroupRead:
		return r.Class
	case "":
		return GroupOther
	default:
		return r.Class
	}
}

// Aggregate folds records into the printable summary.
func Aggregate(records []Record, window, logPath string, since time.Time) Summary {
	s := Summary{
		Window:  window,
		Source:  "local plugin log (claude-token-log.jsonl)",
		LogPath: logPath,
		Records: len(records),
	}
	if !since.IsZero() {
		s.Since = since.UTC().Format(time.RFC3339)
	}

	byKey := map[string]*Group{}
	sessions, workspaces, days, bases := set{}, set{}, set{}, set{}
	for _, r := range records {
		key := GroupKeyOf(r)
		g := byKey[key]
		if g == nil {
			g = &Group{Key: key, Label: labelFor(key)}
			byKey[key] = g
		}
		g.Calls++
		g.Tokens += r.OutTokens
		s.TotalCalls++
		s.TotalToken += r.OutTokens

		sessions.add(r.SessionID)
		workspaces.add(r.Cwd)
		days.add(day(r.TS))
		// Records predating tok_basis were written with the old flat divisor,
		// which is what `text` now means.
		if r.TokBasis == "" {
			bases.add("text")
		} else {
			bases.add(r.TokBasis)
		}

		switch r.Class {
		case GroupCodastre:
			s.Reliance.CodastreCalls++
		case GroupTextSearch:
			s.Reliance.TextSearchCall++
		}
	}

	s.Groups = make([]Group, 0, len(byKey))
	for _, g := range byKey {
		s.Groups = append(s.Groups, *g)
	}
	sort.Slice(s.Groups, func(i, j int) bool {
		if s.Groups[i].Tokens != s.Groups[j].Tokens {
			return s.Groups[i].Tokens > s.Groups[j].Tokens
		}
		return s.Groups[i].Key < s.Groups[j].Key
	})

	s.Reliance.Sessions = sessions.len()
	s.Reliance.Workspaces = workspaces.len()
	s.Reliance.Days = days.len()
	s.Bases = bases.sortedIn("json", "agent", "text")
	s.Caveat = Caveat(s.Bases)
	return s
}

func labelFor(key string) string {
	if l, ok := Label[key]; ok {
		return l
	}
	return key
}

// basisLabel names each ratio, including the unmeasured default, because a
// receipt that presents `text` beside the measured two implies a standing it
// does not have. See codastre-integrations/core/measurement.md.
var basisLabel = map[string]string{
	"json":  "JSON ~2.5 ch/tok",
	"agent": "agent text ~3.0",
	"text":  "other ~4 (unmeasured)",
}

// Caveat is the provenance line that travels with every token figure here. It
// is not optional output: these are result-size estimates from byte ratios,
// they exclude reasoning tokens, and they are not billing-grade.
func Caveat(bases []string) string {
	used := make([]string, 0, len(bases))
	for _, b := range bases {
		if l, ok := basisLabel[b]; ok {
			used = append(used, l)
		} else {
			used = append(used, b)
		}
	}
	if len(used) == 0 {
		return "(result-size estimate; ±20%, reasoning tokens not included — not billing-grade)"
	}
	return "(result-size estimate — " + strings.Join(used, "; ") +
		"; ±20%, reasoning tokens not included — not billing-grade)"
}

// ParseWindow turns 7d / 30d / all (or any <N>d, <N>h) into a cutoff and a
// label. An empty window means the default, 30d.
func ParseWindow(window string, now time.Time) (time.Time, string, error) {
	w := strings.ToLower(strings.TrimSpace(window))
	if w == "" {
		w = "30d"
	}
	if w == "all" {
		return time.Time{}, "all", nil
	}
	unit := w[len(w)-1:]
	n, err := strconv.Atoi(w[:len(w)-1])
	if err != nil || n <= 0 {
		return time.Time{}, "", fmt.Errorf("invalid --window %q: use 7d, 30d, all, or <N>d", window)
	}
	switch unit {
	case "d":
		return now.Add(-time.Duration(n) * 24 * time.Hour), w, nil
	case "h":
		return now.Add(-time.Duration(n) * time.Hour), w, nil
	default:
		return time.Time{}, "", fmt.Errorf("invalid --window %q: use 7d, 30d, all, or <N>d", window)
	}
}

func day(ts string) string {
	if len(ts) < 10 {
		return ""
	}
	return ts[:10]
}

type set map[string]struct{}

func (s set) add(v string) {
	if v == "" {
		return
	}
	s[v] = struct{}{}
}

func (s set) len() int { return len(s) }

// sortedIn returns the members of s in the given preference order, with any
// unknown member appended alphabetically.
func (s set) sortedIn(order ...string) []string {
	out := make([]string, 0, len(s))
	seen := map[string]bool{}
	for _, o := range order {
		if _, ok := s[o]; ok {
			out = append(out, o)
			seen[o] = true
		}
	}
	rest := make([]string, 0, len(s))
	for k := range s {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}
