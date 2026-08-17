package mcpshim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// enrichQueryResponse unmasks path_tokens and hydrates snippets in QUERY
// responses, reporting the cost of the JSON it produced.
func enrichQueryResponse(cfg Config, data []byte) []byte {
	out, results, acct := enrichQueryPayload(cfg, data)
	acct.report(cfg, results, len(out))
	return out
}

// HydrateQueryPayload enriches a raw server QUERY envelope: real_path on every
// result it can unmask, a snippet body read from the local checkout, and a
// `hydration` reason wherever a body could not be produced.
//
// Exported for `codastre query`, which talks to the server directly and so has
// no proxy to enrich for it. It is the same code path `serve` runs, deliberately:
// two implementations would be two chances to disagree about which line a body
// starts at. Returns the payload unchanged when cfg can resolve no paths at all.
//
// No cost line: cfg.Log bills the caller for the bytes it was sent, and what a
// person sees in a terminal is the rendering, not this JSON.
func HydrateQueryPayload(cfg Config, payload []byte) []byte {
	cfg.Log = nil
	out, _, _ := enrichQueryPayload(cfg, payload)
	return out
}

// enrichQueryPayload is enrichQueryResponse without the cost report, for callers
// that go on to re-encode the payload. Agent format renders the JSON to text
// afterwards, so reporting here would bill the caller for a payload that was
// never sent — the one number the cost line exists to keep honest.
func enrichQueryPayload(cfg Config, data []byte) ([]byte, int, payloadAccount) {
	var acct payloadAccount
	if !cfg.canEnrich() {
		return data, 0, acct
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data, 0, acct
	}
	resultsRaw, ok := env["results"]
	if !ok {
		return data, 0, acct
	}

	var results []map[string]json.RawMessage
	if err := json.Unmarshal(resultsRaw, &results); err != nil {
		return data, 0, acct
	}

	// The index-free (federated) QUERY path returns mask_key_rev: null and puts
	// the authoritative version in the per-repo map mask_key_revs. Resolve each
	// result at its own repo's rev so a key rotation is honoured; fall back to
	// the singular field (Mode A, single-index queries).
	var maskKeyRev int
	if raw, ok := env["mask_key_rev"]; ok {
		_ = json.Unmarshal(raw, &maskKeyRev)
	}
	maskKeyRevs := map[string]int{}
	if raw, ok := env["mask_key_revs"]; ok {
		_ = json.Unmarshal(raw, &maskKeyRevs)
	}

	for i, r := range results {
		results[i] = enrichQueryResult(cfg, r, maskKeyRev, maskKeyRevs, &acct)
	}

	enriched, _ := json.Marshal(results)
	env["results"] = enriched
	out, _ := json.Marshal(env)
	return out, len(results), acct
}

// enrichQueryResult adds real_path and, when possible, a snippet to one QUERY
// result. Every path that cannot produce a snippet writes a `hydration` reason
// instead: a missing snippet is otherwise indistinguishable from "this repo
// isn't checked out", "the file moved" and "the read failed" — three cases with
// three different fixes.
func enrichQueryResult(
	cfg Config,
	r map[string]json.RawMessage,
	maskKeyRev int,
	maskKeyRevs map[string]int,
	acct *payloadAccount,
) map[string]json.RawMessage {
	pathToken, _ := unmarshalString(r["path_token"])
	repoID, _ := unmarshalString(r["repo_id"])
	rev := maskKeyRev
	if v, ok := maskKeyRevs[repoID]; ok {
		rev = v
	}

	// hmac → inverse-HMAC via UnmaskPath; none → identity (token is cleartext).
	realPath, ok := cfg.unmaskOrIdentity(pathToken, repoID, rev)
	if !ok {
		// No real path, so no snippet either — but the result must still say
		// why, or it lands in exactly the ambiguous state the `hydration`
		// field exists to remove (see the invariant above).
		r["hydration"], _ = json.Marshal(hydrationUnmaskFailed)
		return r
	}
	r["real_path"], _ = json.Marshal(realPath)

	// --no-snippets: the caller asked for ranked locations only. Cheap
	// orientation queries don't need bodies, and saying so explicitly stops the
	// agent from reading the absence as a hydration failure it should fix.
	if cfg.NoSnippets {
		r["hydration"], _ = json.Marshal(hydrationSnippetsDisabled)
		return r
	}

	// Snippet hydration (impl-spec §2.7). Independent of masking — it needs only
	// a local checkout root, resolved per-repo so federated hits hydrate too.
	root := cfg.rootFor(repoID)
	if root == "" {
		r["hydration"], _ = json.Marshal(hydrationNoCheckout)
		return r
	}

	var lineStart, lineEnd int
	if raw, ok := r["line_start"]; ok {
		_ = json.Unmarshal(raw, &lineStart)
	}
	if raw, ok := r["line_end"]; ok {
		_ = json.Unmarshal(raw, &lineEnd)
	}
	blobSHA, _ := unmarshalString(r["blob_sha"])
	pathClass, _ := unmarshalString(r["path_class"])

	budget := cfg.snippetLineBudget(realPath, pathClass)
	absPath := filepath.Join(root, realPath)
	res, err := hydrateSnippet(absPath, lineStart, lineEnd, budget, blobSHA)
	switch {
	case err == nil && res.Lines == 0:
		// File read fine, but the range wasn't in it. Emitting `"snippet": ""`
		// here would look like a successful hydration of an empty region, so
		// report the reason instead.
		r["hydration"], _ = json.Marshal(hydrationRangeMissing)
	case err == nil:
		r["snippet"], _ = json.Marshal(res.Text)
		if res.Stale {
			r["stale"], _ = json.Marshal(true)
		}
		if res.Truncated {
			r["snippet_truncated"], _ = json.Marshal(true)
			r["snippet_line_end"], _ = json.Marshal(res.LastLine)
		}
		acct.add(res)
	case os.IsNotExist(err):
		// Checkout known, file absent: indexed at a ref this checkout doesn't
		// have, or deleted/moved since. Pulling usually fixes it.
		r["hydration"], _ = json.Marshal(hydrationFileMissing)
	default:
		r["hydration"], _ = json.Marshal(hydrationReadError)
	}
	return r
}

