package clientheader

import (
	"strings"
	"testing"
)

func TestValueDefaultsToCodastreCLIFallback(t *testing.T) {
	SetOverride("")
	t.Cleanup(func() { SetOverride("") })

	if got := Override(); got != "" {
		t.Fatalf("Override() = %q, want empty", got)
	}
	if v := Value(); !strings.HasPrefix(v, "codastre-cli/") {
		t.Fatalf("Value() = %q, want a codastre-cli/<version> fallback", v)
	}
}

func TestSetOverrideWinsOverFallback(t *testing.T) {
	t.Cleanup(func() { SetOverride("") })

	SetOverride("claude-code/1.2.3")
	if got := Override(); got != "claude-code/1.2.3" {
		t.Fatalf("Override() = %q, want %q", got, "claude-code/1.2.3")
	}
	if got := Value(); got != "claude-code/1.2.3" {
		t.Fatalf("Value() = %q, want the override untouched", got)
	}
}
