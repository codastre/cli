package unmask

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func capabilityServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/capabilities" || r.URL.Query().Get("repo_id") != "repo-1" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestSyncEnabledReadsServerAnswer(t *testing.T) {
	srv := capabilityServer(t, http.StatusOK, `{"sync":{"enabled":false}}`)
	defer srv.Close()
	if SyncEnabled(srv.URL, "tok", "repo-1") {
		t.Fatal("want disabled")
	}
}

// An old server (no endpoint) or a malformed answer must never stop syncing:
// the server gates SYNC itself, so the client fails open.
func TestSyncEnabledFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, `{"detail":"Not Found"}`},
		{http.StatusOK, `{}`},
		{http.StatusOK, `not json`},
		{http.StatusInternalServerError, ``},
	} {
		srv := capabilityServer(t, tc.status, tc.body)
		if !SyncEnabled(srv.URL, "tok", "repo-1") {
			t.Errorf("status=%d body=%q: want fail-open enabled", tc.status, tc.body)
		}
		srv.Close()
	}
}
