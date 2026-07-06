package cmd

import (
	"os"

	"github.com/codastre/cli/internal/config"
)

// defaultServerURL resolves the default server URL in precedence order: the
// CODASTRE_SERVER env var, then the persisted CLI config (recorded by
// `codastre login --server`), then the hosted API. This lets a self-hosted
// deployment be configured once instead of passing --server to every command.
func defaultServerURL() string {
	if v := os.Getenv("CODASTRE_SERVER"); v != "" {
		return v
	}
	if v, ok := config.ServerURL(); ok {
		return v
	}
	return "https://api.codastre.com"
}
