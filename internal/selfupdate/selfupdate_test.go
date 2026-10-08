package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		current, release string
		want             Status
	}{
		{"0.22.0", "0.23.0", Outdated},
		{"0.23.0", "0.23.0", UpToDate},
		{"0.24.0", "0.23.0", UpToDate},
		{"0.9.9", "0.10.0", Outdated},
		{"1.0.0-rc.1", "1.0.0", Outdated},
		{"v0.23.0", "0.23.0", UpToDate},
		{"0.23.0-dirty", "0.23.0", Unknown},
		{"0.23.0-titles-dirty", "0.24.0", Unknown},
		{"dev", "0.23.0", Unknown},
		{"a1b2c3d4e5f6", "0.23.0", Unknown},
	}
	for _, c := range cases {
		if got := Compare(c.current, c.release); got != c.want {
			t.Errorf("Compare(%q, %q) = %v, want %v", c.current, c.release, got, c.want)
		}
	}
}

func TestArchiveName(t *testing.T) {
	if got := ArchiveName("0.23.0", "darwin", "arm64"); got != "codastre_0.23.0_darwin_arm64.tar.gz" {
		t.Errorf("darwin: %s", got)
	}
	if got := ArchiveName("0.23.0", "windows", "amd64"); got != "codastre_0.23.0_windows_amd64.zip" {
		t.Errorf("windows: %s", got)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("abc123  codastre_1.0.0_linux_amd64.tar.gz\nDEF456 *codastre_1.0.0_darwin_arm64.tar.gz\n")
	if got, err := checksumFor(sums, "codastre_1.0.0_darwin_arm64.tar.gz"); err != nil || got != "def456" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := checksumFor(sums, "missing.tar.gz"); err == nil {
		t.Error("want error for missing entry")
	}
}

func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	tgz := makeTarGz(t, map[string][]byte{"LICENSE": []byte("l"), "codastre": []byte("BIN")})
	if got, err := ExtractBinary("x.tar.gz", tgz); err != nil || string(got) != "BIN" {
		t.Errorf("tar.gz: %q, %v", got, err)
	}
	if _, err := ExtractBinary("x.tar.gz", makeTarGz(t, map[string][]byte{"README.md": nil})); err == nil {
		t.Error("want error when binary absent")
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("codastre.exe")
	_, _ = w.Write([]byte("EXE"))
	_ = zw.Close()
	if got, err := ExtractBinary("x.zip", zbuf.Bytes()); err != nil || string(got) != "EXE" {
		t.Errorf("zip: %q, %v", got, err)
	}
}

func TestReplace(t *testing.T) {
	target := filepath.Join(t.TempDir(), "codastre")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Errorf("content = %q", got)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("not executable: %v", info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

// fakeReleaseServer serves one release whose archive digest is listed in
// checksums.txt — or deliberately wrong when corrupt is set.
func fakeReleaseServer(t *testing.T, version string, archive []byte, corrupt bool) (*httptest.Server, *Client) {
	t.Helper()
	name := ArchiveName(version, "linux", "amd64")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	if corrupt {
		digest = strings.Repeat("0", 64)
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/codastre/cli/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Release{Tag: "v" + version, Assets: []Asset{
			{Name: name, URL: srv.URL + "/dl/" + name},
			{Name: "checksums.txt", URL: srv.URL + "/dl/checksums.txt"},
		}})
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", digest, name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &Client{APIBase: srv.URL, Repo: DefaultRepo, HTTP: srv.Client()}
}

func TestDownloadArchiveVerifiesChecksum(t *testing.T) {
	archive := makeTarGz(t, map[string][]byte{"codastre": []byte("BIN")})
	ctx := context.Background()

	_, c := fakeReleaseServer(t, "1.2.3", archive, false)
	rel, err := c.Latest(ctx)
	if err != nil || rel.Version() != "1.2.3" {
		t.Fatalf("latest: %+v, %v", rel, err)
	}
	if _, data, err := c.DownloadArchive(ctx, rel, "linux", "amd64"); err != nil || !bytes.Equal(data, archive) {
		t.Fatalf("download: %v", err)
	}
	if _, _, err := c.DownloadArchive(ctx, rel, "plan9", "amd64"); err == nil {
		t.Error("want error for missing platform asset")
	}

	_, bad := fakeReleaseServer(t, "1.2.3", archive, true)
	rel, _ = bad.Latest(ctx)
	if _, _, err := bad.DownloadArchive(ctx, rel, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("want checksum mismatch, got %v", err)
	}
}

func TestByVersionNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &Client{APIBase: srv.URL, Repo: DefaultRepo, HTTP: srv.Client()}
	if _, err := c.ByVersion(context.Background(), "9.9.9"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("got %v", err)
	}
}
