package mcpshim

import (
	"encoding/json"
	"slices"
)

// Advertising the client-only hydration arguments.
//
// `snippets` and `max_snippet_lines` are implemented HERE, in the proxy (see
// overrides.go) — the server never sees them and could not honour them, since it
// never holds the source. That left them undiscoverable: an agent reads the QUERY
// tool schema the server publishes, and a parameter absent from that schema does
// not exist as far as the agent is concerned. Worse, strict MCP clients validate
// arguments against the advertised schema before sending, so passing one could be
// rejected client-side and never reach this proxy at all.
//
// So the proxy adds them to the QUERY tool's inputSchema on the way back out. The
// schema then describes what this endpoint actually accepts — proxy plus server —
// rather than the server half alone. A client talking to /mcp directly still sees
// the unannotated schema, which is correct: there, the arguments genuinely do not
// exist.
//
// The same applies to the `agent` rung of `format` on QUERY and GRAPH. That one
// is not a client-only argument but a client-only *value*: the server owns
// `format` and produces verbose/compact, and only the rendering happens here — so
// the proxy extends the published enum instead of declaring a second argument.
const (
	snippetsArgDescription = "Set false to skip snippet bodies and return ranked " +
		"locations only (path + line span + score). Use it for orientation calls " +
		"(\"which repo/file owns X?\") and when refining a query whose hits you " +
		"have already seen hydrated — re-hydrating them costs the bytes again for " +
		"content you hold. Each result then carries hydration=\"snippets_disabled\", " +
		"so the absence is a choice, not a failure to repair. Handled locally by " +
		"the codastre CLI proxy."

	maxSnippetLinesArgDescription = "Cap each snippet at this many lines for this " +
		"call. Lower it to widen top_k without paying for full bodies; 0 is " +
		"equivalent to snippets=false. Handled locally by the codastre CLI proxy."

	// formatAgentNote extends the server's own `format` description rather than
	// replacing it: verbose and compact are the server's values and it documents
	// them, while `agent` is a third rung this proxy adds on top of compact.
	formatAgentNote = " Through the codastre CLI proxy a third value is available: " +
		"'agent' renders the response as text — hits grouped by file, one path " +
		"per file instead of once per hit, bodies as source with real line " +
		"numbers instead of JSON-escaped strings. Pair it with snippets=false " +
		"for the cheapest locate call."

	// queryFormatStandalone is QUERY's `format` description when this proxy has to
	// declare the argument itself — see declareFormatArg. It restates only what
	// the proxy can honour unaided, since there is no server text to extend.
	queryFormatStandalone = "How compact a response to return. 'verbose' is the " +
		"server's full JSON envelope (the default). 'agent' renders it as text — " +
		"hits grouped by file, one path per file instead of once per hit, bodies " +
		"as source with real line numbers instead of JSON-escaped strings. Pair " +
		"'agent' with snippets=false for the cheapest locate call. Rendered by " +
		"the codastre CLI proxy, so it does not depend on the server's own " +
		"support for this argument."

	// graphFormatStandalone is the same, in GRAPH's terms.
	graphFormatStandalone = "How compact a traversal to return. 'verbose' is the " +
		"server's full JSON envelope (the default). 'agent' renders it as text — " +
		"edges grouped under their source file, so a fan-out writes that file's " +
		"path once instead of once per edge, and paths are unmasked to real ones. " +
		"Rendered by the codastre CLI proxy, so it does not depend on the " +
		"server's own support for this argument."

	// graphFormatAgentNote is the same rung on GRAPH, described in GRAPH's terms:
	// there are no bodies here, so the saving is the repetition a traversal
	// creates — every edge out of one function repeats that function's path.
	graphFormatAgentNote = " Through the codastre CLI proxy a third value is " +
		"available: 'agent' renders the traversal as text — edges grouped under " +
		"their source file, so a fan-out writes that file's path once instead of " +
		"once per edge, and paths are unmasked to real ones."
)

