package transcript

import (
	"math"
	"os"
	"strings"
	"testing"
)

// apiCall is one main-thread request with an explicit message id and a
// 1-hour cache-write split, optionally issuing one tool call.
func apiCall(ts, msgID string, in, cw1h, cacheRead, out int, toolID, tool, command string) string {
	content := []any{map[string]any{"type": "text", "text": "…"}}
	if toolID != "" {
		content = []any{map[string]any{
			"type": "tool_use", "id": toolID, "name": tool,
			"input": map[string]any{"command": command},
		}}
	}
	return mustJSON(map[string]any{
		"type": "assistant", "sessionId": "sess-1", "timestamp": ts,
		"message": map[string]any{
			"id": msgID, "model": "m", "role": "assistant",
			"usage": map[string]any{
				"input_tokens": in, "output_tokens": out,
				"cache_read_input_tokens":     cacheRead,
				"cache_creation_input_tokens": cw1h,
				"cache_creation":              map[string]any{"ephemeral_1h_input_tokens": cw1h},
			},
			"content": content,
		},
	})
}

// pricedCostState prices model "m" at exactly $0.001 per input-token
// equivalent: 500 one-hour cache writes are 1,000 units for $1.
func pricedCostState() string {
	return mustJSON(map[string]any{
		"type": "cost-state", "sessionId": "sess-1", "totalCostUSD": 1.0,
		"modelUsage": map[string]any{"m": map[string]any{
			"costUSD": 1.0, "cacheCreationInputTokens": 500,
		}},
	})
}

// The worked example: a 400-byte grep result grows the prompt by 300 tokens
// beyond the previous output; a 100-byte codastre result by 80. The grep is
// then re-read once before a compaction empties the window.
func attributionLines() []string {
	return []string{
		userPrompt("2026-09-20T10:00:00.000Z", "q"),
		apiCall("2026-09-20T10:00:01.000Z", "a", 0, 0, 1000, 50, "g1", "Grep", ""),
		toolResult("2026-09-20T10:00:02.000Z", "g1", strings.Repeat("g", 398), false), // 400 bytes as JSON
		apiCall("2026-09-20T10:00:03.000Z", "b", 0, 300, 1050, 20, "c1", "mcp__codastre__QUERY", ""),
		apiCall("2026-09-20T10:00:03.000Z", "b", 0, 300, 1050, 20, "", "", ""), // same id: counted once
		toolResult("2026-09-20T10:00:04.000Z", "c1", strings.Repeat("c", 98), false),
		apiCall("2026-09-20T10:00:05.000Z", "c", 0, 80, 1370, 10, "", "", ""),
		userPrompt("2026-09-20T10:01:00.000Z", "q2"),
		apiCall("2026-09-20T10:01:01.000Z", "d", 0, 400, 0, 10, "", "", ""), // compaction: 400 < 0.6 × 1450
		apiCall("2026-09-20T10:01:02.000Z", "e", 0, 0, 410, 10, "", "", ""),
		pricedCostState(),
	}
}

func TestUsageCountsOncePerMessageID(t *testing.T) {
	sess := parseLines(t, attributionLines())
	if sess.Messages.Messages != 5 || sess.Messages.CacheReadTokens != 1000+1050+1370+410 {
		t.Errorf("messages = %+v, want 5 unique requests", sess.Messages)
	}
}

func TestAttributionSplitsIngestAndCarry(t *testing.T) {
	sess := parseLines(t, attributionLines())
	sess.Finalize()

	grep := sess.CostByClass[ClassTextSearch]
	if grep == nil || grep.IngestTokens != 300 || grep.CarryTokens != 300 {
		t.Fatalf("text-search cost = %+v, want ingest 300, carried once (300)", grep)
	}
	cod := sess.CostByClass[ClassCodastre]
	if cod == nil || cod.IngestTokens != 80 || cod.CarryTokens != 0 {
		t.Fatalf("codastre cost = %+v, want ingest 80, never carried (compaction)", cod)
	}

	// Ingest is uncached (1h write, 2×); carry pays request c's blended rate.
	rateC := (80*weightCacheWrite1h + 1370*weightCacheRead) / 1450
	wantGrepUSD := (300*weightCacheWrite1h + 300*rateC) * 0.001
	if grep.USD == nil || math.Abs(*grep.USD-wantGrepUSD) > 1e-9 {
		t.Errorf("text-search usd = %v, want %v", grep.USD, wantGrepUSD)
	}
	if cod.USD == nil || math.Abs(*cod.USD-80*weightCacheWrite1h*0.001) > 1e-9 {
		t.Errorf("codastre usd = %v", cod.USD)
	}

	// Both results belong to turn 0's episode.
	ep := sess.EpisodeCost[0]
	if ep == nil || ep.IngestTokens != 380 || ep.CarryTokens != 300 {
		t.Errorf("episode 0 cost = %+v", ep)
	}
	if len(sess.EpisodeCost) != 1 {
		t.Errorf("episode costs = %+v, want only the search turn", sess.EpisodeCost)
	}
}

