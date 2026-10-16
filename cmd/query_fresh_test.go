package cmd

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/mcpshim"
)

// commitRepo makes a throwaway repo with n empty commits and returns its root
// and every commit sha, oldest first.
func commitRepo(t *testing.T, n int) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	shas := make([]string, 0, n)
	for i := 0; i < n; i++ {
		runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q",
			"--allow-empty", "-m", fmt.Sprintf("c%d", i))
		out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, strings.TrimSpace(string(out)))
	}
	return dir, shas
}

func TestQueryRefFor(t *testing.T) {
	co := localCheckout{branch: "feat/x"}
	cases := []struct {
		name     string
		explicit string
		baseOnly bool
		ok       bool
		want     string
	}{
		{"explicit wins", "release", false, true, "release"},
		{"explicit wins over base-only", "release", true, true, "release"},
		{"own checkout sends the branch", "", false, true, "feat/x"},
		{"base-only sends none", "", true, true, ""},
		{"not this checkout sends none", "", false, false, ""},
	}
	for _, c := range cases {
		if got := queryRefFor(c.explicit, c.baseOnly, co, c.ok); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEagerSyncBudget(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":      defaultEagerSyncBudget,
		"500ms": 500 * time.Millisecond,
		"3":     3 * time.Second,
		"0":     0,
		"soon":  defaultEagerSyncBudget,
		"-1s":   defaultEagerSyncBudget,
	} {
		t.Setenv("CODASTRE_EAGER_SYNC_BUDGET", raw)
		if got := eagerSyncBudget(); got != want {
			t.Errorf("%q: got %s, want %s", raw, got, want)
		}
	}
}

func TestCheckoutFor_SkipsTargetsWithoutOneCheckout(t *testing.T) {
	for _, tgt := range []target{{federated: true}, {indexID: "i"}, {}} {
		if _, ok := checkoutFor(tgt); ok {
			t.Errorf("%+v: want no checkout", tgt)
		}
	}
}

func envelope(t *testing.T, repos map[string]searchedRefs) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"status": "ok", "freshness": "fresh", "repos": repos})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSearchedLine(t *testing.T) {
	root, shas := commitRepo(t, 4)
	head := shas[3]
	// HEAD is on origin (a remote-tracking ref is all OnRemote reads).
	runGit(t, root, "update-ref", "refs/remotes/origin/main", head)
	co := localCheckout{root: root, branch: "feat/x", head: head}
	cases := []struct {
		name string
		refs searchedRefs
		want string
	}{
		{"base behind HEAD", searchedRefs{BaseRef: "main", BaseSHA: shas[0]},
			"searched: base main @ " + shas[0][:7] + " · 3 commits behind HEAD"},
		{"one commit", searchedRefs{BaseRef: "main", BaseSHA: shas[2]},
			"searched: base main @ " + shas[2][:7] + " · 1 commit behind HEAD"},
		{"overlay at HEAD wins over base", searchedRefs{BaseRef: "main", BaseSHA: shas[0],
			OverlayRef: "feat/x", OverlaySHA: head},
			"searched: overlay feat/x @ " + head[:7] + " · at HEAD"},
		{"unknown commit", searchedRefs{BaseRef: "main", BaseSHA: strings.Repeat("e", 40)},
			"searched: base main @ eeeeeee · not in this checkout — HEAD relation unknown"},
	}
	for _, c := range cases {
		got := searchedLine(envelope(t, map[string]searchedRefs{"r1": c.refs}), co)
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestSearchedLine_IndexAheadOfHead(t *testing.T) {
	root, shas := commitRepo(t, 3)
	// The checkout sits on an older commit than the one indexed.
	co := localCheckout{root: root, branch: "main", head: shas[0]}
	got := searchedLine(envelope(t, map[string]searchedRefs{"r1": {BaseRef: "main", BaseSHA: shas[2]}}), co)
	if !strings.Contains(got, "ahead of HEAD") {
		t.Errorf("got %q, want an ahead-of-HEAD note", got)
	}
}

func TestSearchedLine_AbsentWhenServerSendsNoRefs(t *testing.T) {
	co := localCheckout{root: t.TempDir(), head: "abc"}
	// An older server: repos entries carry no base_sha.
	if got := searchedLine(envelope(t, map[string]searchedRefs{"r1": {}}), co); got != "" {
		t.Errorf("got %q, want empty (absent means unknown)", got)
	}
	if got := searchedLine(json.RawMessage(`{"status":"ok"}`), co); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRenderQueryText_PrintsSearchedUnderHeader(t *testing.T) {
	payload := []byte(`{"status":"ok","freshness":"fresh","searched_repos":["r1"],"results":[]}`)
	text, ok := mcpshim.RenderQueryText(payload, mcpshim.RenderOptions{
		NoSnippets: true,
		Searched:   "searched: base main @ 1aa1e6b · 3 commits behind HEAD",
	})
	if !ok {
		t.Fatal("render failed")
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "codastre · ") ||
		lines[1] != "searched: base main @ 1aa1e6b · 3 commits behind HEAD" {
		t.Errorf("unexpected rendering:\n%s", text)
	}
}

func TestIndexLagVerdict(t *testing.T) {
	cases := []struct {
		name   string
		lag    indexLag
		wantOK bool
		want   string
	}{
		{"synced overlay", indexLag{branch: "feat/x", head: "abcdef1234", baseRef: "main",
			baseSHA: "1111111", ahead: 40, synced: true, syncedAt: time.Now()}, true, "synced at HEAD"},
		{"unsynced, small lag", indexLag{branch: "feat/x", head: "h", baseRef: "main",
			baseSHA: "1111111", ahead: 3}, true, "3 commit(s) behind HEAD"},
		{"unsynced, far ahead", indexLag{branch: "feat/x", head: "h", baseRef: "main",
			baseSHA: "1111111", ahead: 11}, false, "codastre sync --once"},
		{"unsynced, unknown", indexLag{branch: "feat/x", head: "h", baseRef: "main",
			baseSHA: "1111111", ahead: -1}, false, "lag unknown"},
		{"base branch within ceiling", indexLag{branch: "main", head: "h", baseRef: "main",
			baseSHA: "1111111", ahead: 10}, true, "on main"},
		{"base branch past ceiling", indexLag{branch: "main", head: "h", baseRef: "main",
			baseSHA: "1111111", ahead: 11}, false, "base roll-forward"},
	}
	for _, c := range cases {
		f := c.lag.verdict()
		if f.ok != c.wantOK || !f.warning || !strings.Contains(f.detail, c.want) {
			t.Errorf("%s: got ok=%v warning=%v %q", c.name, f.ok, f.warning, f.detail)
		}
	}
}

func TestSearchedLine_FlagsAnUnpushedHead(t *testing.T) {
	root, shas := commitRepo(t, 3)
	runGit(t, root, "update-ref", "refs/remotes/origin/main", shas[1]) // HEAD (shas[2]) not pushed
	co := localCheckout{root: root, branch: "feat/x", head: shas[2]}
	got := searchedLine(envelope(t, map[string]searchedRefs{"r1": {BaseRef: "main", BaseSHA: shas[0]}}), co)
	if !strings.Contains(got, "2 commits behind HEAD (HEAD is not pushed") {
		t.Errorf("got %q, want the unpushed note", got)
	}
}

func TestPushedBaseAndOnRemote(t *testing.T) {
	root, shas := commitRepo(t, 3)
	runGit(t, root, "update-ref", "refs/remotes/origin/main", shas[1])
	// Upstream by config: there is no real remote to track.
	runGit(t, root, "config", "remote.origin.url", "https://example.invalid/repo.git")
	runGit(t, root, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	runGit(t, root, "config", "branch.main.remote", "origin")
	runGit(t, root, "config", "branch.main.merge", "refs/heads/main")
	if !git.OnRemote(root, "origin", shas[0]) || !git.OnRemote(root, "origin", shas[1]) {
		t.Error("ancestors of origin/main must read as on origin")
	}
	if git.OnRemote(root, "origin", shas[2]) {
		t.Error("an unpushed commit must not read as on origin")
	}
	if got := git.PushedBase(root, shas[2]); got != shas[1] {
		t.Errorf("PushedBase = %q, want the upstream tip %q", got, shas[1])
	}
}

func TestSyncIsRedundant_StatusWindows(t *testing.T) {
	now := time.Now()
	st := func(status string, age time.Duration) *syncState {
		return &syncState{ToRef: "b", ToSHA: "s", SyncedAt: now.Add(-age), Status: status}
	}
	cases := []struct {
		name string
		st   *syncState
		want bool
	}{
		{"done within TTL", st(syncDone, 10*time.Minute), true},
		{"legacy (no status) within TTL", st("", 10*time.Minute), true},
		{"done past TTL", st(syncDone, 16*time.Minute), false},
		{"pending suppresses parallel callers", st(syncPending, 30*time.Second), true},
		{"stale pending (crashed attempt) retries", st(syncPending, 2*time.Minute), false},
		{"failed backs off", st(syncFailed, time.Minute), true},
		{"failed retries after backoff", st(syncFailed, 3*time.Minute), false},
	}
	for _, c := range cases {
		if got := syncIsRedundant(c.st, "b", "s", now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSyncCoversHead(t *testing.T) {
	ok := &syncState{ToRef: "b", ToSHA: "h", Status: syncDone}
	if !syncCoversHead(ok, "b", "h") {
		t.Error("a done sync of HEAD covers it")
	}
	for name, st := range map[string]*syncState{
		"pending":     {ToRef: "b", ToSHA: "h", Status: syncPending},
		"failed":      {ToRef: "b", ToSHA: "h", Status: syncFailed},
		"pushed base": {ToRef: "b", ToSHA: "h", Status: syncDone, SentSHA: "p"},
		"other head":  {ToRef: "b", ToSHA: "x", Status: syncDone},
	} {
		if syncCoversHead(st, "b", "h") {
			t.Errorf("%s must not count as synced at HEAD", name)
		}
	}
}
