package mcpshim

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/codastre/cli/internal/clientheader"
)

func TestInitializeClientHeader(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantOK  bool
		wantHdr string
	}{
		{
			name:    "name and version",
			line:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code","version":"2.1.0"}}}`,
			wantOK:  true,
			wantHdr: "claude-code/2.1.0",
		},
		{
			name:    "name only",
			line:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code"}}}`,
			wantOK:  true,
			wantHdr: "claude-code/",
		},
		{
			name:   "wrong method is ignored",
			line:   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"clientInfo":{"name":"claude-code"}}}`,
			wantOK: false,
		},
		{
			name:   "no clientInfo",
			line:   `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
			wantOK: false,
		},
		{
			name:   "malformed json",
			line:   `not json`,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := initializeClientHeader([]byte(tc.line))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.wantHdr {
				t.Fatalf("header = %q, want %q", got, tc.wantHdr)
			}
		})
	}
}

func TestClientHeaderForPrecedence(t *testing.T) {
	t.Cleanup(func() { clientheader.SetOverride("") })

	clientheader.SetOverride("")
	if got := clientHeaderFor("claude-code/1.0"); got != "claude-code/1.0" {
		t.Fatalf("sniffed clientInfo should win with no override, got %q", got)
	}
	if got := clientHeaderFor(""); !strings.HasPrefix(got, "codastre-cli/") {
		t.Fatalf("fallback should apply with no override and nothing sniffed, got %q", got)
	}

	clientheader.SetOverride("custom/9.9")
	if got := clientHeaderFor("claude-code/1.0"); got != "custom/9.9" {
		t.Fatalf("explicit override should win over sniffed clientInfo, got %q", got)
	}
}

// TestRunSendsSniffedClientHeader drives Run end-to-end (as
// TestRunRoundTripAndNotification does) but asserts the server actually
// receives the sniffed initialize clientInfo as X-Codastre-Client — on the
// initialize call itself and on a subsequent tools/call in the same session.
func TestRunSendsSniffedClientHeader(t *testing.T) {
	clientheader.SetOverride("")
	t.Cleanup(func() { clientheader.SetOverride("") })

	var mu sync.Mutex
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("X-Codastre-Client"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer srv.Close()

	cfg := Config{ServerURL: srv.URL, APIKey: "test-key"}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code","version":"2.1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"QUERY","arguments":{}}}` + "\n",
	)
	var out strings.Builder
	if err := Run(cfg, in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("expected 2 requests, got %d: %v", len(headers), headers)
	}
	for i, h := range headers {
		if h != "claude-code/2.1.0" {
			t.Fatalf("request %d: X-Codastre-Client = %q, want %q", i, h, "claude-code/2.1.0")
		}
	}
}

// TestRunFallsBackWithoutInitialize confirms a session that never sends
// `initialize` still self-identifies as the CLI, rather than an empty header.
func TestRunFallsBackWithoutInitialize(t *testing.T) {
	clientheader.SetOverride("")
	t.Cleanup(func() { clientheader.SetOverride("") })

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Codastre-Client")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer srv.Close()

	cfg := Config{ServerURL: srv.URL, APIKey: "test-key"}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"QUERY","arguments":{}}}` + "\n")
	var out strings.Builder
	if err := Run(cfg, in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.HasPrefix(got, "codastre-cli/") {
		t.Fatalf("X-Codastre-Client = %q, want a codastre-cli/<version> fallback", got)
	}
}

// TestRunOverrideWinsOverSniffedInitialize confirms an explicit
// --client/$CODASTRE_CLIENT override outranks even a real initialize
// handshake, per the plan's precedence (client-attribution-plan.md §8d).
func TestRunOverrideWinsOverSniffedInitialize(t *testing.T) {
	clientheader.SetOverride("codastre-cli-plugin/9.9.9")
	t.Cleanup(func() { clientheader.SetOverride("") })

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Codastre-Client")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer srv.Close()

	cfg := Config{ServerURL: srv.URL, APIKey: "test-key"}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code","version":"2.1.0"}}}` + "\n",
	)
	var out strings.Builder
	if err := Run(cfg, in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got != "codastre-cli-plugin/9.9.9" {
		t.Fatalf("X-Codastre-Client = %q, want the override", got)
	}
}
