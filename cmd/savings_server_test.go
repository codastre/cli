package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The shape of GET /v1/me/usage in M1: counters plus the receipt. `episodes`
// is deliberately present and unknown to the CLI — an M3 server must not break
// an M1 client.
const meUsageFixture = `{
  "window": "30d",
  "calls": 1234,
  "envelope_tokens": 98765,
  "repos_touched_ge2": 7,
  "episodes": {"total": 3},
  "receipt": {
    "formula": "calls = count(usage_events where principal = you)",
    "inputs": {"principal": "self", "window": "30d"},
    "n": 1234,
    "coverage": null,
    "provenance": "plane 1 · usage_events · server",
    "caveat": "counts only; no counterfactual is computed",
    "rerun_cmd": "codastre savings --source server --window 30d"
  }
}`

// execServerSavings runs `codastre savings --source server …` against a stub
// server, isolating the collector state and the server/key flags.
func execServerSavings(t *testing.T, serverURL string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("CODASTRE_COLLECT_STATE", filepath.Join(t.TempDir(), "empty-state.json"))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		savingsWindow, savingsJSON, savingsLogPath, savingsSource = "30d", false, "", "auto"
		savingsServerURL, savingsKey = defaultServerURL(), ""
	})
	full := append([]string{"savings", "--source", "server", "--server", serverURL, "--key", "k"}, args...)
	rootCmd.SetArgs(full)
	err := rootCmd.Execute()
	return out.String(), err
}

func stubUsageServer(t *testing.T, status int, body string) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/me/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("window"); got == "" {
			t.Errorf("no window query parameter")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestSavingsServerSourceRendersCountersAndReceipt(t *testing.T) {
	srv, _ := stubUsageServer(t, http.StatusOK, meUsageFixture)
	got, err := execServerSavings(t, srv.URL)
	if err != nil {
		t.Fatalf("savings --source server: %v\n%s", err, got)
	}
	for _, want := range []string{
		"codastre savings — server, 30d window",
		"/v1/me/usage",
		"1,234",
		"98,765",
		// The receipt, verbatim: the CLI must not reword the server's own
		// explanation of the number.
		"calls = count(usage_events where principal = you)",
		"plane 1 · usage_events · server",
		"counts only; no counterfactual is computed",
		"codastre savings --source server --window 30d",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// --source auto is local-only: the server source is opt-in, and sources are
// never summed.
func TestSavingsAutoNeverCallsServer(t *testing.T) {
	srv, hits := stubUsageServer(t, http.StatusOK, meUsageFixture)
	t.Setenv("CODASTRE_SERVER", srv.URL)
	t.Setenv("CODASTRE_TOKEN_LOG", writeFixture(t))
	if out := runSavingsCmd(t, "--window", "all"); strings.Contains(out, "server,") {
		t.Errorf("auto reported the server source:\n%s", out)
	}
	if *hits != 0 {
		t.Errorf("auto made %d server call(s), want 0", *hits)
	}
}

func TestSavingsServerPublishesNoSavedOrBaseline(t *testing.T) {
	srv, _ := stubUsageServer(t, http.StatusOK, meUsageFixture)

	human, err := execServerSavings(t, srv.URL)
	if err != nil {
		t.Fatalf("human: %v", err)
	}
	for _, banned := range []string{"saved", "baseline"} {
		if strings.Contains(strings.ToLower(human), banned) {
			t.Errorf("human output contains %q:\n%s", banned, human)
		}
	}

	jsonOut, err := execServerSavings(t, srv.URL, "--json")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, jsonOut)
	}
	for _, banned := range []string{"saved", "saved_tokens_est", "baseline"} {
		if _, ok := payload[banned]; ok {
			t.Errorf("json payload carries a %q field", banned)
		}
	}
	// Passthrough: an unknown (future) field survives the round trip.
	if _, ok := payload["episodes"]; !ok {
		t.Errorf("--json did not pass the server payload through:\n%s", jsonOut)
	}
	if payload["calls"].(float64) != 1234 {
		t.Errorf("calls = %v", payload["calls"])
	}
}

func TestSavingsServerErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"unauthenticated", http.StatusUnauthorized, "codastre login"},
		{"endpoint absent", http.StatusNotFound, "--source transcript"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := stubUsageServer(t, tc.status, `{"detail":"nope"}`)
			out, err := execServerSavings(t, srv.URL)
			if err == nil {
				t.Fatalf("expected an error:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		srv, _ := stubUsageServer(t, http.StatusOK, meUsageFixture)
		url := srv.URL
		srv.Close()
		out, err := execServerSavings(t, url)
		if err == nil {
			t.Fatalf("expected an error:\n%s", out)
		}
		if !strings.Contains(err.Error(), "cannot reach") {
			t.Errorf("error = %q", err)
		}
	})
}

// The three failures must not read alike: each names a different fix.
func TestSavingsServerErrorsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		srv, _ := stubUsageServer(t, status, `{}`)
		_, err := execServerSavings(t, srv.URL)
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if seen[err.Error()] {
			t.Errorf("duplicate error message: %q", err)
		}
		seen[err.Error()] = true
	}
}

func TestSavingsRejectsUnknownSourceNamingServer(t *testing.T) {
	out, err := execSavings(t, "--source", "nope")
	if err == nil {
		t.Fatalf("expected an error:\n%s", out)
	}
	if !strings.Contains(err.Error(), "server") || !strings.Contains(err.Error(), "transcript") {
		t.Errorf("error = %q, want the full source list", err)
	}
}
