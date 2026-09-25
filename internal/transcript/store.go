package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// StateVersion guards the on-disk format. A bump discards the old state and
// re-parses from scratch, which is always safe: the transcripts are the source
// of truth and the state file is only a cache.
const StateVersion = 2

// FileMark is the per-file watermark: identity plus how far the parse got.
// Identity is (inode, size) — a rotated or replaced file has a new inode, and
// a file that shrank was truncated. Either invalidates the offset.
type FileMark struct {
	Inode     uint64    `json:"inode"`
	Size      int64     `json:"size"`
	Offset    int64     `json:"offset"`
	SessionID string    `json:"session_id"`
	ParsedAt  time.Time `json:"parsed_at"`
}

// State is the whole local collection: one watermark per transcript file and
// one counter set per session. It holds no prompts, no code and no tool
// output — only counts, bytes and timestamps.
type State struct {
	Version  int                  `json:"version"`
	Files    map[string]*FileMark `json:"files"`
	Sessions map[string]*Session  `json:"sessions"`
	// HookLog is the watermark into the hook event log (session-events.jsonl).
	HookLog *FileMark `json:"hook_log,omitempty"`
	// Compactions is keyed by raw session id. It lives beside Sessions, not
	// on them, because a hook event can arrive before its transcript is
	// collected and must survive a session being rebuilt from zero.
	Compactions map[string]*Compactions `json:"compactions,omitempty"`
	// ReportSince is the consent boundary (D7): set by the first
	// upload-enabled run, and no session that started earlier is uploaded.
	ReportSince time.Time `json:"report_since,omitzero"`
}

// StatePath is where the collection lives: the same config root as the
// keychain fallback and the checkout registry. $CODASTRE_COLLECT_STATE
// overrides it, which is what the tests use.
func StatePath() string {
	if p := os.Getenv("CODASTRE_COLLECT_STATE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "collect-state.json")
}

// LoadState reads the state file. A missing, unreadable or stale-versioned
// file yields an empty state rather than an error — the cache is disposable.
func LoadState(path string) *State {
	empty := &State{
		Version:     StateVersion,
		Files:       map[string]*FileMark{},
		Sessions:    map[string]*Session{},
		Compactions: map[string]*Compactions{},
	}
	if path == "" {
		return empty
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var s State
	if err := json.Unmarshal(blob, &s); err != nil || s.Version != StateVersion {
		return empty
	}
	if s.Files == nil {
		s.Files = map[string]*FileMark{}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
	if s.Compactions == nil {
		s.Compactions = map[string]*Compactions{}
	}
	return &s
}

// Save writes the state atomically, 0600: it is derived from the developer's
// own transcripts and nobody else's business.
func (s *State) Save(path string) error {
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SessionsSince returns the collected sessions that ended at or after `since`,
// newest first. A zero `since` returns everything.
func (s *State) SessionsSince(since time.Time) []*Session {
	out := make([]*Session, 0, len(s.Sessions))
	for _, sess := range s.Sessions {
		if !since.IsZero() && sess.EndedAt.Before(since) {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndedAt.After(out[j].EndedAt) })
	return out
}
