package mcpshim

import "encoding/json"

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
)

// annotateToolList injects the client-only hydration arguments into the QUERY
// tool's inputSchema in a tools/list response. Returns data unchanged when the
// message is not a tools/list result, or when there is nothing to tune.
func annotateToolList(cfg Config, data []byte) []byte {
	// Nothing to advertise when this proxy will not hydrate anyway: with
	// hydration off process-wide, or with no way to resolve paths at all, a
	// per-call dial would be a control that does nothing. Note the asymmetry is
	// deliberate — the overrides can turn hydration off, never on, so the
	// operator's `--no-snippets` stays the floor.
	if cfg.NoSnippets || !cfg.canEnrich() {
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
		if name, _ := unmarshalString(tool["name"]); name != "QUERY" {
			continue
		}
		schema, ok := withHydrationArgs(tool["inputSchema"])
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

// withHydrationArgs returns schemaRaw with the two hydration properties added.
// Existing properties of the same name are left alone: if a future server starts
// declaring them, its definition wins over this local one.
func withHydrationArgs(schemaRaw json.RawMessage) (json.RawMessage, bool) {
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

	added := false
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
			return nil, false
		}
		props[name] = b
		added = true
	}
	if !added {
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