// annotateToolList injects the client-only hydration arguments into the QUERY
// tool's inputSchema in a tools/list response, and the extra `agent` format on
// both QUERY and GRAPH. Returns data unchanged when the message is not a
// tools/list result, or when there is nothing to tune.
func annotateToolList(cfg Config, data []byte) []byte {
	// Nothing to advertise when this proxy cannot resolve paths at all: it then
	// enriches nothing and renders nothing, so every argument added here would
	// be a control that does nothing.
	if !cfg.canEnrich() {
		return data
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(env["result"], &result); err != nil {
		return data
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(result["tools"], &tools); err != nil {
		return data
	}

	changed := false
	for i, tool := range tools {
		name, _ := unmarshalString(tool["name"])
		var schema json.RawMessage
		var ok bool
		switch name {
		case "QUERY":
			// Hydration is QUERY's alone, and only offered when this proxy will
			// actually hydrate: the overrides can turn hydration off, never on,
			// so the operator's `--no-snippets` stays the floor. The format rung
			// is independent of it — a rendering costs no hydration.
			schema, ok = withQueryArgs(tool["inputSchema"], !cfg.NoSnippets)
		case "GRAPH":
			schema, ok = withFormatArg(tool["inputSchema"], graphFormatAgentNote, graphFormatStandalone)
		default:
			continue
		}
		if !ok {
			continue
		}
		tool["inputSchema"] = schema
		tools[i] = tool
		changed = true
	}
	if !changed {
		return data
	}

	toolsB, err := json.Marshal(tools)
	if err != nil {
		return data
	}
	result["tools"] = toolsB
	resultB, err := json.Marshal(result)
	if err != nil {
		return data
	}
	env["result"] = resultB
	out, err := json.Marshal(env)
	if err != nil {
		return data
	}
	return out
}

// extendFormatEnum adds "agent" to the server's `format` property — its enum and
// its description, extended with note — and reports whether it changed anything.
// When the server publishes no `format` at all, it falls back to declaring one
// (declareFormatArg) rather than leaving the rung unreachable.
//
// Extension is preferred where it is possible. The server owns this argument's
// meaning and documents verbose/compact itself; restating them here would be a
// second copy to keep in step.
func extendFormatEnum(props map[string]json.RawMessage, note, standalone string) bool {
	raw, ok := props[argFormat]
	if !ok {
		return declareFormatArg(props, standalone)
	}
	var prop map[string]json.RawMessage
	if err := json.Unmarshal(raw, &prop); err != nil {
		return false
	}
	var values []string
	if err := json.Unmarshal(prop["enum"], &values); err != nil {
		return false
	}
	if slices.Contains(values, formatAgent) {
		return false // already advertised (a future server, or a re-annotation)
	}
	prop["enum"], _ = json.Marshal(append(values, formatAgent))

	if desc, ok := unmarshalString(prop["description"]); ok {
		prop["description"], _ = json.Marshal(desc + note)
	}
	updated, err := json.Marshal(prop)
	if err != nil {
		return false
	}
	props[argFormat] = updated
	return true
}

// proxyFormatValues is the enum advertised when this proxy declares `format`
// itself: the two rungs it can honour without server support.
//
// `compact` is omitted deliberately. It is the server's rung, and advertising it
// against a server that cannot produce it would promise a saving that never
// arrives — the failure mode this fallback exists to avoid, not to reproduce.
var proxyFormatValues = []string{formatVerbose, formatAgent}

// declareFormatArg publishes a proxy-owned `format` for a server that has not
// shipped one.
//
// This used to return false, on the grounds that declaring an argument the
// server does not know would be advertising a phantom. That reasoning holds for
// the argument's *server* half and not for its client half: the `agent`
// rendering is entirely local (render.go / render_graph.go), so the whole
// measured saving of the rung is available the moment this binary runs,
// regardless of the server's build. Withholding the advertisement withheld the
// saving too — most MCP clients refuse to send an argument the schema does not
// declare, which made `agent` unreachable over MCP while the CLI's own
// `--format agent` worked against the same deployment.
//
// Forwarding is safe: `agent` travels as `compact` (takeCallOverrides), and a
// server predating the argument ignores an unknown one rather than rejecting it
// — verified against the deployed build, which answers a tools/call carrying
// `format: "compact"` with a normal verbose envelope. A server that later ships
// `format` publishes its own property, and the extension path above takes over.
func declareFormatArg(props map[string]json.RawMessage, description string) bool {
	def, err := json.Marshal(map[string]any{
		"type":        "string",
		"enum":        proxyFormatValues,
		"default":     formatVerbose,
		"description": description,
	})
	if err != nil {
		return false
	}
	props[argFormat] = def
	return true
}

// withFormatArg returns schemaRaw with `agent` added to the server's format
// enum — or a proxy-declared `format` when the server publishes none — for a
// tool that has no client-only arguments of its own (GRAPH).
func withFormatArg(schemaRaw json.RawMessage, note, standalone string) (json.RawMessage, bool) {
	return withArgs(schemaRaw, func(props map[string]json.RawMessage) bool {
		return extendFormatEnum(props, note, standalone)
	})
}

// withQueryArgs returns schemaRaw with `agent` on the format enum and, when
// hydration is offered, the two client-only hydration properties. Existing
// properties of the same name are left alone: if a future server starts
// declaring them, its definition wins over this local one.
func withQueryArgs(schemaRaw json.RawMessage, hydration bool) (json.RawMessage, bool) {
	return withArgs(schemaRaw, func(props map[string]json.RawMessage) bool {
		// `format` is the server's argument; the proxy only adds its extra value
		// to the enum the server published, so a strict client will send it.
		added := extendFormatEnum(props, formatAgentNote, queryFormatStandalone)
		if !hydration {
			return added
		}
		for name, def := range map[string]any{
			argSnippets: map[string]any{
				"type":        "boolean",
				"default":     true,
				"description": snippetsArgDescription,
			},
			argMaxSnippetLines: map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": maxSnippetLinesArgDescription,
			},
		} {
			if _, exists := props[name]; exists {
				continue
			}
			b, err := json.Marshal(def)
			if err != nil {
				continue
			}
			props[name] = b
			added = true
		}
		return added
	})
}

// withArgs applies mutate to a tool inputSchema's properties map and re-marshals
// it, reporting false when there was nothing to parse or nothing to add.
func withArgs(schemaRaw json.RawMessage, mutate func(map[string]json.RawMessage) bool) (json.RawMessage, bool) {
	if len(schemaRaw) == 0 {
		return nil, false
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, false
	}
	props := map[string]json.RawMessage{}
	if raw, ok := schema["properties"]; ok {
		if err := json.Unmarshal(raw, &props); err != nil {
			return nil, false
		}
	}
	if !mutate(props) {
		return nil, false
	}

	propsB, err := json.Marshal(props)
	if err != nil {
		return nil, false
	}
	schema["properties"] = propsB
	out, err := json.Marshal(schema)
	if err != nil {
		return nil, false
	}
	return out, true
}
