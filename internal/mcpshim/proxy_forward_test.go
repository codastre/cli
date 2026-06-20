package mcpshim

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fastMCPStub mimics FastMCP's streamable-HTTP transport: it rejects any request
// whose Accept header lacks both application/json and text/event-stream with a
// 406 (the real cause of the -32000 "failed to connect" the proxy surfaced).
// Requests carrying an "id" get a JSON-RPC result; notifications (no id) are
// acked with 202 and an empty body.
func fastMCPStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
			http.Error(w, "Not Acceptable: Client must accept both application/json and text/event-stream", http.StatusNotAcceptable)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		buf, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(buf), `"id"`) {
			w.WriteHeader(http.StatusAccepted) // notification ack: no body
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
}

func TestForwardMessageSendsAcceptHeader(t *testing.T) {
	srv := fastMCPStub(t)
	defer srv.Close()

	cfg := Config{ServerURL: srv.URL, APIKey: "test-key"}
	resp, err := forwardMessage(cfg, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatalf("forwardMessage returned error (Accept header missing?): %v", err)
	}
	if !strings.Contains(string(resp), `"result"`) {
		t.Fatalf("unexpected response: %s", resp)
	}
}

func TestRunRoundTripAndNotification(t *testing.T) {
	srv := fastMCPStub(t)
	defer srv.Close()

	cfg := Config{ServerURL: srv.URL, APIKey: "test-key"}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n",
	)
	var out strings.Builder
	if err := Run(cfg, in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// The initialize request gets exactly one response line; the notification
	// (202, empty body) must produce no line at all.
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"result"`) {
		t.Fatalf("initialize response missing result: %q", lines[0])
	}
}
