// Package study is the client half of the Plane 4 paired study (Tier D):
// the local study file the plugin hook reads, the REST client for the study
// endpoints, and the study view's rendering. See
// docs/plans/m3.5-plane4-study-contract.md.
package study

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileVersion guards the study file's shape; the hook ignores other versions.
const FileVersion = 1

// File is the local study file (§4): written by `codastre study start`,
// claimed by the UserPromptSubmit hook for the next fresh session, removed
// by `codastre study stop`. SessionID and ClaimedAt are the hook's to set.
type File struct {
	Version      int        `json:"version"`
	AssignmentID string     `json:"assignment_id"`
	Study        string     `json:"study"`
	Task         string     `json:"task"`
	Arm          string     `json:"arm"`
	Mode         string     `json:"mode"`
	ArmOrder     int        `json:"arm_order"`
	Prompt       string     `json:"prompt"`
	PromptSHA256 string     `json:"prompt_sha256"`
	WrittenAt    time.Time  `json:"written_at"`
	SessionID    *string    `json:"session_id"`
	ClaimedAt    *time.Time `json:"claimed_at"`
}

// FilePath is $CODASTRE_STUDY_FILE, or ~/.config/codastre/study.json.
func FilePath() string {
	if p := os.Getenv("CODASTRE_STUDY_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "study.json")
}

// ErrNoFile reports that no study run is active on this machine.
var ErrNoFile = errors.New("no study run is active")

// Load reads the study file; ErrNoFile when there is none.
func Load(path string) (*File, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoFile
		}
		return nil, err
	}
	var f File
	if err := json.Unmarshal(blob, &f); err != nil {
		return nil, fmt.Errorf("study file %s is unreadable: %w", path, err)
	}
	return &f, nil
}

// Save writes the study file atomically, 0600: it holds the prompt.
func (f *File) Save(path string) error {
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FromAssignment builds the unclaimed study file for a server assignment.
func FromAssignment(a Assignment, now time.Time) *File {
	return &File{
		Version:      FileVersion,
		AssignmentID: a.AssignmentID,
		Study:        a.Study,
		Task:         a.Task,
		Arm:          a.Arm,
		Mode:         a.Mode,
		ArmOrder:     a.ArmOrder,
		Prompt:       a.Prompt,
		PromptSHA256: a.PromptSHA256,
		WrittenAt:    now.UTC(),
	}
}
