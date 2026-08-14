package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codastre/cli/internal/checkouts"
)

// initCloneAt creates a git clone at dir with the given origin, so scans have
// something realistic to find.
func initCloneAt(t *testing.T, dir, origin string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", origin)
	return dir
}

// scanFixture builds the layout developers actually have: several clones under
// one root, some nested one level deeper under an org directory, plus the traps
// a naive walk would fall into — a vendored clone inside node_modules, a
// non-repo directory, and a linked worktree sharing an origin with its main
// clone.
func scanFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	initCloneAt(t, filepath.Join(root, "api"), "git@github.com:acme/api.git")
	initCloneAt(t, filepath.Join(root, "web"), "https://github.com/acme/web.git")
	initCloneAt(t, filepath.Join(root, "org", "billing"), "git@github.com:acme/billing.git")
	initCloneAt(t, filepath.Join(root, "api", "node_modules", "dep"), "git@github.com:vendored/dep.git")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	api := filepath.Join(root, "api")
	runGit(t, api, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")
	runGit(t, api, "worktree", "add", filepath.Join(root, "api-wt"), "-b", "wt")
	return root
}

func foundURLs(t *testing.T, root string, depth int) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, r := range findRepos(root, depth) {
		if prev, dup := got[r.url]; dup {
			t.Fatalf("findRepos returned %s twice: %s and %s", r.url, prev, r.dir)
		}
		got[r.url] = r.dir
	}
	return got
}

func TestFindRepos_SkipsVendoredNonRepoAndWorktrees(t *testing.T) {
	root := scanFixture(t)
	got := foundURLs(t, root, 2)

	for _, want := range []string{"github.com/acme/api", "github.com/acme/web", "github.com/acme/billing"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s; got %v", want, got)
		}
	}
	// A clone under node_modules belongs to a dependency, not the developer.
	if dir, ok := got["github.com/vendored/dep"]; ok {
		t.Errorf("registered a vendored clone at %s", dir)
	}
	// The worktree shares acme/api's origin; registering it would shadow the
	// real clone, so it must not be the directory chosen.
	if dir := got["github.com/acme/api"]; dir != filepath.Join(root, "api") {
		t.Errorf("acme/api resolved to %s, want the main clone", dir)
	}
	if len(got) != 3 {
		t.Errorf("found %d repos, want 3: %v", len(got), got)
	}
}

func TestFindRepos_DepthLimits(t *testing.T) {
	root := scanFixture(t)

	shallow := foundURLs(t, root, 1)
	if _, ok := shallow["github.com/acme/billing"]; ok {
		t.Error("depth 1 should not reach org/billing")
	}
	if _, ok := shallow["github.com/acme/api"]; !ok {
		t.Error("depth 1 should still find a clone directly under root")
	}

	if deep := foundURLs(t, root, 2); len(deep) <= len(shallow) {
		t.Errorf("depth 2 found %d, depth 1 found %d; deeper should find more", len(deep), len(shallow))
	}
}

func TestFindRepos_RootIsItselfAClone(t *testing.T) {
	// Pointing scan at a single repo should register that repo, not walk into
	// its subdirectories looking for others.
	root := t.TempDir()
	initCloneAt(t, filepath.Join(root, "api"), "git@github.com:acme/api.git")
	initCloneAt(t, filepath.Join(root, "api", "node_modules", "dep"), "git@github.com:vendored/dep.git")

	got := foundURLs(t, filepath.Join(root, "api"), 2)
	if len(got) != 1 || got["github.com/acme/api"] == "" {
		t.Fatalf("got %v, want only github.com/acme/api", got)
	}
}

func TestRepoAt_RejectsWorktreeAndNonRepo(t *testing.T) {
	root := scanFixture(t)

	if _, _, err := repoAt(filepath.Join(root, "api-wt")); err == nil {
		t.Error("expected a linked worktree to be rejected")
	}
	_, _, err := repoAt(filepath.Join(root, "notes"))
	if err == nil {
		t.Fatal("expected a non-repo directory to be rejected")
	}
	// The add command keys its "did you mean scan?" hint off this sentinel.
	if !isNotARepo(err) {
		t.Errorf("error %v should match errNotARepo", err)
	}
	// A subdirectory of a clone is not itself a checkout: walking up would let a
	// scan attribute every plain directory to its enclosing repo.
	if _, _, err := repoAt(filepath.Join(root, "api", "node_modules")); err == nil {
		t.Error("expected a subdirectory of a clone to be rejected")
	}
}

func isNotARepo(err error) bool {
	return err == errNotARepo
}

func TestRepoAt_NormalizesOrigin(t *testing.T) {
	dir := initCloneAt(t, filepath.Join(t.TempDir(), "api"), "git@github.com:Acme/API.git")
	_, url, err := repoAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Host lowercased, .git stripped — the same key the registry and server use.
	if url != "github.com/Acme/API" {
		t.Fatalf("url = %q", url)
	}
}

func TestScanRoot_DefaultsToParentOfCurrentRepo(t *testing.T) {
	root := t.TempDir()
	clone := initCloneAt(t, filepath.Join(root, "api"), "git@github.com:acme/api.git")

	t.Chdir(clone)
	got, err := scanRoot(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Symlinked temp dirs (macOS /var -> /private/var) make a string compare
	// unreliable, so resolve both sides.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Fatalf("scanRoot = %s, want %s (the directory holding the clones)", got, root)
	}
}

func TestScanRoot_RejectsFileAndMissing(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanRoot([]string{f}); err == nil {
		t.Error("expected a file to be rejected as a scan root")
	}
	if _, err := scanRoot([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("expected a missing directory to be rejected")
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := expandHome("~/src"); got != filepath.Join(home, "src") {
		t.Errorf("expandHome(~/src) = %s", got)
	}
	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(~) = %s", got)
	}
	// Only a leading ~ is special; a literal path with a tilde inside is left be.
	if got := expandHome("/tmp/~x"); got != "/tmp/~x" {
		t.Errorf("expandHome(/tmp/~x) = %s", got)
	}
}

func TestRegistry_ForgetAndAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := checkouts.All(); len(got) != 0 {
		t.Fatalf("fresh registry not empty: %v", got)
	}
	if err := checkouts.Remember("github.com/acme/api", "/src/api"); err != nil {
		t.Fatal(err)
	}
	if got := checkouts.All(); got["github.com/acme/api"] != "/src/api" {
		t.Fatalf("All() = %v", got)
	}
	// A returned copy must not be a window into the live registry.
	checkouts.All()["github.com/acme/api"] = "/mutated"
	if got := checkouts.All(); got["github.com/acme/api"] != "/src/api" {
		t.Fatalf("All() returned an aliased map: %v", got)
	}

	removed, err := checkouts.Forget("github.com/acme/api")
	if err != nil || !removed {
		t.Fatalf("Forget = %v, %v; want true, nil", removed, err)
	}
	if removed, _ := checkouts.Forget("github.com/acme/api"); removed {
		t.Error("second Forget reported a removal")
	}
	if got := checkouts.All(); len(got) != 0 {
		t.Fatalf("registry not empty after Forget: %v", got)
	}
}
