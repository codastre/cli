package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rpcEnvelope wraps a tool payload in the JSON-RPC tools/call result shape that
// FastMCP returns, with the payload serialized into a text content block.
func rpcEnvelope(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	text, _ := json.Marshal(payload)
	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": string(text)}},
		},
	}
	b, _ := json.Marshal(env)
	return b
}

func newServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Errorf("Accept header = %q, want streamable-http media types", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("Authorization = %q, want Bearer k", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

func TestCall_ExtractsPayloadFromTextContent(t *testing.T) {
	srv := newServer(t, http.StatusOK, rpcEnvelope(t, map[string]any{
		"status":  "ok",
		"results": []any{map[string]any{"path_token": "a/b.go"}},
	}))
	defer srv.Close()

	got, err := Call(context.Background(), Config{ServerURL: srv.URL, APIKey: "k"}, "QUERY",
		map[string]any{"query_text": "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var env struct {
		Status  string           `json:"status"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(got, &env); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if env.Status != "ok" || len(env.Results) != 1 {
		t.Fatalf("payload = %s, want status ok with 1 result", got)
	}
}

func TestCall_PrefersStructuredContent(t *testing.T) {
	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": `{"status":"ignored"}`}},
			"structuredContent": map[string]any{"status": "ok"},
		},
	}
	body, _ := json.Marshal(env)
	srv := newServer(t, http.StatusOK, body)
	defer srv.Close()

	got, err := Call(context.Background(), Config{ServerURL: srv.URL, APIKey: "k"}, "QUERY", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var probe struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(got, &probe)
	if probe.Status != "ok" {
		t.Fatalf("expected structuredContent to win, got %s", got)
	}
}

func TestCall_ToolError(t *testing.T) {
	srv := newServer(t, http.StatusOK, rpcEnvelope(t, map[string]any{"error": "REPO_NOT_INDEXED"}))
	defer srv.Close()

	_, err := Call(context.Background(), Config{ServerURL: srv.URL, APIKey: "k"}, "QUERY", nil)
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %v", err)
	}
	if te.Code != "REPO_NOT_INDEXED" {
		t.Fatalf("ToolError.Code = %q", te.Code)
	}
}

func TestCall_Unauthorized(t *testing.T) {
	srv := newServer(t, http.StatusUnauthorized, []byte(`{"error":"UNAUTHENTICATED"}`))
	defer srv.Close()

	_, err := Call(context.Background(), Config{ServerURL: srv.URL, APIKey: "k"}, "QUERY", nil)
	if err == nil {
		t.Fatal("expected error for HTTP 401")
	}
	var te *ToolError
	if errors.As(err, &te) {
		t.Fatal("401 should be a transport error, not a ToolError")
	}
}
