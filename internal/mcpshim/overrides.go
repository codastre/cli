package mcpshim

import "encoding/json"

// Client-only QUERY arguments. Hydration happens in this proxy, not on the
// server, so these have no server-side counterpart: they are read here and
// REMOVED from the outgoing request. Forwarding them would fail validation
// against the QUERY tool schema.
//
// Why per-call and not just `serve` flags: the flags are set once, by whoever
// wrote the MCP config, and an agent cannot reach them. But the right budget is
// a property of the question — an orientation query ("which repo owns billing?")
// wants paths only, while "show me the recall action" wants the body. Without a
// per-call lever the caller's only cost dial is top_k, which trades away recall
// to save bytes. See docs/bugs/query-defaults-token-budget.md.
const (
	// argMaxSnippetLines (int) caps this call's snippets. 0 → no snippet bodies.
	argMaxSnippetLines = "max_snippet_lines"
	// argSnippets (bool) turns hydration off for this call when false.
	argSnippets = "snippets"
	// argFormat is shared with the server, which is why it is handled differently
	// from the two above: it is REWRITTEN rather than stripped. The three values
	// form one ladder of increasing compaction, on both QUERY and GRAPH —
	//
	//	verbose  server's full JSON item (default)
	//	compact  server drops what a caller can't act on
	//	         (query_shape.py / graph_shape.py)
	//	agent    compact, and this proxy renders it as text
	//	         (render.go / render_graph.go)
	//
	// One knob rather than a client `format` beside a server `format`: from the
	// caller's side "how compact do you want the answer" is a single question,
	// and two similarly-named arguments with different value sets would be a
	// standing invitation to pass the wrong one. `agent` is the only value the
	// server cannot honour, so it is the only one that gets rewritten.
	argFormat = "format"
)

// WireFormatCompact is the server-side rung of the compaction ladder: the most
// compact shape the server itself can produce. Exported because `codastre query`
// talks to the server directly rather than through this proxy, and asks for the
// same rung when it is going to render the result as agent text.
const WireFormatCompact = "compact"

// The WIRE values of argFormat — the rungs a request can carry. Anything outside
// this set is forwarded untouched so the server answers it: an unknown format is
// a caller mistake, and INVALID_REQUEST from the tool that owns the argument
// beats a silent fallback here.
const (
	formatVerbose = "verbose"
	formatCompact = WireFormatCompact
	// formatAgent is the one wire value the server cannot honour: it travels as
	// compact and this proxy renders the result as text. See takeCallOverrides.
	formatAgent = "agent"
)

// formatJSON is a CONFIG-level spelling of "do not render", deliberately kept
// out of the block above: it is not a wire value, because on the wire both
// verbose and compact already mean JSON.
//
// It is what `serve --format json` and $CODASTRE_QUERY_FORMAT set, so a caller
// who read either — or the const block this used to share with the wire values —
// will reach for it over MCP too. takeCallOverrides therefore accepts it as a
// synonym for verbose rather than forwarding it to a server whose enum has no
// such value. Without that, "json" was the one spelling of "I want JSON" that
// did not get JSON: apply's switch had no case for it, so an explicit request
// silently inherited `serve --format agent` and came back as text.
const formatJSON = "json"

// callOverrides is one call's client-side settings. Absent fields leave the
// Config's value alone, so a call that passes none behaves exactly as before.
//
// Named for the call rather than for hydration: the format is an encoding
// choice, not a hydration one, and the two are independent (an agent-format
// response can carry bodies; a JSON response can omit them).
type callOverrides struct {
	maxSnippetLines *int
	snippets        *bool
	format          *string
}

func (o callOverrides) empty() bool {
	return o.maxSnippetLines == nil && o.snippets == nil && o.format == nil
}

// apply returns a copy of cfg with this call's overrides folded in.
func (o callOverrides) apply(cfg Config) Config {
	// An explicit per-call format beats the process default in BOTH directions:
	// `serve --format=agent` sets the house style, and a caller that asks for
	// verbose/compact JSON gets JSON.
	switch {
	case o.format == nil:
	case *o.format == formatAgent:
		cfg.Format = formatAgent
	case *o.format == formatVerbose || *o.format == formatCompact || *o.format == formatJSON:
		cfg.Format = formatJSON
	}
	if o.snippets != nil && !*o.snippets {
		cfg.NoSnippets = true
	}
	if o.maxSnippetLines != nil {
		n := *o.maxSnippetLines
		if n <= 0 {
			// "at most zero lines" is a request for no bodies, not for the
			// default budget — which is what a 0 would otherwise mean to
			// snippetLineBudget.
			cfg.NoSnippets = true
		} else {
			cfg.MaxSnippetLines = n
		}
	}
	return cfg
}