// The prompt total counts each request once (b's repeated id is not
// re-added), so search ingest + carry is a share of it, never above it.
func TestPromptTokensSumMainThreadPrompts(t *testing.T) {
	sess := parseLines(t, attributionLines())
	if want := int64(1000 + 1350 + 1450 + 400 + 410); sess.PromptTokens != want {
		t.Errorf("prompt tokens = %d, want %d", sess.PromptTokens, want)
	}
}

func TestAttributionStaysUnpricedWithoutCostState(t *testing.T) {
	lines := attributionLines()
	sess := parseLines(t, lines[:len(lines)-1])
	sess.Finalize()
	if c := sess.CostByClass[ClassTextSearch]; c == nil || c.USD != nil || c.IngestTokens != 300 {
		t.Errorf("unpriced cost = %+v, want tokens and no usd", c)
	}
}

// Split the same transcript across two collect runs: the second run must
// keep carrying what the first ingested, and key episodes absolutely.
func TestAttributionResumesAcrossCollects(t *testing.T) {
	lines := attributionLines()
	// Through turn 1's prompt, which closes turn 0 — a run only ever
	// commits whole turns.
	root, path := writeTranscript(t, lines[:8])
	state := LoadState("")
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	extra := []string{
		apiCall("2026-09-20T10:01:01.000Z", "d2", 0, 30, 1460, 10, "g2", "Grep", ""),
		toolResult("2026-09-20T10:01:02.000Z", "g2", "xx", false),
		apiCall("2026-09-20T10:01:03.000Z", "e2", 0, 2, 1500, 10, "", "", ""),
		pricedCostState(),
	}
	if _, err := f.WriteString(strings.Join(extra, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Collect(root, state, 0); err != nil {
		t.Fatal(err)
	}

	sess := state.Sessions["sess-1"]
	// The first grep is carried by c, d2 and e2; turn 1's grep by e2 only.
	if ep := sess.EpisodeCost[0]; ep == nil || ep.CarryTokens != 300+300+300+80+80 {
		t.Errorf("episode 0 = %+v, want its 380 tokens carried by d2 and e2 after c", ep)
	}
	if ep := sess.EpisodeCost[1]; ep == nil || ep.IngestTokens != 2 {
		t.Errorf("episode 1 = %+v, want the second turn keyed as ordinal 1", ep)
	}
	if len(sess.EpisodeLog) != 2 || sess.EpisodeLog[1].Ordinal != 1 {
		t.Errorf("episode log = %+v", sess.EpisodeLog)
	}
	// a 1000 + b 1350 + c 1450, then d2 1490 + e2 1502: each request once.
	if want := int64(1000 + 1350 + 1450 + 1490 + 1502); sess.PromptTokens != want {
		t.Errorf("prompt tokens = %d, want %d summed across both collects", sess.PromptTokens, want)
	}
}

func TestStateVersionBumpKeepsTheConsentBoundary(t *testing.T) {
	path := t.TempDir() + "/state.json"
	if err := os.WriteFile(path, []byte(`{"version":2,"report_since":"2026-09-01T00:00:00Z","sessions":{"x":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := LoadState(path)
	if len(s.Sessions) != 0 || s.ReportSince.IsZero() {
		t.Errorf("state = %+v, want sessions dropped and report_since kept", s)
	}
}

func TestModelKeyStripsContextVariant(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-5-5[1m]": "claude-sonnet-5-5",
		"claude-sonnet-5-5":     "claude-sonnet-5-5",
		"[1m]":                  "[1m]",
	} {
		if got := modelKey(in); got != want {
			t.Errorf("modelKey(%q) = %q, want %q", in, got, want)
		}
	}
}
