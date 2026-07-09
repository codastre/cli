package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/codastre/cli/internal/mcpclient"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepoWithRemotes creates a throwaway git repo whose remotes mirror the
// reported bug: `origin` points at an unregistered fork while `upstream`/`source`
// point at the registered repo (the latter two normalize to the same canonical URL).
func initRepoWithRemotes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:acme/codastre.git")
	runGit(t, dir, "remote", "add", "upstream", "https://github.com/jflbr/codastre.git")
	runGit(t, dir, "remote", "add", "source", "https://github.com/jflbr/codastre.git")
	return dir
}

func TestRemoteURLs_Order(t *testing.T) {
	dir := initRepoWithRemotes(t)
	urls, err := remoteURLs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// origin (rank 0), then upstream (rank 1), then the rest (source).
	want := []string{
		"git@github.com:acme/codastre.git",
		"https://github.com/jflbr/codastre.git",
		"https://github.com/jflbr/codastre.git",
	}
	if len(urls) != len(want) {
		t.Fatalf("remoteURLs = %v, want %v", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("remoteURLs[%d] = %q, want %q (full: %v)", i, urls[i], want[i], urls)
		}
	}
}

func TestRemoteURLs_NoRemotes(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	if _, err := remoteURLs(dir); err == nil {
		t.Fatal("expected error for repo with no remotes")
	}
}

func TestAutoTargets_NormalizeDedupOrder(t *testing.T) {
	dir := initRepoWithRemotes(t)
	t.Chdir(dir)

	got := autoTargets()
	// Both jflbr remotes collapse to one canonical URL; origin stays first.
	want := []string{"github.com/acme/codastre", "github.com/jflbr/codastre"}
	if len(got) != len(want) {
		t.Fatalf("autoTargets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("autoTargets[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestAutoTargets_OutsideRepo(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := autoTargets(); got != nil {
		t.Fatalf("autoTargets outside a git repo = %v, want nil", got)
	}
}

// An explicit --repo-url must never carry fallbacks: it targets exactly one repo.
func TestResolveTarget_RepoURLHasNoFallbacks(t *testing.T) {
	tgt, err := resolveTarget("", "git@github.com:acme/api.git", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tgt.fallbacks) != 0 {
		t.Fatalf("explicit --repo-url got fallbacks %v, want none", tgt.fallbacks)
	}
}

// The auto (no-flag) path inside the bug's repo targets origin first and keeps the
// other remote as a fallback, so an unregistered origin still resolves via upstream.
func TestResolveTarget_AutoCarriesRemoteFallbacks(t *testing.T) {
	dir := initRepoWithRemotes(t)
	t.Chdir(dir)

	tgt, err := resolveTarget("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if tgt.repoURL != "github.com/acme/codastre" {
		t.Fatalf("primary repoURL = %q, want origin", tgt.repoURL)
	}
	if len(tgt.fallbacks) != 1 || tgt.fallbacks[0] != "github.com/jflbr/codastre" {
		t.Fatalf("fallbacks = %v, want [github.com/jflbr/codastre]", tgt.fallbacks)
	}
}

// repoURLOf extracts the repo_url argument from a tools/call request body.
func repoURLOf(t *testing.T, r *http.Request) string {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Params struct {
			Arguments struct {
				RepoURL string `json:"repo_url"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req.Params.Arguments.RepoURL
}

func okEnvelope(t *testing.T) []byte {
	t.Helper()
	text, _ := json.Marshal(map[string]any{"status": "ok", "results": []any{}})
	env := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": string(text)}},
		},
	}
	b, _ := json.Marshal(env)
	return b
}

func notIndexedEnvelope(t *testing.T) []byte {
	t.Helper()
	text, _ := json.Marshal(map[string]any{"error": "REPO_NOT_INDEXED"})
	env := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": string(text)}},
		},
	}
	b, _ := json.Marshal(env)
	return b
}

// The bug's core: origin is unregistered, so the first call returns
// REPO_NOT_INDEXED; callWithRepoFallback must retry the fallback remote and
// report the target that actually resolved.
func TestCallWithRepoFallback_RetriesUntilIndexed(t *testing.T) {
	var tried []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoURL := repoURLOf(t, r)
		tried = append(tried, repoURL)
		w.WriteHeader(http.StatusOK)
		if repoURL == "github.com/jflbr/codastre" {
			_, _ = w.Write(okEnvelope(t))
			return
		}
		_, _ = w.Write(notIndexedEnvelope(t))
	}))
	defer srv.Close()

	tgt := target{
		repoURL:   "github.com/acme/codastre",
		fallbacks: []string{"github.com/jflbr/codastre"},
	}
	cfg := mcpclient.Config{ServerURL: srv.URL, APIKey: "k"}
	_, resolved, err := callWithRepoFallback(context.Background(), cfg, "QUERY", map[string]any{}, tgt)
	if err != nil {
		t.Fatalf("callWithRepoFallback: %v", err)
	}
	if resolved.repoURL != "github.com/jflbr/codastre" {
		t.Fatalf("resolved repoURL = %q, want the registered fallback", resolved.repoURL)
	}
	want := []string{"github.com/acme/codastre", "github.com/jflbr/codastre"}
	if len(tried) != len(want) || tried[0] != want[0] || tried[1] != want[1] {
		t.Fatalf("server saw repo_urls %v, want %v", tried, want)
	}
}

// When every remote is unregistered, the final REPO_NOT_INDEXED surfaces.
func TestCallWithRepoFallback_AllUnindexed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(notIndexedEnvelope(t))
	}))
	defer srv.Close()

	tgt := target{repoURL: "github.com/a/b", fallbacks: []string{"github.com/c/d"}}
	cfg := mcpclient.Config{ServerURL: srv.URL, APIKey: "k"}
	_, _, err := callWithRepoFallback(context.Background(), cfg, "QUERY", map[string]any{}, tgt)
	var te *mcpclient.ToolError
	if !errors.As(err, &te) || te.Code != "REPO_NOT_INDEXED" {
		t.Fatalf("err = %v, want REPO_NOT_INDEXED ToolError", err)
	}
}

// An explicit single target (no fallbacks) must not retry on REPO_NOT_INDEXED.
func TestCallWithRepoFallback_NoFallbacksNoRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(notIndexedEnvelope(t))
	}))
	defer srv.Close()

	tgt := target{repoURL: "github.com/a/b"}
	cfg := mcpclient.Config{ServerURL: srv.URL, APIKey: "k"}
	if _, _, err := callWithRepoFallback(context.Background(), cfg, "QUERY", map[string]any{}, tgt); err == nil {
		t.Fatal("expected REPO_NOT_INDEXED error")
	}
	if calls != 1 {
		t.Fatalf("server hit %d times, want exactly 1 (no retry without fallbacks)", calls)
	}
}
