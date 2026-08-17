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

// The values argFormat accepts. Anything else is forwarded untouched so the
// server answers it — an unknown format is a caller mistake, and INVALID_REQUEST
// from the tool that owns the argument beats a silent fallback here.
const (
	formatVerbose = "verbose"
	formatCompact = "compact"
	formatAgent   = "agent"
	// formatJSON is the Config-level spelling for "do not render". It is not a
	// wire value: on the wire, both verbose and compact mean JSON.
	formatJSON = "json"
)

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
	case *o.format == formatVerbose || *o.format == formatCompact:
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
// format=agent rewritten to the compact JSON the server can actually produce.
//
// Returns the ORIGINAL body untouched whenever nothing was found or anything
// fails to parse. That matters: this runs on every message on the wire,
// including ones this proxy knows nothing about, and a re-marshal that drops an
// unrecognised field would break them. Only a body that actually carried an
// override is rewritten.
func takeCallOverrides(body []byte) ([]byte, callOverrides) {
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
				// Unlike the other two, format is a real server argument. Only the
				// one value the server cannot produce is rewritten — and to
				// `compact` rather than dropped, because agent rendering reads none
				// of the fields compact omits, so a verbose copy would be paid for
				// on the server → proxy hop and then discarded.
				if value == formatAgent {
					args[argFormat], _ = json.Marshal(formatCompact)
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
