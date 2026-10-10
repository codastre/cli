package git

import (
	"fmt"
	"os/exec"
	"strconv"
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

// CountCommits returns how many commits are reachable from to but not from from
// (`git rev-list --count from..to`) — the commits an index built at from lacks.
// Errors when either commit is absent locally, e.g. a base sha this clone never
// fetched; callers treat that as "unknown", never as zero.
func CountCommits(repoPath, from, to string) (int, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-list", "--count", from+".."+to).Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count %s..%s: %w", from, to, err)
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// OnRemote reports whether sha is contained in any remote-tracking branch of
// remote (`refs/remotes/<remote>/*`) — i.e. whether the server, which fetches
// from that remote, can fetch it. Judged from the last fetch, so a commit pushed
// from another machine since then reads as absent; errs toward "not pushed".
func OnRemote(repoPath, remote, sha string) bool {
	out, err := exec.Command("git", "-C", repoPath, "for-each-ref", "--count=1",
		"--contains", sha, "--format=%(refname)", "refs/remotes/"+remote+"/").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// PushedBase returns the newest commit of sha's history that remote has, judged
// by its merge-base with the branch's upstream (`<sha>@{upstream}` is not valid
// syntax, so the current branch's `@{upstream}` is used). Empty when the branch
// has no upstream or the upstream does not share history with sha.
func PushedBase(repoPath, sha string) string {
	out, err := exec.Command("git", "-C", repoPath, "merge-base", sha, "@{upstream}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
