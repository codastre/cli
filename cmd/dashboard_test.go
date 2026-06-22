package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandoffHandlerServesKeyOnceWithMatchingNonce(t *testing.T) {
	done := make(chan error, 1)
	h := handoffHandler("secret-key", "n0nce", "http://localhost:3000", done)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Wrong nonce → forbidden, no key.
	bad, err := http.Get(srv.URL + "/?nonce=wrong")
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong nonce: got %d, want 403", bad.StatusCode)
	}

	// Correct nonce → key + CORS header + done signal.
	ok, err := http.Get(srv.URL + "/?nonce=n0nce")
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("matching nonce: got %d, want 200", ok.StatusCode)
	}
	if got := ok.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("CORS origin: got %q", got)
	}
	var body struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(ok.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.APIKey != "secret-key" {
		t.Fatalf("api_key: got %q", body.APIKey)
	}
	select {
	case <-done:
	default:
		t.Fatal("expected done signal after serving the key")
	}

	// Single-use: a second correct request is now refused.
	again, err := http.Get(srv.URL + "/?nonce=n0nce")
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusForbidden {
		t.Fatalf("second request: got %d, want 403 (single-use)", again.StatusCode)
	}
}

func TestOriginOf(t *testing.T) {
	got, err := originOf("https://app.example.com/cli-auth?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://app.example.com" {
		t.Fatalf("got %q", got)
	}
	if _, err := originOf("not-a-url"); err == nil {
		t.Fatal("expected error for relative URL")
	}
}
