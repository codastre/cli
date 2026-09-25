package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	studyAssignment = "0b7c2d4e-1111-4a2b-9c3d-abcdefabcdef"
	studySHA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestCollectStudyLogFoldsClaimsAndDropsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "study-sessions.jsonl")
	lines := []string{
		`{"ts":"2026-09-25T09:00:00Z","session_id":"s1","event":"study_claim","assignment_id":"` + studyAssignment + `","arm":"no_tool","prompt_sha256":"` + studySHA + `"}`,
		`not json at all`,
		`{"session_id":"s2","event":"study_claim","assignment_id":"not-a-uuid","arm":"tool","prompt_sha256":"` + studySHA + `"}`,
		`{"session_id":"s3","event":"study_claim","assignment_id":"` + studyAssignment + `","arm":"sideways","prompt_sha256":"` + studySHA + `"}`,
		`{"session_id":"s4","event":"study_claim","assignment_id":"` + studyAssignment + `","arm":"tool","prompt_sha256":"short"}`,
		`{"session_id":"s5","event":"compact","assignment_id":"` + studyAssignment + `"}`,
	}
	// A truncated final line (no newline) is not committed.
	body := strings.Join(lines, "\n") + "\n" + `{"session_id":"s6","event":"study_cl`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	state := LoadState("")
	res, err := CollectStudyLog(path, state)
	if err != nil {
		t.Fatal(err)
	}
	if res.Claims != 1 || len(state.Study) != 1 {
		t.Fatalf("claims = %d, study = %+v", res.Claims, state.Study)
	}
	tag := state.Study["s1"]
	if tag == nil || tag.AssignmentID != studyAssignment || tag.Arm != "no_tool" || tag.PromptSHA256 != studySHA {
		t.Errorf("tag = %+v", tag)
	}

	again, err := CollectStudyLog(path, state)
	if err != nil {
		t.Fatal(err)
	}
	if again.BytesRead != 0 || again.Claims != 0 {
		t.Errorf("re-run read %d bytes / %d claims; the watermark should make it a no-op", again.BytesRead, again.Claims)
	}
}

func TestCollectStudyLogMissingIsNotAnError(t *testing.T) {
	state := LoadState("")
	if _, err := CollectStudyLog(filepath.Join(t.TempDir(), "absent.jsonl"), state); err != nil {
		t.Fatal(err)
	}
}
