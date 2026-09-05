package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// renderContracts prints a contract report. Two shapes, same data:
//
//   - human: the scope, the counts, then the contracts grouped by status and
//     kind with every party's location;
//   - agent: the same ordering, one line per contract, party counts and repos
//     only.
//
// Both lead with the scope block, and both write it to w rather than stderr.
// That placement is the point of the block: an orphan-heavy report over a
// single repo looks identical to a real finding, so the caveat has to travel
// with the answer — down a pipe, into a log, in front of an agent that only
// reads stdout — not beside it.
func renderContracts(
	w io.Writer,
	payload json.RawMessage,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
	agent bool,
) error {
	env, ok := decodeContracts(payload)
	if !ok {
		return fmt.Errorf("decode CONTRACTS response")
	}

	renderContractScope(w, env, agent)

	if len(env.Contracts) == 0 {
		// Not an error: "no contracts of this status in scope" is a valid answer.
		// The scope and counts above it are what make the zero credible rather
		// than suspicious — which of the two it is, the warnings just said.
		fmt.Fprintf(w, "no contracts matched this filter (%d assembled in scope)\n", env.Total)
		return nil
	}

	groups := groupContracts(env.Contracts)
	if agent {
		renderContractsAgent(w, groups)
		return nil
	}
	renderContractsHuman(w, env, groups, unmask)
	return nil
}

// ── scope ───────────────────────────────────────────────────────────────────

// contractWarningCopy mirrors the dashboard's WARNING_COPY
// (dashboard/src/api/contracts.ts): each warning names the scope problem and
// what to do about it, never just "no results". An unknown code (a newer
// server) still prints, bare.
var contractWarningCopy = map[string]struct{ title, detail string }{
	"single_repo_scope": {
		"only one repo is in scope",
		"a contract is a boundary between repos, so nothing here can match — index or gain access to the services on the other side",
	},
	"endpoints_in_one_repo": {
		"all endpoints come from a single repo",
		"more than one repo is visible, but only one has extracted endpoints — every orphan below is explained by that, not by missing code",
	},
	"no_endpoints": {
		"no contract endpoints extracted",
		"no HTTP routes or Kafka topics were found — graph extraction may not have run yet, or no extractor covers these languages",
	},
	"truncated": {
		"the endpoint set was truncated",
		"some contracts are incomplete, so treat orphan verdicts below as unreliable and narrow the filters",
	},
}

func renderContractScope(w io.Writer, env contractsEnvelope, agent bool) {
	reach := "cross-repo matches reachable"
	if !env.Scope.CrossRepoPossible {
		reach = "cross-repo matches NOT reachable in this scope"
	}
	fmt.Fprintf(w, "scope: %d repo(s) visible · %d with endpoints · %d endpoint(s) read · %s\n",
		env.Scope.VisibleRepos, env.Scope.ReposWithEndpoints, env.Scope.EndpointsRead, reach)

	for _, code := range env.Scope.Warnings {
		text, known := contractWarningCopy[code]
		switch {
		case !known:
			fmt.Fprintf(w, "warning: %s\n", code)
		case agent:
			// The code is what an agent branches on; the title says why in six
			// words. The full remedy prose is human output's job.
			fmt.Fprintf(w, "warning: %s — %s\n", code, text.title)
		default:
			fmt.Fprintf(w, "warning: %s: %s\n", text.title, text.detail)
		}
	}

	fmt.Fprintf(w, "counts: %s (total %d, showing %d)\n",
		contractCountsLine(env.Counts), env.Total, len(env.Contracts))
	fmt.Fprintln(w)
}

// contractCountsLine renders the per-status totals in the fixed status order,
// including zeros. The counts cover every assembled contract regardless of
// --status, so the orphan totals stay readable against matched/internal — a
// denominator that moved with the filter would make them meaningless.
func contractCountsLine(counts map[string]int) string {
	var parts []string
	for _, status := range contractStatusOrder {
		parts = append(parts, fmt.Sprintf("%s %d", status, counts[status]))
	}
	for _, status := range extraKeys(counts, contractStatusOrder) {
		parts = append(parts, fmt.Sprintf("%s %d", status, counts[status]))
	}
	return strings.Join(parts, " · ")
}

// ── grouping ────────────────────────────────────────────────────────────────

// contractStatusOrder is orphans-first: the orphan report is the default answer
// and the finding a caller acts on. matched/internal follow as the denominator
// it reads against; quarantined last, since it is a "do not conclude" bucket.
var contractStatusOrder = []string{
	"orphan_exposer", "orphan_user", "matched", "internal", "quarantined",
}

var contractStatusHint = map[string]string{
	"orphan_exposer": "exposed, nothing indexed uses it",
	"orphan_user":    "used, nothing indexed exposes it",
	"matched":        "exposed in one repo, used from another",
	"internal":       "both sides present, all in one repo — not a cross-repo contract",
	"quarantined":    "every party is test/fixture/vendored/generated code",
}

var contractKindOrder = []string{"http", "kafka"}

type contractGroup struct {
	status string
	kind   string
	items  []contractEntry
}

