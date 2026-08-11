package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codastre/cli/internal/checkouts"
)

// federatedHydration returns three lookups of the identical type
// func(string) (string, bool); before they were fields, a swap at either the
// return or the call site compiled silently and made the proxy join repo paths
// onto remote URLs — nothing hydrated and nothing errored. This pins each field
// to its own meaning.
func TestFederatedHydration_LookupsAreDistinct(t *testing.T) {
	// Hermetic HOME: checkouts.Lookup reads ~/.config/codastre/checkouts.json.
	t.Setenv("HOME", t.TempDir())

	const (
		alphaID  = "11111111-1111-1111-1111-111111111111"
		alphaURL = "github.com/test-only/alpha"
		betaID   = "22222222-2222-2222-2222-222222222222"
		betaURL  = "github.com/test-only/beta"
	)
	alphaDir := t.TempDir()
	if err := checkouts.Remember(alphaURL, alphaDir); err != nil {
		t.Fatalf("remember checkout: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/repos" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"repo_id":"` + alphaID + `","remote_url":"` + alphaURL + `","masking_scheme":"none"},
			{"repo_id":"` + betaID + `","remote_url":"` + betaURL + `","masking_scheme":"hmac"}
		],"next_cursor":null}`))
	}))
	defer srv.Close()

	// cwdRoot "" so no repo is treated as the CWD checkout; the URLs above are
	// deliberately not this repo's origin, so CWDRepoID stays empty either way.
	hy := federatedHydration(srv.URL, "test-key", "")

	if got, _ := hy.Scheme(alphaID); got != "none" {
		t.Errorf("Scheme(alpha) = %q, want %q", got, "none")
	}
	if got, _ := hy.Scheme(betaID); got != "hmac" {
		t.Errorf("Scheme(beta) = %q, want %q", got, "hmac")
	}
	if got, _ := hy.RemoteURL(alphaID); got != alphaURL {
		t.Errorf("RemoteURL(alpha) = %q, want %q", got, alphaURL)
	}
	// RootFor resolves through the checkout registry — a local dir, never a URL.
	if got, ok := hy.RootFor(alphaID); !ok || got != alphaDir {
		t.Errorf("RootFor(alpha) = %q (ok=%v), want the registered checkout %q", got, ok, alphaDir)
	}
	// beta has no registered checkout: a miss, not a fallback to some other tree.
	if got, ok := hy.RootFor(betaID); ok {
		t.Errorf("RootFor(beta) = %q, want a miss (no checkout registered)", got)
	}
	// Unknown repo misses on every lookup.
	for name, fn := range map[string]func(string) (string, bool){
		"Scheme": hy.Scheme, "RootFor": hy.RootFor, "RemoteURL": hy.RemoteURL,
	} {
		if got, ok := fn("unknown-repo"); ok {
			t.Errorf("%s(unknown) = %q, want a miss", name, got)
		}
	}
}

// A server that can't be listed must degrade to CWD-only, UnmaskPath-gated
// behavior: every lookup nil so mcpshim falls back, rather than a half-built
// struct that reports "no checkout" for repos it never learned about.
func TestFederatedHydration_ListFailureReturnsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	hy := federatedHydration(srv.URL, "test-key", "/some/root")

	if hy.Scheme != nil || hy.RootFor != nil || hy.RemoteURL != nil || hy.CWDRepoID != "" {
		t.Errorf("want zero hydrationLookups on list failure, got %+v", hy)
	}
}
