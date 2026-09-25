package transcript

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ProjectsDir is where Claude Code keeps transcripts.
// $CLAUDE_PROJECTS_DIR overrides it (the tests use that; so can a developer
// with a relocated home).
func ProjectsDir() string {
	if p := os.Getenv("CLAUDE_PROJECTS_DIR"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// TranscriptFile is one discovered transcript.
type TranscriptFile struct {
	Path     string
	Project  string // the project directory's name (a path slug)
	Size     int64
	Inode    uint64
	Modified time.Time
}

// Discover lists every transcript under root, newest first. A missing root is
// not an error — it means Claude Code has never run here.
func Discover(root string) ([]TranscriptFile, error) {
	if root == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	out := make([]TranscriptFile, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, TranscriptFile{
			Path:     path,
			Project:  filepath.Base(filepath.Dir(path)),
			Size:     info.Size(),
			Inode:    inodeOf(info),
			Modified: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// SessionIDFromPath reads the session id off the filename, which is how
// Claude Code names transcripts. The parse overrides it when the records
// carry one, so this only has to cover an empty or truncated file.
func SessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}