// takeCallOverrides extracts this call's overrides from a JSON-RPC tools/call
// body and returns the body to forward: the client-only arguments removed, and
// format=agent rewritten to compact JSON when this proxy is the one that will
// render it.
//
// Returns the ORIGINAL body untouched whenever nothing was found or anything
// fails to parse. That matters: this runs on every message on the wire,
// including ones this proxy knows nothing about, and a re-marshal that drops an
// unrecognised field would break them. Only a body that actually carried an
// override is rewritten.
func takeCallOverrides(cfg Config, body []byte) ([]byte, callOverrides) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return body, callOverrides{}
	}
	if method, _ := unmarshalString(msg["method"]); method != "tools/call" {
		return body, callOverrides{}
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(msg["params"], &params); err != nil {
		return body, callOverrides{}
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(params["arguments"], &args); err != nil {
		return body, callOverrides{}
	}

	var out callOverrides
	stripped := false
	if raw, ok := args[argMaxSnippetLines]; ok {
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			out.maxSnippetLines = &n
		}
		// Deleted even when it failed to decode: it is not a server argument
		// either way, and forwarding it would turn a bad value into a tool
		// validation error instead of a silently ignored hint.
		delete(args, argMaxSnippetLines)
		stripped = true
	}
	if raw, ok := args[argSnippets]; ok {
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			out.snippets = &b
		}
		delete(args, argSnippets)
		stripped = true
	}
	// QUERY and GRAPH only: they are the tools that declare `format`, and
	// rewriting a same-named argument on any other tool would corrupt that call.
	if toolName, _ := unmarshalString(params["name"]); toolName == "QUERY" || toolName == "GRAPH" {
		if raw, ok := args[argFormat]; ok {
			if value, ok := unmarshalString(raw); ok {
				out.format = &value
				// Unlike the other two, format is a real server argument, so it is
				// forwarded rather than stripped — verbose, compact and any
				// unrecognised value all travel untouched. Only values this proxy
				// answers itself are rewritten:
				switch value {
				case formatAgent:
					// To `compact`, not dropped: agent rendering reads none of the
					// fields compact omits, so a verbose copy would be paid for on
					// the server → proxy hop and then discarded.
					//
					// Only when this proxy is the one that will render, though. The
					// server serves this rung too (server/api/agent_render.py), and it
					// is what answers when enrichment is impossible. Without the
					// guard, a caller with no unmasker and no scheme knowledge asked
					// for `agent`, the request was downgraded to compact here,
					// enrichResponse then declined to render — and the answer came
					// back as compact JSON with no rendering and nothing saying why.
					// The proxy takes the rung over only where it can do better: real
					// paths, and bodies.
					if !cfg.canEnrich() {
						break
					}
					args[argFormat], _ = json.Marshal(formatCompact)
					stripped = true
				case formatJSON:
					// To `verbose`: "json" is the Config spelling (see formatJSON),
					// and the server's enum is verbose|compact. Forwarding it as-is
					// is inert against a server that ignores the argument and an
					// INVALID_REQUEST against one that does not — for a value the
					// CLI flag and the env var both accept.
					args[argFormat], _ = json.Marshal(formatVerbose)
					stripped = true
				}
			}
		}
	}
	if !stripped {
		return body, out
	}

	rewritten, ok := rewriteArguments(msg, params, args)
	if !ok {
		return body, callOverrides{}
	}
	return rewritten, out
}

// rewriteArguments re-marshals a tools/call body with a new arguments map.
func rewriteArguments(
	msg map[string]json.RawMessage,
	params map[string]json.RawMessage,
	args map[string]json.RawMessage,
) ([]byte, bool) {
	argsB, err := json.Marshal(args)
	if err != nil {
		return nil, false
	}
	params["arguments"] = argsB
	paramsB, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	msg["params"] = paramsB
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}
	return b, true
}
