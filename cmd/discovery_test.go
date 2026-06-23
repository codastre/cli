package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func discoveryServer(t *testing.T, dashURL string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/system/discovery", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dashboard_url":"` + dashURL + `","server_base_url":"http://srv","version":"1.2.3"}`))
	})
	return httptest.NewServer(mux)
}

func TestDiscoverReturnsDashboardURL(t *testing.T) {
	srv := discoveryServer(t, "https://app.example.com")
	defer srv.Close()

	d := discover(context.Background(), srv.URL)
	if d == nil {
		t.Fatal("discover returned nil for a healthy server")
	}
	if d.DashboardURL != "https://app.example.com" {
		t.Fatalf("dashboard_url = %q", d.DashboardURL)
	}
	if d.Version != "1.2.3" {
		t.Fatalf("version = %q", d.Version)
	}
}

func TestDiscoverNilOnUnreachable(t *testing.T) {
	// Closed server → connection refused → best-effort nil, not a panic/error.
	srv := discoveryServer(t, "https://x")
	srv.Close()
	if d := discover(context.Background(), srv.URL); d != nil {
		t.Fatal("expected nil from an unreachable server")
	}
}

func TestResolveDashboardURLPrecedence(t *testing.T) {
	srv := discoveryServer(t, "https://discovered.example.com")
	defer srv.Close()

	t.Run("flag wins over everything", func(t *testing.T) {
		t.Setenv("CODASTRE_DASHBOARD_URL", "https://env.example.com")
		dashboardURL = "https://flag.example.com/"
		defer func() { dashboardURL = "" }()
		got, warn := resolveDashboardURL(srv.URL)
		if got != "https://flag.example.com" || warn != "" {
			t.Fatalf("got %q warn %q", got, warn)
		}
	})

	t.Run("env beats discovery", func(t *testing.T) {
		t.Setenv("CODASTRE_DASHBOARD_URL", "https://env.example.com/")
		dashboardURL = ""
		got, warn := resolveDashboardURL(srv.URL)
		if got != "https://env.example.com" || warn != "" {
			t.Fatalf("got %q warn %q", got, warn)
		}
	})

	t.Run("discovery when no flag or env", func(t *testing.T) {
		t.Setenv("CODASTRE_DASHBOARD_URL", "")
		dashboardURL = ""
		got, warn := resolveDashboardURL(srv.URL)
		if got != "https://discovered.example.com" || warn != "" {
			t.Fatalf("got %q warn %q", got, warn)
		}
	})

	t.Run("localhost fallback with warning when discovery fails", func(t *testing.T) {
		t.Setenv("CODASTRE_DASHBOARD_URL", "")
		dashboardURL = ""
		got, warn := resolveDashboardURL("http://127.0.0.1:1") // nothing listening
		if got != "http://localhost:3000" {
			t.Fatalf("got %q, want localhost fallback", got)
		}
		if warn == "" {
			t.Fatal("expected a warning when falling back to localhost")
		}
	})
}
