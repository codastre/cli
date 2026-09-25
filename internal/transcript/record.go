package transcript

import "encoding/json"

// Raw record shapes. Only the fields that become counters are declared —
// prompts, assistant text and tool inputs are deliberately left undeclared so
// they are never even materialised, let alone stored.
type record struct {
	Type        string   `json:"type"`
	SessionID   string   `json:"sessionId"`
	Timestamp   string   `json:"timestamp"`
	Cwd         string   `json:"cwd"`
	GitBranch   string   `json:"gitBranch"`
	Version     string   `json:"version"`
	IsMeta      bool     `json:"isMeta"`
	IsSidechain bool     `json:"isSidechain"`
	Message     *message `json:"message"`

	// cost-state
	TotalCostUSD      float64               `json:"totalCostUSD"`
	TotalDuration     int64                 `json:"totalDuration"`
	TotalAPIDuration  int64                 `json:"totalAPIDuration"`
	TotalToolDuration int64                 `json:"totalToolDuration"`
	TotalLinesAdded   int                   `json:"totalLinesAdded"`
	TotalLinesRemoved int                   `json:"totalLinesRemoved"`
	ModelUsage        map[string]modelUsage `json:"modelUsage"`

	// The tool result payload, present on the user record that answers a
	// tool_use. Kept as raw JSON so its size is measured without its content
	// being decoded into anything this process keeps.
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type message struct {
	Role    string          `json:"role"`
	Usage   *usage          `json:"usage"`
	Content json.RawMessage `json:"content"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokensDetails      struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type modelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	ThinkingTokens           int64 `json:"thinkingTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// contentBlock covers the three block shapes that matter: the tool_use that
// names a call, the tool_result that answers it, and plain text (counted only
// to tell a real user turn from a tool-result carrier record).
type contentBlock struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Input     struct {
		Command string `json:"command"`
	} `json:"input"`
}
