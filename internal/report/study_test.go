package report

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codastre/cli/internal/transcript"
)

const (
	assignmentID = "0b7c2d4e-1111-4a2b-9c3d-abcdefabcdef"
	promptSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func tagged(state *transcript.State, id string) {
	state.Study[id] = &transcript.StudyTag{AssignmentID: assignmentID, Arm: "tool", PromptSHA256: promptSHA}
}

// A study session is reported under its assignment even when it started
// before the consent boundary; an untagged one beside it is not.
func TestReportAttachesStudyBlockOnlyToTaggedSessions(t *testing.T) {
	st := newStub(t)
	c := &Client{BaseURL: st.srv.URL, APIKey: "k"}
	now := t0()
	study := fixtureSession("study-"+secretSessionID, now.Add(-time.Hour), true)
	plain := fixtureSession("plain-"+secretSessionID, now.Add(time.Minute), true)
	old := fixtureSession("old-"+secretSessionID, now.Add(-2*time.Hour), true)
	state := stateWith(study, plain, old)
	state.ReportSince = now
	tagged(state, study.SessionID)

	res, err := Run(context.Background(), c, state, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.StudySessions != 1 || res.SessionsBeforeConsent != 1 || res.Sessions.Received != 2 {
		t.Fatalf("res = %+v", res)
	}
	var sessionsBody string
	for _, b := range st.bodies {
		if strings.Contains(b, `"sessions"`) {
			sessionsBody = b
		}
	}
	var payload struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(sessionsBody), &payload); err != nil {
		t.Fatal(err)
	}
	withStudy := 0
	for _, row := range payload.Sessions {
		if raw, ok := row["study"]; ok {
			withStudy++
			want := `{"assignment_id":"` + assignmentID + `","prompt_sha256":"` + promptSHA + `"}`
			if string(raw) != want {
				t.Errorf("study block = %s", raw)
			}
		}
	}
	if withStudy != 1 {
		t.Errorf("%d rows carry a study block, want 1 (untagged rows omit the key)", withStudy)
	}
	if strings.Contains(sessionsBody, `"arm"`) {
		t.Error("the client must never state its own arm")
	}
}

func TestStudyRowKeysAreTheAllowlistPlusStudy(t *testing.T) {
	s := fixtureSession(secretSessionID, t0(), true)
	row, _ := BuildSessionRow(s, "r", nil)
	row.Study = &StudyRef{AssignmentID: assignmentID, PromptSHA256: promptSHA}
	got := strings.Join(keysOf(t, row), ",")
	keys := append(append([]string{}, sessionKeys...), "study")
	sort.Strings(keys)
	want := strings.Join(keys, ",")
	if got != want {
		t.Errorf("keys = %s\nwant  %s", got, want)
	}
}

func TestBackfillSelectsOnlyPreBoundaryUntaggedAndNeverMovesConsent(t *testing.T) {
	st := newStub(t)
	c := &Client{BaseURL: st.srv.URL, APIKey: "k"}
	now := t0()
	old := fixtureSession("old", now.Add(-2*time.Hour), true)
	oldStudy := fixtureSession("old-study", now.Add(-time.Hour), true)
	fresh := fixtureSession("fresh", now.Add(time.Minute), true)
	state := stateWith(old, oldStudy, fresh)
	state.ReportSince = now
	tagged(state, oldStudy.SessionID)

	plan := Select(state, ScopeBackfill, now.Add(time.Hour))
	pv := plan.Preview()
	if len(pv.SessionRows) != 1 || len(pv.Sessions) != 1 || pv.SessionsAfterBoundary != 2 {
		t.Fatalf("preview = %+v", pv)
	}
	for _, r := range pv.SessionRows {
		if r.SessionRef != PlaceholderRef || r.Study != nil {
			t.Errorf("preview row = %+v", r)
		}
	}
	if len(st.hits) != 0 {
		t.Fatalf("selecting a plan hit the server: %v", st.hits)
	}

	res, err := Send(context.Background(), c, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions.Received != 1 || res.Episodes.Received != 2 {
		t.Errorf("sent = %+v", res)
	}
	if !state.ReportSince.Equal(now) {
		t.Errorf("backfill moved the consent boundary to %s", state.ReportSince)
	}
	if strings.Contains(strings.Join(st.bodies, "\n"), `"study"`) {
		t.Error("a backfilled row carried a study block")
	}
	if old.Upload.SessionSig == "" || fresh.Upload.SessionSig != "" {
		t.Errorf("marks: old=%+v fresh=%+v", old.Upload, fresh.Upload)
	}
	if again := Select(state, ScopeBackfill, now.Add(2*time.Hour)); !again.Empty() {
		t.Error("a re-run backfill would re-send what already landed")
	}
}

// With no boundary recorded, a backfill reaches only what started before now.
func TestBackfillWithoutBoundaryUsesNow(t *testing.T) {
	now := t0()
	state := stateWith(fixtureSession("a", now.Add(-time.Hour), true), fixtureSession("b", now.Add(time.Hour), true))
	plan := Select(state, ScopeBackfill, now)
	if len(plan.Preview().SessionRows) != 1 || !state.ReportSince.IsZero() {
		t.Errorf("plan = %+v, since = %s", plan.Result, state.ReportSince)
	}
}
