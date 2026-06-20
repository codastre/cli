package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRepoWithCommit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "init")
	return root
}

func TestHeadRefOnBranch(t *testing.T) {
	root := gitRepoWithCommit(t)
	name, sha, err := HeadRef(root)
	if err != nil {
		t.Fatal(err)
	}
	if name != "feature" {
		t.Errorf("branch name = %q, want feature", name)
	}
	if len(sha) != 40 {
		t.Errorf("sha = %q, want 40-char SHA", sha)
	}
}

func TestHeadRefDetached(t *testing.T) {
	root := gitRepoWithCommit(t)
	sha, err := RevParse(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// Detach HEAD at the commit.
	cmd := exec.Command("git", "-C", root, "checkout", "-q", sha)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout detached: %v\n%s", err, out)
	}
	name, gotSHA, err := HeadRef(root)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != sha {
		t.Errorf("sha = %q, want %q", gotSHA, sha)
	}
	// Detached: name falls back to the short SHA.
	if name != sha[:12] {
		t.Errorf("detached name = %q, want short sha %q", name, sha[:12])
	}
}
