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

	// ai-title / custom-title / pr-link: the labels Claude Code shows in its
	// resume picker. Local display only — no upload row has a field for them.
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	PRNumber    int    `json:"prNumber"`

	// The tool result payload, present on the user record that answers a
	// tool_use. Kept as raw JSON so its size is measured without its content
	// being decoded into anything this process keeps.
	ToolUseResult json.RawMessage `json:"toolUseResult"`

	// attachment records; only the hook-context shape is read, and only to
	// recover the search-mode enum (see searchMode).
	Attachment *attachment `json:"attachment"`
}

type attachment struct {
	Type      string   `json:"type"`
	HookEvent string   `json:"hookEvent"`
	Content   []string `json:"content"`
}

type message struct {
	// ID is the API message id. Claude Code writes one transcript record per
	// content block, each repeating the same usage — the id is what makes a
	// request count once.
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Role    string          `json:"role"`
	Usage   *usage          `json:"usage"`
	Content json.RawMessage `json:"content"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type modelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	ThinkingTokens           int64   `json:"thinkingTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
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
