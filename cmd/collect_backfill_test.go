package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/codastre/cli/internal/transcript"
)

func countingServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/me/usage/session-key":
			_, _ = io.WriteString(w, `{"key":"`+strings.Repeat("ab", 32)+`","algorithm":"hmac-sha256"}`)
		case "/v1/usage/episodes", "/v1/usage/sessions":
			var p map[string][]json.RawMessage
			_ = json.Unmarshal(body, &p)
			n := len(p["episodes"]) + len(p["sessions"])
			_ = json.NewEncoder(w).Encode(map[string]int{"received": n, "inserted": n})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestBackfillPreviewSendsNothing(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	srv, hits := countingServer(t)

	out, err := execRoot(t, "collect", "--backfill", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("collect --backfill: %v\n%s", err, out)
	}
	if *hits != 0 {
		t.Errorf("a preview hit the server %d times", *hits)
	}
	if !strings.Contains(out, "nothing left this machine") || !strings.Contains(out, "would send:") {
		t.Errorf("output:\n%s", out)
	}

	out, err = execRoot(t, "collect", "--backfill", "--json", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("collect --backfill --json: %v\n%s", err, out)
	}
	if *hits != 0 || !strings.Contains(out, `"session_ref": "hmac-sha256(\u003ctenant usage key\u003e, \u003csession id\u003e)"`) &&
		!strings.Contains(out, `hmac-sha256(<tenant usage key>, <session id>)`) {
		t.Errorf("hits=%d json preview:\n%s", *hits, out)
	}
}

func TestBackfillUploadOffTerminalNeedsYes(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	srv, hits := countingServer(t)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = orig })

	out, err := execRoot(t, "collect", "--backfill", "--upload", "--server", srv.URL, "--key", "k")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if *hits != 0 {
		t.Errorf("unconfirmed backfill hit the server %d times", *hits)
	}
}

func TestBackfillUploadOnTerminalNeedsExactYes(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	srv, hits := countingServer(t)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = orig })

	rootCmd.SetIn(strings.NewReader("y\n"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	out, err := execRoot(t, "collect", "--backfill", "--upload", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *hits != 0 || !strings.Contains(out, "nothing was uploaded") {
		t.Errorf("hits=%d out:\n%s", *hits, out)
	}
}

func TestBackfillUploadWithYesSendsHistoryAndKeepsBoundary(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	srv, hits := countingServer(t)

	out, err := execRoot(t, "collect", "--backfill", "--upload", "--yes", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *hits == 0 || !strings.Contains(out, "consent boundary did not move") {
		t.Errorf("hits=%d out:\n%s", *hits, out)
	}
	state := transcript.LoadState(os.Getenv("CODASTRE_COLLECT_STATE"))
	if !state.ReportSince.IsZero() {
		t.Errorf("backfill recorded a consent boundary: %s", state.ReportSince)
	}

	// Re-running sends nothing: the marks advanced.
	before := *hits
	out, err = execRoot(t, "collect", "--backfill", "--upload", "--yes", "--server", srv.URL, "--key", "k")
	if err != nil || *hits != before || !strings.Contains(out, "nothing from before") {
		t.Errorf("re-run err=%v hits=%d→%d out:\n%s", err, before, *hits, out)
	}
}

func TestYesWithoutBackfillIsRefused(t *testing.T) {
	stageTranscript(t)
	if _, err := execRoot(t, "collect", "--yes"); err == nil {
		t.Error("--yes without --backfill must be refused")
	}
}
