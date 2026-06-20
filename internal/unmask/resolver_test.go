package unmask

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/codastre/cli/internal/masking"
)

// newGitRepo creates a temp git repo with the given tracked files and returns its
// root. Files are staged (git add) so `git ls-files` reports them; no commit is
// needed.
func newGitRepo(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for _, f := range files {
		abs := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	run(append([]string{"add"}, files...)...)
	return root
}

var testKeyRev1 = []byte("0123456789abcdef0123456789abcdef")
var testKeyRev2 = []byte("fedcba9876543210fedcba9876543210")

func TestUnmaskExactRev(t *testing.T) {
	root := newGitRepo(t, "src/auth/handler.go", "README.md")
	r := New(root, func(rev int, _ bool) ([]byte, bool, error) {
		return testKeyRev1, true, nil
	})
	if ok, err := r.LoadRev(1); err != nil || !ok {
		t.Fatalf("LoadRev: ok=%v err=%v", ok, err)
	}

	token := masking.MaskPath(testKeyRev1, "src/auth/handler.go")
	got, ok := r.Unmask(token, 1)
	if !ok || got != "src/auth/handler.go" {
		t.Fatalf("Unmask = (%q, %v), want (src/auth/handler.go, true)", got, ok)
	}
}

func TestUnmaskUnknownTokenReturnsFalse(t *testing.T) {
	root := newGitRepo(t, "main.go")
	r := New(root, func(rev int, _ bool) ([]byte, bool, error) {
		return testKeyRev1, true, nil
	})
	if _, err := r.LoadRev(1); err != nil {
		t.Fatal(err)
	}
	// A token for a file not in the working tree must not resolve.
	token := masking.MaskPath(testKeyRev1, "secret/not/checked/out.go")
	if got, ok := r.Unmask(token, 1); ok {
		t.Fatalf("expected miss, got %q", got)
	}
}

func TestUnmaskLazyFetchOnRotation(t *testing.T) {
	root := newGitRepo(t, "app.go")
	var fetched []int
	r := New(root, func(rev int, _ bool) ([]byte, bool, error) {
		fetched = append(fetched, rev)
		switch rev {
		case 1:
			return testKeyRev1, true, nil
		case 2:
			return testKeyRev2, true, nil
		default:
			return nil, false, nil
		}
	})
	// Warm only rev 1.
	if _, err := r.LoadRev(1); err != nil {
		t.Fatal(err)
	}

	// A result tagged rev 2 (post-rotation) must trigger a lazy fetch + build.
	token2 := masking.MaskPath(testKeyRev2, "app.go")
	got, ok := r.Unmask(token2, 2)
	if !ok || got != "app.go" {
		t.Fatalf("Unmask rev2 = (%q, %v), want (app.go, true)", got, ok)
	}
	if len(fetched) != 2 || fetched[1] != 2 {
		t.Fatalf("expected lazy fetch of rev 2, fetched=%v", fetched)
	}
}

func TestUnmaskRefreshesStaleKeyOnReenable(t *testing.T) {
	// Disable → re-enable masking reuses rev 1 with a NEW key. The old rev-1 key
	// is cached (keychain + in-memory map). A token masked with the new key misses
	// the stale map; a forced refresh (bypassing cache) must rebuild rev 1 with
	// the fresh key and resolve.
	root := newGitRepo(t, "app.go")
	freshKey := testKeyRev2
	var forced int
	r := New(root, func(_ int, force bool) ([]byte, bool, error) {
		if force {
			forced++
			return freshKey, true, nil // server now serves the re-enabled rev-1 key
		}
		return testKeyRev1, true, nil // stale cached key
	})
	// Warm rev 1 with the stale key.
	if _, err := r.LoadRev(1); err != nil {
		t.Fatal(err)
	}

	token := masking.MaskPath(freshKey, "app.go")
	got, ok := r.Unmask(token, 1)
	if !ok || got != "app.go" {
		t.Fatalf("Unmask after re-enable = (%q, %v), want (app.go, true)", got, ok)
	}
	if forced != 1 {
		t.Fatalf("expected exactly 1 forced refresh, got %d", forced)
	}
}

func TestUnmaskForcedRefreshIsRateLimited(t *testing.T) {
	// Tokens for files not in the local checkout legitimately never resolve; they
	// must not trigger a forced refetch on every miss. Two misses at the same rev
	// within the cooldown must force at most once.
	root := newGitRepo(t, "app.go")
	var forced int
	r := New(root, func(_ int, force bool) ([]byte, bool, error) {
		if force {
			forced++
		}
		return testKeyRev1, true, nil
	})
	if _, err := r.LoadRev(1); err != nil {
		t.Fatal(err)
	}
	r.Unmask(masking.MaskPath(testKeyRev2, "ghost1.go"), 1)
	r.Unmask(masking.MaskPath(testKeyRev2, "ghost2.go"), 1)
	if forced != 1 {
		t.Fatalf("expected 1 forced refresh within cooldown, got %d", forced)
	}
}

func TestUnmaskRevZeroScansAllLoaded(t *testing.T) {
	// GRAPH responses carry no mask_key_rev and pass rev 0; the resolver must
	// still find the token among loaded revs.
	root := newGitRepo(t, "pkg/svc.go")
	r := New(root, func(rev int, _ bool) ([]byte, bool, error) {
		return testKeyRev1, true, nil
	})
	if _, err := r.LoadRev(1); err != nil {
		t.Fatal(err)
	}
	token := masking.MaskPath(testKeyRev1, "pkg/svc.go")
	got, ok := r.Unmask(token, 0)
	if !ok || got != "pkg/svc.go" {
		t.Fatalf("Unmask rev0 = (%q, %v), want (pkg/svc.go, true)", got, ok)
	}
}

func TestLoadRevDisabledIsNoOp(t *testing.T) {
	root := newGitRepo(t, "x.go")
	r := New(root, func(rev int, _ bool) ([]byte, bool, error) {
		return nil, false, nil // server has no such rev (masking disabled)
	})
	ok, err := r.LoadRev(1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when fetcher reports no key")
	}
	if _, ok := r.Unmask("anything", 1); ok {
		t.Fatal("expected no resolution when no key loaded")
	}
}
