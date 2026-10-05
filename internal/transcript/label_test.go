package transcript

import (
	"strings"
	"testing"
)

func TestParseCapturesSessionLabels(t *testing.T) {
	lines := []string{
		userPrompt("2026-09-20T10:00:00.000Z", "hi"),
		mustJSON(map[string]any{"type": "ai-title", "sessionId": "sess-1", "aiTitle": "First guess"}),
		mustJSON(map[string]any{"type": "ai-title", "sessionId": "sess-1", "aiTitle": " Fix the parser "}),
		mustJSON(map[string]any{"type": "pr-link", "sessionId": "sess-1", "prNumber": 212}),
		costState(),
	}
	s, _, err := Parse(strings.NewReader(strings.Join(lines, "\n")+"\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.AITitle != "Fix the parser" || s.PRNumber != 212 || s.CustomTitle != "" {
		t.Fatalf("labels = %q / %q / %d", s.AITitle, s.CustomTitle, s.PRNumber)
	}

	// A later increment's /rename lands beside the generated title; an
	// increment without labels keeps both.
	inc, _, err := Parse(strings.NewReader(mustJSON(map[string]any{
		"type": "custom-title", "sessionId": "sess-1", "customTitle": "Mine",
	})+"\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Merge(inc)
	s.Merge(newSession("sess-1"))
	if s.CustomTitle != "Mine" || s.AITitle != "Fix the parser" || s.PRNumber != 212 {
		t.Fatalf("after merge = %q / %q / %d", s.AITitle, s.CustomTitle, s.PRNumber)
	}
}
