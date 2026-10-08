package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/codastre/cli/internal/selfupdate"
)

// selfUpdateArchive builds a release archive for the host platform holding body.
func selfUpdateArchive(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("codastre.exe")
		_, _ = w.Write(body)
		_ = zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "codastre", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// runSelfUpdateAgainst serves release v<latest> and runs `self-update args…`
// as if the installed binary at exe were version current.
func runSelfUpdateAgainst(t *testing.T, current, latest, exe string, args ...string) (string, error) {
	t.Helper()
	archive := selfUpdateArchive(t, []byte("NEW"))
	name := selfupdate.ArchiveName(latest, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/codastre/cli/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(selfupdate.Release{Tag: "v" + latest, Assets: []selfupdate.Asset{
			{Name: name, URL: srv.URL + "/dl/" + name},
			{Name: "checksums.txt", URL: srv.URL + "/dl/checksums.txt"},
		}})
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	origClient, origExe, origCurrent := newSelfUpdateClient, selfUpdateExecutable, selfUpdateCurrent
	newSelfUpdateClient = func() *selfupdate.Client {
		return &selfupdate.Client{APIBase: srv.URL, Repo: selfupdate.DefaultRepo, HTTP: srv.Client()}
	}
	selfUpdateExecutable = func() (string, error) { return exe, nil }
	selfUpdateCurrent = func() string { return current }
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	defer func() {
		newSelfUpdateClient, selfUpdateExecutable, selfUpdateCurrent = origClient, origExe, origCurrent
		selfUpdateCheck, selfUpdateVersion, selfUpdateForce = false, "", false
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	}()
	rootCmd.SetArgs(append([]string{"self-update"}, args...))
	err := rootCmd.Execute()
	return out.String(), err
}

func fakeInstalledBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "codastre")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func installedContent(t *testing.T, exe string) string {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSelfUpdate_ReplacesOutdatedBinary(t *testing.T) {
	exe := fakeInstalledBinary(t)
	out, err := runSelfUpdateAgainst(t, "0.22.0", "0.23.0", exe)
	if err != nil {
		t.Fatal(err)
	}
	if got := installedContent(t, exe); got != "NEW" {
		t.Errorf("binary = %q, want NEW", got)
	}
	if !strings.Contains(out, "0.22.0 → 0.23.0") {
		t.Errorf("output: %s", out)
	}
}

func TestSelfUpdate_CheckChangesNothing(t *testing.T) {
	exe := fakeInstalledBinary(t)
	out, err := runSelfUpdateAgainst(t, "0.22.0", "0.23.0", exe, "--check")
	if err != nil {
		t.Fatal(err)
	}
	if got := installedContent(t, exe); got != "OLD" {
		t.Errorf("--check modified binary: %q", got)
	}
	if !strings.Contains(out, "Update available: 0.22.0 → 0.23.0") {
		t.Errorf("output: %s", out)
	}
}

func TestSelfUpdate_UpToDateIsNoop(t *testing.T) {
	exe := fakeInstalledBinary(t)
	out, err := runSelfUpdateAgainst(t, "0.23.0", "0.23.0", exe)
	if err != nil {
		t.Fatal(err)
	}
	if got := installedContent(t, exe); got != "OLD" {
		t.Errorf("binary replaced while up to date: %q", got)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("output: %s", out)
	}
}

func TestSelfUpdate_DevBuildNeedsForce(t *testing.T) {
	exe := fakeInstalledBinary(t)
	if _, err := runSelfUpdateAgainst(t, "0.23.0-dirty", "0.23.0", exe); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want --force error, got %v", err)
	}
	if got := installedContent(t, exe); got != "OLD" {
		t.Errorf("dev build replaced without --force: %q", got)
	}
	if _, err := runSelfUpdateAgainst(t, "0.23.0-dirty", "0.23.0", exe, "--force"); err != nil {
		t.Fatal(err)
	}
	if got := installedContent(t, exe); got != "NEW" {
		t.Errorf("--force did not replace: %q", got)
	}
}