// groupContracts buckets contracts by status, then kind, preserving the
// server's ordering within a bucket. Unknown statuses and kinds (a newer
// server) sort after the known ones rather than being dropped.
func groupContracts(contracts []contractEntry) []contractGroup {
	index := map[[2]string]int{}
	var groups []contractGroup
	for _, c := range contracts {
		key := [2]string{c.Status, c.Kind}
		if at, ok := index[key]; ok {
			groups[at].items = append(groups[at].items, c)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, contractGroup{status: c.Status, kind: c.Kind, items: []contractEntry{c}})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		si, sj := rankOf(contractStatusOrder, groups[i].status), rankOf(contractStatusOrder, groups[j].status)
		if si != sj {
			return si < sj
		}
		if groups[i].status != groups[j].status {
			return groups[i].status < groups[j].status // both unranked
		}
		ki, kj := rankOf(contractKindOrder, groups[i].kind), rankOf(contractKindOrder, groups[j].kind)
		if ki != kj {
			return ki < kj
		}
		return groups[i].kind < groups[j].kind
	})
	return groups
}

// rankOf returns the position of v in order, or len(order) when it is absent —
// so anything unknown sorts last as one block, then alphabetically.
func rankOf(order []string, v string) int {
	for i, o := range order {
		if o == v {
			return i
		}
	}
	return len(order)
}

// extraKeys returns the keys of m that `known` does not list, sorted — so a
// status the CLI predates is still reported rather than silently dropped.
func extraKeys(m map[string]int, known []string) []string {
	var out []string
	for k := range m {
		if rankOf(known, k) == len(known) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ── human ───────────────────────────────────────────────────────────────────

func renderContractsHuman(
	w io.Writer,
	env contractsEnvelope,
	groups []contractGroup,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
) {
	seen := map[string]bool{}
	lastStatus := ""
	for _, g := range groups {
		if g.status != lastStatus {
			if lastStatus != "" {
				fmt.Fprintln(w)
			}
			if hint := contractStatusHint[g.status]; hint != "" {
				fmt.Fprintf(w, "%s — %s\n", g.status, hint)
			} else {
				fmt.Fprintf(w, "%s\n", g.status)
			}
			lastStatus = g.status
		}
		fmt.Fprintf(w, "  %s\n", g.kind)
		for _, c := range g.items {
			fmt.Fprintf(w, "   • %s\n", c.ContractID)
			renderContractParties(w, "exposers", c.Exposers, env, unmask, seen)
			renderContractParties(w, "users", c.Users, env, unmask, seen)
		}
	}
	renderContractRepoLegend(w, env, seen)
}

func renderContractParties(
	w io.Writer,
	label string,
	parties []contractParty,
	env contractsEnvelope,
	unmask func(pathToken string, maskKeyRev int) (string, bool),
	seen map[string]bool,
) {
	if len(parties) == 0 {
		// The empty side is the finding for an orphan — say it, don't omit it.
		fmt.Fprintf(w, "       %s: none\n", label)
		return
	}
	fmt.Fprintf(w, "       %s (%d):\n", label, len(parties))
	for _, p := range parties {
		seen[p.RepoID] = true
		path := p.PathToken
		if unmask != nil {
			if real, ok := unmask(p.PathToken, env.Repos[p.RepoID].MaskKeyRev); ok {
				path = real
			}
		}
		tag := ""
		if p.Quarantined {
			// Shown, never dropped: a quarantined party that does not corroborate
			// is why a contract reads as an orphan, so hiding it hides the reason.
			tag = "  [quarantined]"
		}
		fmt.Fprintf(w, "         %s:%d  %s  (%s, %.2f)%s\n",
			path, p.Line, shortRepoID(p.RepoID), p.Role, p.Confidence, tag)
	}
}

// renderContractRepoLegend expands the short repo ids used in the party lines.
// The report carries no repo names — only masking metadata — so the full UUID
// has to appear somewhere: it is what `--repo` takes, and the masking scheme
// says whether a still-masked path is masked or simply not in your checkout.
func renderContractRepoLegend(w io.Writer, env contractsEnvelope, seen map[string]bool) {
	if len(seen) == 0 {
		return
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintln(w, "\nrepos:")
	for _, id := range ids {
		meta, ok := env.Repos[id]
		if !ok {
			fmt.Fprintf(w, "  %s  %s\n", shortRepoID(id), id)
			continue
		}
		fmt.Fprintf(w, "  %s  %s  (masking %s, key rev %d)\n",
			shortRepoID(id), id, meta.MaskingScheme, meta.MaskKeyRev)
	}
}

// shortRepoID abbreviates a repo UUID for the per-party lines, which repeat it
// once per location. The full id stays available in the legend and --json.
func shortRepoID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ── agent ───────────────────────────────────────────────────────────────────

// renderContractsAgent prints one line per contract: status, canonical id, and
// how many parties sit on each side with the repos they are in. A caller that
// wants a party's file opens the human or json form — the line is here to be
// scanned and counted, and party detail would bury the status column.
func renderContractsAgent(w io.Writer, groups []contractGroup) {
	for _, g := range groups {
		for _, c := range g.items {
			fmt.Fprintf(w, "%s %s — exposers: %d%s · users: %d%s\n",
				c.Status, c.ContractID,
				len(c.Exposers), contractRepoSet(c.Exposers),
				len(c.Users), contractRepoSet(c.Users))
		}
	}
}

// contractRepoSet lists the distinct repos on one side, in first-seen order.
func contractRepoSet(parties []contractParty) string {
	if len(parties) == 0 {
		return ""
	}
	var ids []string
	seen := map[string]bool{}
	for _, p := range parties {
		if seen[p.RepoID] {
			continue
		}
		seen[p.RepoID] = true
		ids = append(ids, shortRepoID(p.RepoID))
	}
	return " (" + strings.Join(ids, ", ") + ")"
}
