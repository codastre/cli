package cmd

import "testing"

func TestDefaultClientOverride(t *testing.T) {
	t.Setenv("CODASTRE_CLIENT", "")
	if got := defaultClientOverride(); got != "" {
		t.Errorf("unset CODASTRE_CLIENT → %q, want empty", got)
	}

	t.Setenv("CODASTRE_CLIENT", "claude-code-plugin/0.1.0")
	if got := defaultClientOverride(); got != "claude-code-plugin/0.1.0" {
		t.Errorf("CODASTRE_CLIENT=claude-code-plugin/0.1.0 → %q, want unchanged", got)
	}
}
