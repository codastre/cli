package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// integrationsSource is where a deployment publishes its Codastre agent
// integrations — the commands, skills and hooks a harness installs on top of the
// MCP connection. The server resolves it (server/domain/integrations.py) and
// returns a complete set on GET /v1/system/discovery; the hosted values below are
// the fallback for an older server, an unreachable one, or a deployment that
// predates the field.
type integrationsSource struct {
	Source          string `json:"source"`
	MarketplaceName string `json:"marketplace_name"`
	Label           string `json:"label"`
	URL             string `json:"url"`
}

var hostedIntegrations = integrationsSource{
	Source:          "codastre/integrations",
	MarketplaceName: "codastre-plugins",
	Label:           "codastre/integrations",
	URL:             "https://github.com/codastre/integrations",
}

// resolveIntegrationsSource picks the deployment's integrations source, falling
// back to the hosted one. Both install fields must be present to use the server's
// answer: a source paired with a borrowed default name would print a command that
// cannot resolve, which is worse than pointing at the public repository.
func resolveIntegrationsSource(d *serverDiscovery) integrationsSource {
	if d == nil || d.Integrations.Source == "" || d.Integrations.MarketplaceName == "" {
		return hostedIntegrations
	}
	return d.Integrations
}

// printIntegrationHint advertises the integration for the harness just connected.
// Registering the MCP server only makes Codastre available to the agent; the
// integration is what makes it reach for Codastre rather than falling back to grep.
//
// Only harnesses with an integration available get a hint — today that is Claude
// Code, which installs through its plugin marketplace. Best-effort by design:
// discovery failures fall back to the hosted source rather than blocking or
// failing a connect that has already succeeded.
func printIntegrationHint(cmd *cobra.Command, target, serverURL string) {
	if target != "claude" {
		return
	}
	// cobra only populates the context during Execute; a command constructed
	// directly (tests, or any non-Execute caller) carries a nil one, and
	// context.WithTimeout panics on that.
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	src := resolveIntegrationsSource(discover(ctx, serverURL))
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\nOptional — the Codastre integration for Claude Code adds slash commands,\n"+
		"skills that load themselves, and hooks that steer the agent to Codastre\n"+
		"instead of grep. Published in %s:\n\n", src.Label)
	fmt.Fprintf(out, "  claude plugin marketplace add %s\n", src.Source)
	fmt.Fprintf(out, "  claude plugin install codastre@%s --scope project\n", src.MarketplaceName)
}
