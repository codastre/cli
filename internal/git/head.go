package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// HeadRef returns the current branch name and commit SHA of HEAD in repoPath.
// On a detached HEAD (no symbolic ref) the name falls back to the short SHA.
func HeadRef(repoPath string) (name, sha string, err error) {
	sha, err = revParse(repoPath, "HEAD")
	if err != nil {
		return "", "", err
	}
	// --quiet so a detached HEAD exits non-zero instead of printing to stderr.
	out, nameErr := exec.Command("git", "-C", repoPath, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if nameErr != nil {
		// Detached HEAD: use the short SHA as the ref name.
		if len(sha) >= 12 {
			return sha[:12], sha, nil
		}
		return sha, sha, nil
	}
	return strings.TrimSpace(string(out)), sha, nil
}

// RevParse resolves a ref (branch, tag, or SHA) to its full commit SHA.
func RevParse(repoPath, ref string) (string, error) {
	return revParse(repoPath, ref)
}

func revParse(repoPath, ref string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", ref).Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}
