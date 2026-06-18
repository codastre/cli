// Package mcpclient is a minimal one-shot client for the codastre MCP endpoint.
//
// Unlike the stdio proxy in internal/mcpshim (which forwards an agent's own
// JSON-RPC stream), this package originates a single `tools/call` request and
// returns the tool's payload. The server's MCP endpoint is stateless
// (stateless_http=True, json_response=True), so no `initialize` handshake is
// required — a lone POST with the right Accept header and a Bearer token works.
//
// It powers the `codastre query` and `codastre graph` subcommands, giving
// agents and humans index-free QUERY/GRAPH access (docs/mcp-index-free-access.md)
// without standing up an MCP connection.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Config configures a one-shot tool call.
type Config struct {
	ServerURL string // base URL, e.g. http://localhost:8000 (no trailing /mcp)
	APIKey    string
}

// ToolError is a tool-level error returned in the QUERY/GRAPH envelope, e.g.
// {"error": "REPO_NOT_INDEXED"}. It is distinct from a transport/HTTP failure:
// the call reached the tool and the tool declined to answer.
type ToolError struct {
	Code   string // the "error" field, e.g. REPO_NOT_INDEXED, RETRIEVAL_UNAVAILABLE
	Detail string // optional "detail" field
}

func (e *ToolError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return e.Code
}

// jsonRPCResponse mirrors the JSON-RPC envelope FastMCP returns for tools/call.
type jsonRPCResponse struct {
	Error  *jsonRPCError   `json:"error"`
	Result *toolCallResult `json:"result"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallResult struct {
	Content           []contentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Call invokes the named MCP tool with the given arguments and returns the
// tool's payload (the QUERY/GRAPH envelope) as raw JSON.
//
// Errors:
//   - a transport or HTTP >= 400 failure → a plain error;
//   - a JSON-RPC protocol error → a plain error;
//   - a tool-level {"error": ...} envelope → a *ToolError.
func Call(ctx context.Context, cfg Config, tool string, args map[string]any) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	url := strings.TrimRight(cfg.ServerURL, "/") + "/mcp"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Streamable-HTTP transport requires both media types in Accept even when
	// the server is configured for JSON responses.
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact server: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("unauthorized (HTTP %d) — run `codastre login` or pass --key", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var env jsonRPCResponse
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", env.Error.Code, env.Error.Message)
	}
	if env.Result == nil {
		return nil, fmt.Errorf("malformed response: no result")
	}

	payload, err := extractPayload(env.Result)
	if err != nil {
		return nil, err
	}

	// A tool that declines to answer returns {"error": "...", "detail"?: "..."}.
	if te := toolError(payload); te != nil {
		return nil, te
	}
	return payload, nil
}

// extractPayload pulls the tool's JSON payload from the tools/call result.
// Prefers structuredContent (already an object); falls back to the first text
// content block, which carries the JSON-serialized envelope.
func extractPayload(r *toolCallResult) (json.RawMessage, error) {
	if len(r.StructuredContent) > 0 && !bytes.Equal(r.StructuredContent, []byte("null")) {
		return r.StructuredContent, nil
	}
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			return json.RawMessage(c.Text), nil
		}
	}
	if r.IsError {
		return nil, fmt.Errorf("tool reported an error with no payload")
	}
	return nil, fmt.Errorf("malformed response: empty tool result")
}

// toolError returns a *ToolError if the payload carries an "error" field.
func toolError(payload json.RawMessage) *ToolError {
	var probe struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return nil
	}
	if probe.Error == "" {
		return nil
	}
	return &ToolError{Code: probe.Error, Detail: probe.Detail}
}
