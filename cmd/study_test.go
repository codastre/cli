package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codastre/cli/internal/study"
)

func execStudy(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Cleanup(func() { studyTask, studyJSON, studyKey = "", false, "" })
	return execRoot(t, args...)
}

func TestStudyStartWritesTheStudyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "study.json")
	t.Setenv("CODASTRE_STUDY_FILE", path)
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/usage/studies/auth-q4/assignments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"assignment_id":"0b7c2d4e-1111-4a2b-9c3d-abcdefabcdef","study":"auth-q4",
			"task":"refresh","design":"within","pair_ref":"p","arm":"no_tool","arm_order":1,"mode":"grep",
			"prompt":"Explain token refresh.","prompt_sha256":"`+strings.Repeat("a", 64)+`",
			"assigned_at":"2026-09-25T09:00:00Z","reused":false}`)
	}))
	defer srv.Close()

	out, err := execStudy(t, "study", "start", "auth-q4", "--task", "refresh", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if gotBody["task"] != "refresh" {
		t.Errorf("request body = %v", gotBody)
	}
	if !strings.Contains(out, "arm no_tool") || !strings.Contains(out, "NEW Claude Code session") {
		t.Errorf("output:\n%s", out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("study file mode = %o", info.Mode().Perm())
	}
	f, err := study.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || f.Mode != "grep" || f.Prompt != "Explain token refresh." || f.SessionID != nil {
		t.Errorf("study file = %+v", f)
	}

	// A second start while the run is active is refused, and stop clears it.
	if _, err := execStudy(t, "study", "start", "auth-q4", "--server", srv.URL, "--key", "k"); err == nil ||
		!strings.Contains(err.Error(), "study stop") {
		t.Errorf("second start err = %v", err)
	}
	if out, err := execStudy(t, "study", "status"); err != nil || !strings.Contains(out, "unclaimed") {
		t.Errorf("status err=%v out=%s", err, out)
	}
	if _, err := execStudy(t, "study", "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stop left the study file behind")
	}
}

func TestStudyShowRendersPairsAndWithholdsHeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"study":{"slug":"auth-q4","design":"within","tool_arm_mode":"auto",
			"status":"open","created_at":"2026-09-20T00:00:00Z"},"source":"transcript",
			"pairs":[{"pair_ref":"11111111-2222","task":"refresh","task_shape":"conceptual","order":"tool_first",
			  "complete":true,"judged":false,
			  "arms":{"tool":{"assignment_id":"a","cost_usd":0.42,"duration_ms":90000},"no_tool":null},
			  "correct":{"tool":true,"no_tool":null}}],
			"n":0,"target_n":12,"publishable":false,"order_balance":{"tool_first":1,"no_tool_first":0},
			"shape_mix":{"conceptual":1},"headline":null,
			"headline_note":"withheld: 0 of 12 pre-registered pairs complete and judged",
			"receipt":{"formula":"pairs counted per arm","n":0,"provenance":"Plane 4","caveat":"CAVEAT-TEXT"}}`)
	}))
	defer srv.Close()

	out, err := execStudy(t, "study", "show", "auth-q4", "--server", srv.URL, "--key", "k")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"refresh", "tool_first", "$0.420", "not run", "n = 0 of 12",
		"withheld: 0 of 12", "CAVEAT-TEXT", "tool_first 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	lower := strings.ToLower(out)
	for _, banned := range []string{"mean", "average", "avg"} {
		if strings.Contains(lower, banned) {
			t.Errorf("study view renders %q — a study is pairs, not a metric:\n%s", banned, out)
		}
	}
}
