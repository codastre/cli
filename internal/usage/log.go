// Package usage reads the local search-usage log written by the Claude Code
// plugin hook (adapters/claude/hooks/track.js) and aggregates it into the
// numbers `codastre savings` prints.
//
// This is source 4 of the measurement design (docs/plans/per-developer-value-metrics.md):
// an estimate, ±20%, and the only client-side source that covers clients other
// than Claude Code. It stays on the machine — nothing here uploads, and nothing
// here needs an opt-in beyond the one that wrote the log.
//
// What it deliberately does not compute: a counterfactual. There is no
// `saved`, no `baseline`, no "tokens you would have spent". Every figure below
// is a count or a sum of something that actually happened.
package usage

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Record is one line of claude-token-log.jsonl. Field names match track.js
// exactly; unknown fields are ignored so a newer hook never breaks an older
// binary.
type Record struct {
	TS        string `json:"ts"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Tool      string `json:"tool"`
	Class     string `json:"class"`
	Plane     string `json:"plane"`
	OutTokens int    `json:"out_tokens"`
	TokBasis  string `json:"tok_basis"`
	Rung      string `json:"rung"`
	// Detail holds a query string or a file path. It is read so a malformed
	// line can be told apart from an empty one, and it is never aggregated,
	// never printed, and never leaves this process.
	Detail string `json:"detail"`
}

// DefaultLogPath mirrors lib.js tokenLogPath(): $CODASTRE_TOKEN_LOG, else
// ~/.config/codastre/claude-token-log.jsonl. Returns "" when neither resolves.
func DefaultLogPath() string {
	if p := os.Getenv("CODASTRE_TOKEN_LOG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "claude-token-log.jsonl")
}

// TrackingEnabled reports whether the hook is set to log at all
// (CODASTRE_TRACK_TOKENS=1, per lib.js trackingEnabled()). A live A/B mode also
// makes the hook log, so a false here does not mean the log is empty — only
// that continuous tracking is off.
func TrackingEnabled() bool { return os.Getenv("CODASTRE_TRACK_TOKENS") == "1" }

// UploadEnabled reports whether the developer opted into sending counters to
// the server. Nothing in M0 uploads; `codastre doctor` reports the flag so the
// answer to "is anything leaving my machine" is one line, not an inference.
func UploadEnabled() bool { return os.Getenv("CODASTRE_USAGE_REPORT") == "1" }

// Load reads every parseable record at or after `since` from path, plus the
// rotated `.1` sibling the hook leaves behind. A zero `since` reads everything.
//
// Malformed lines are skipped rather than fatal: the log is appended to from a
// hook that can be killed mid-write, so a truncated final line is expected and
// must not cost the developer their whole history.
func Load(path string, since time.Time) ([]Record, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	var out []Record
	// Oldest first: the rotated file holds the earlier window.
	for _, p := range []string{path + ".1", path} {
		recs, err := loadFile(p, since)
		if err != nil {
			if os.IsNotExist(err) && p != path {
				continue // no rotation yet — normal
			}
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func loadFile(path string, since time.Time) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	// Records carry a truncated 200-char detail, but a pathological line should
	// not abort the scan; give the scanner room and drop anything past it.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if !since.IsZero() && !after(r.TS, since) {
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		// A too-long line is the one scanner error worth surviving: everything
		// read so far is still a valid, if short, window.
		return out, nil
	}
	return out, nil
}

// after reports whether an RFC3339 timestamp is at or after `since`. An
// unparseable or missing timestamp is kept: dropping it would silently shrink
// the denominator of every count.
func after(ts string, since time.Time) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return true
	}
	return !t.Before(since)
}
