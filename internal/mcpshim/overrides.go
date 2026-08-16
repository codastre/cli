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
)

// hydrationOverrides is one call's client-side hydration settings. Absent fields
// leave the Config's value alone, so a call that passes neither argument behaves
// exactly as before.
type hydrationOverrides struct {
	maxSnippetLines *int
	snippets        *bool
}

func (o hydrationOverrides) empty() bool {
	return o.maxSnippetLines == nil && o.snippets == nil
}

// apply returns a copy of cfg with this call's overrides folded in.
func (o hydrationOverrides) apply(cfg Config) Config {
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

// takeHydrationOverrides extracts the client-only arguments from a JSON-RPC
// tools/call body and returns the body with them removed.
//
// Returns the ORIGINAL body untouched whenever nothing was found or anything
// fails to parse. That matters: this runs on every message on the wire,
// including ones this proxy knows nothing about, and a re-marshal that drops an
// unrecognised field would break them. Only a body that actually carried an
// override is rewritten.
func takeHydrationOverrides(body []byte) ([]byte, hydrationOverrides) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return body, hydrationOverrides{}
	}
	if method, _ := unmarshalString(msg["method"]); method != "tools/call" {
		return body, hydrationOverrides{}
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(msg["params"], &params); err != nil {
		return body, hydrationOverrides{}
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(params["arguments"], &args); err != nil {
		return body, hydrationOverrides{}
	}

	var out hydrationOverrides
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
	if !stripped {
		return body, out
	}

	rewritten, ok := rewriteArguments(msg, params, args)
	if !ok {
		return body, hydrationOverrides{}
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
