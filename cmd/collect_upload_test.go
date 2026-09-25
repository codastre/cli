package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Upload is opt-in twice. --upload without CODASTRE_USAGE_REPORT=1 must fail
// before anything — including the key fetch — touches the network.
func TestCollectUploadWithoutOptInSendsNothing(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()

	out, err := execRoot(t, "collect", "--upload", "--server", srv.URL, "--key", "k")
	if err == nil || !strings.Contains(err.Error(), "CODASTRE_USAGE_REPORT=1") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if hits != 0 {
		t.Errorf("server was hit %d times without the opt-in", hits)
	}
}

// Without --upload, the opt-in alone sends nothing either: collecting stays local.
func TestCollectWithOptInButNoFlagStaysLocal(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()
	t.Setenv("CODASTRE_SERVER", srv.URL)

	out, err := execRoot(t, "collect")
	if err != nil {
		t.Fatalf("collect: %v\n%s", err, out)
	}
	if hits != 0 || !strings.Contains(out, "nothing uploaded") {
		t.Errorf("hits=%d out=%s", hits, out)
	}
}

// The first upload-enabled run records consent; the staged transcript predates
// it, so it stays local and no request is made.
func TestCollectUploadFirstRunRecordsConsentOnly(t *testing.T) {
	stageTranscript(t)
	t.Setenv("CODASTRE_USAGE_REPORT", "1")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()

	out, err := execRoot(t, "collect", "--upload", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("collect --upload: %v\n%s", err, out)
	}
	if !strings.Contains(out, "upload consent recorded") || !strings.Contains(out, "1 started before the consent boundary") {
		t.Errorf("output:\n%s", out)
	}
	if hits != 0 {
		t.Errorf("pre-consent history reached the server (%d hits)", hits)
	}
}
