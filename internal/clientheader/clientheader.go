// Package clientheader resolves the X-Codastre-Client value this CLI process
// sends on outbound requests (client-attribution-plan.md §8).
//
// A neutral leaf package — mirrors server/request_context.py's role on the
// server side. cmd (the composition root) resolves the --client/$CODASTRE_CLIENT
// override once at startup and calls SetOverride; internal/mcpclient,
// internal/mcpshim, and internal/unmask read it back via Value/Override without
// importing cmd, keeping the dependency rule intact (imports flow inward only —
// CLAUDE.md's hexagonal-architecture section).
//
// TRUST MODEL: like the header itself, this is self-asserted attribution
// telemetry only. It must never gate a request or influence its outcome.
package clientheader

import "github.com/codastre/cli/internal/buildinfo"

var override string

// SetOverride records the resolved --client/$CODASTRE_CLIENT value (cmd
// resolves the flag-wins-over-env precedence before calling this). Called once
// at startup, before any request is sent. Empty means neither was set.
func SetOverride(v string) {
	override = v
}

// Override returns the raw --client/$CODASTRE_CLIENT value, or "" when neither
// was set. Exposed separately from Value so the stdio proxy (internal/mcpshim)
// can rank an explicit override above a sniffed agent `initialize` clientInfo,
// which in turn ranks above Value's own default.
func Override() string {
	return override
}

// Value is the header this process sends when nothing more specific applies:
// the explicit override if set, else this binary's own self-identification,
// "codastre-cli/<version>".
func Value() string {
	if override != "" {
		return override
	}
	v, _, _ := buildinfo.Resolve()
	return "codastre-cli/" + v
}