// payloadAccount tallies what a QUERY response actually cost to hydrate.
type payloadAccount struct {
	hydrated  int
	lines     int
	truncated int
}

func (a *payloadAccount) add(res snippetResult) {
	a.hydrated++
	a.lines += res.Lines
	if res.Truncated {
		a.truncated++
	}
}

// Bytes per token, by payload shape. The prose default of 4 is wrong for
// everything this tool emits, and wrong unevenly, which matters because the cost
// line exists to compare tiers against each other.
//
// Measured with cl100k_base over eight deployed responses — two repos plus a
// federated run, top_k 5 to 20, bodies on and off
// (docs/plans/agent-response-format-implementation.md §8.4):
//
//	JSON envelopes   2.38 – 2.69   (pooled 2.53)
//	agent rendering  2.48 – 3.48   (pooled 3.33)
//
// A JSON envelope is dense in the tokeniser's worst input — 64-hex ids, quoted
// keys, escaped newlines — while the rendering keeps mostly paths and
// identifiers, which pack well. Hence two constants rather than one: a single
// divisor would understate JSON and overstate the rendering, biasing exactly the
// comparison the line is for.
//
// These are estimates and the "~" in the output is meant: within a shape the
// spread is corpus-dependent (repeated long path prefixes merge well and push
// the ratio up), so treat any single figure as ±20%. Not Claude's tokeniser
// either — same family, so the ordering holds and the magnitudes may shift.
const (
	bytesPerTokenJSON  = 2.5
	bytesPerTokenAgent = 3.0
)

func bytesPerToken(cfg Config) float64 {
	if cfg.Format == formatAgent {
		return bytesPerTokenAgent
	}
	return bytesPerTokenJSON
}

// report writes a one-line cost summary to cfg.Log (C6). Payload cost is
// invisible at the point of use, which is why measuring it took a dedicated
// experiment; a line per response makes an expensive query legible immediately.
// Bytes are the enriched envelope's real size; the token figure is derived from
// it via the measured ratio above and is labelled approximate.
func (a *payloadAccount) report(cfg Config, results, bytes int) {
	if cfg.Log == nil {
		return
	}
	msg := fmt.Sprintf(
		"query: %d results · %d hydrated · %d lines · %d bytes (~%.1fk tokens)",
		results, a.hydrated, a.lines, bytes, float64(bytes)/(bytesPerToken(cfg)*1000.0),
	)
	if a.truncated > 0 {
		msg += fmt.Sprintf(" · %d truncated", a.truncated)
	}
	// Name the tier that produced the number. Without it the cost line cannot
	// answer the question the plan says to instrument first: whether callers
	// ever actually reach for the cheap tiers (agent-response-format.md,
	// "What would falsify this").
	msg += " · " + a.mode(cfg)
	fmt.Fprintln(cfg.Log, msg)
}

// mode names the encoding and hydration tier this response was produced under,
// e.g. "agent/snippets:off".
func (a *payloadAccount) mode(cfg Config) string {
	format := cfg.Format
	if format == "" {
		format = formatJSON
	}
	snippets := "snippets:on"
	if cfg.NoSnippets {
		snippets = "snippets:off"
	}
	return format + "/" + snippets
}
