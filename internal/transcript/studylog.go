package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// The study log is written by the plugin's UserPromptSubmit hook when it
// claims a fresh session for a Plane 4 study assignment. One line per claim,
// ids only — no prompt, no path. See
// docs/plans/m3.5-plane4-study-contract.md §4.

// StudyTag ties a raw session id to the assignment it ran under. The arm is
// kept for local display; the server takes the arm from the assignment and
// never from the client.
type StudyTag struct {
	AssignmentID string `json:"assignment_id"`
	Arm          string `json:"arm"`
	PromptSHA256 string `json:"prompt_sha256"`
}

// StudyResult reports what one pass over the study log did.
type StudyResult struct {
	BytesRead int64 `json:"bytes_read"`
	Claims    int   `json:"claims"`
}

var (
	uuidRE   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// StudyLogPath is where the hook appends: $CODASTRE_STUDY_LOG, or
// ~/.config/codastre/study-sessions.jsonl.
func StudyLogPath() string {
	if p := os.Getenv("CODASTRE_STUDY_LOG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "study-sessions.jsonl")
}

type studyEvent struct {
	SessionID    string `json:"session_id"`
	Event        string `json:"event"`
	AssignmentID string `json:"assignment_id"`
	Arm          string `json:"arm"`
	PromptSHA256 string `json:"prompt_sha256"`
}

// CollectStudyLog folds new study-log lines into state.Study, resuming from
// state.StudyLog. A missing log is not an error: most machines never join a
// study.
func CollectStudyLog(path string, state *State) (StudyResult, error) {
	if state.Study == nil {
		state.Study = map[string]*StudyTag{}
	}
	n, c, err := collectLog(path, &state.StudyLog, func(line []byte) bool {
		return applyStudyLine(line, state)
	})
	return StudyResult{BytesRead: n, Claims: c}, err
}

// applyStudyLine records one claim. A malformed line, or one whose ids are
// not the shapes the server accepts, is dropped rather than uploaded as a
// tag the server would refuse — and a refused tag fails the whole batch.
func applyStudyLine(line []byte, state *State) bool {
	var ev studyEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return false
	}
	if ev.Event != "study_claim" || ev.SessionID == "" ||
		!uuidRE.MatchString(ev.AssignmentID) || !sha256RE.MatchString(ev.PromptSHA256) {
		return false
	}
	if ev.Arm != "tool" && ev.Arm != "no_tool" {
		return false
	}
	state.Study[ev.SessionID] = &StudyTag{
		AssignmentID: ev.AssignmentID,
		Arm:          ev.Arm,
		PromptSHA256: ev.PromptSHA256,
	}
	return true
}
