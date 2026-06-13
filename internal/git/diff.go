package git

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const maxDiffTuples = 5000
const maxDiffBytes = 4 * 1024 * 1024 // 4 MB

// DiffEntry is one line of `git diff --raw` output.
type DiffEntry struct {
	Status  string // A, M, D, R
	Path    string // new path (or only path for non-renames)
	OldPath string // set for R entries
	BlobSHA string // new blob SHA; old SHA for deletions
}

// DiffResult is the outcome of ComputeDiff.
type DiffResult struct {
	Entries      []DiffEntry
	DiffOmitted  bool // true when cap exceeded — server must diff its clone
	ManifestMode bool // true for shallow-clone fallback
}

// ComputeDiff returns the diff between fromSHA and toSHA in repoPath.
// Falls back to manifest mode when the clone is shallow and merge-base fails (impl-spec §2.6).
func ComputeDiff(repoPath, fromSHA, toSHA string) (*DiffResult, error) {
	mergeBase, err := mergeBaseOf(repoPath, fromSHA, toSHA)
	if err != nil {
		if isShallowClone(repoPath) {
			return computeManifest(repoPath, toSHA)
		}
		return nil, fmt.Errorf("merge-base: %w", err)
	}
	return computeRawDiff(repoPath, mergeBase, toSHA)
}

func mergeBaseOf(repoPath, a, b string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "merge-base", a, b).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isShallowClone(repoPath string) bool {
	_, err := os.Stat(repoPath + "/.git/shallow")
	return err == nil
}

func computeRawDiff(repoPath, base, to string) (*DiffResult, error) {
	out, err := exec.Command(
		"git", "-C", repoPath,
		"diff", "--raw", "--find-renames", base, to,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	if len(out) > maxDiffBytes {
		return &DiffResult{DiffOmitted: true}, nil
	}

	entries, err := parseRawDiff(out)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxDiffTuples {
		return &DiffResult{DiffOmitted: true}, nil
	}

	return &DiffResult{Entries: entries}, nil
}

func parseRawDiff(data []byte) ([]DiffEntry, error) {
	var entries []DiffEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, ":") {
			continue
		}
		e, err := parseRawLine(line)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// parseRawLine parses one line of `git diff --raw --find-renames`.
// Format: ":old_mode new_mode old_sha new_sha status\tpath[\told_path]"
func parseRawLine(line string) (DiffEntry, error) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) < 2 {
		return DiffEntry{}, fmt.Errorf("unexpected diff line: %q", line)
	}

	header := strings.TrimPrefix(parts[0], ":")
	fields := strings.Fields(header)
	if len(fields) < 5 {
		return DiffEntry{}, fmt.Errorf("unexpected diff header: %q", header)
	}

	status := fields[4]
	newBlobSHA := fields[3]
	if status == "D" {
		newBlobSHA = fields[2]
	}

	// R90, R100, etc. → "R"
	normalized := status
	if strings.HasPrefix(status, "R") {
		normalized = "R"
	}

	var oldPath, path string
	if normalized == "R" && len(parts) >= 3 {
		oldPath = parts[1]
		path = parts[2]
	} else {
		path = parts[1]
	}

	return DiffEntry{
		Status:  normalized,
		Path:    path,
		OldPath: oldPath,
		BlobSHA: newBlobSHA,
	}, nil
}

func computeManifest(repoPath, sha string) (*DiffResult, error) {
	out, err := exec.Command("git", "-C", repoPath, "ls-tree", "-r", sha).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}

	var entries []DiffEntry
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		tabIdx := strings.Index(line, "\t")
		if tabIdx < 0 {
			continue
		}
		fields := strings.Fields(line[:tabIdx])
		if len(fields) < 3 {
			continue
		}
		entries = append(entries, DiffEntry{
			Status:  "A",
			Path:    line[tabIdx+1:],
			BlobSHA: fields[2],
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &DiffResult{Entries: entries, ManifestMode: true}, nil
}
