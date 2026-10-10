package cmd

import (
	"fmt"
	"time"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/unmask"
)

// doctorLagWarnCommits is how far HEAD may run ahead of the indexed base, with
// no overlay synced, before doctor warns. It mirrors the server's roll-forward
// commit ceiling (search-effectiveness B, CODASTRE_ROLLFORWARD_MAX_COMMITS = 20):
// past it, a base-only answer is missing more history than the base itself is
// allowed to trail its own branch.
const doctorLagWarnCommits = 20

// indexLag is what the "index lag" check measured, kept apart from the network
// lookups so the verdict is testable against a real git repo alone.
type indexLag struct {
	branch, head     string
	baseRef, baseSHA string
	ahead            int  // commits on HEAD the base lacks; -1 = unknown
	synced           bool // the recorded sync covers this exact branch + HEAD
	syncedAt         time.Time
}

// verdict turns a measured lag into a doctor finding (search-effectiveness C).
func (l indexLag) verdict() finding {
	f := finding{label: "index lag", ok: true, warning: true}
	base := fmt.Sprintf("base %s @ %s", l.baseRef, shortSHA(l.baseSHA))
	switch {
	case l.branch == l.baseRef:
		// The base branch never gets an overlay; keeping the base current is the
		// server's roll-forward. Report it, warn only past the server's ceiling.
		f.detail = fmt.Sprintf("on %s; %s is %s", l.branch, base, aheadPhrase(l.ahead))
		if l.ahead > doctorLagWarnCommits {
			f.ok = false
			f.detail += " — waiting on the server's base roll-forward"
		}
	case l.synced:
		f.detail = fmt.Sprintf("overlay for %s synced at HEAD %s (%s ago)",
			l.branch, shortSHA(l.head), time.Since(l.syncedAt).Round(time.Minute))
	default:
		f.detail = fmt.Sprintf("no overlay synced for %s at HEAD; %s is %s",
			l.branch, base, aheadPhrase(l.ahead))
		if l.ahead < 0 || l.ahead > doctorLagWarnCommits {
			f.ok = false
			f.detail += " — run `codastre sync --once` (queries here sync eagerly too)"
		}
	}
	return f
}

func aheadPhrase(n int) string {
	switch {
	case n < 0:
		return "not in this checkout (lag unknown)"
	case n == 0:
		return "at or ahead of HEAD"
	default:
		return fmt.Sprintf("%d commit(s) behind HEAD", n)
	}
}

// checkIndexLag measures how far the checkout's HEAD is past the indexed base
// and whether an overlay covers it. Every lookup failure is a skip, not an
// error: doctor reports state, and an unregistered repo is a legitimate one.
func checkIndexLag(serverURL, apiKey, repoRoot string) finding {
	skip := func(why string) finding { return finding{label: "index lag", ok: true, detail: why} }
	if repoRoot == "" {
		return skip("not in a git repository")
	}
	if apiKey == "" {
		return skip("skipped (no API key or server unreachable)")
	}
	branch, head, err := git.HeadRef(repoRoot)
	if err != nil {
		return skip("skipped (cannot read HEAD)")
	}
	remote, err := getRemoteURL(repoRoot)
	if err != nil {
		return skip("skipped (no remote)")
	}
	normalized, err := git.Normalize(remote)
	if err != nil {
		return skip("skipped (unrecognised remote)")
	}
	info, err := unmask.ResolveRepo(serverURL, apiKey, normalized)
	if err != nil || info == nil {
		return skip("skipped (repo not registered)")
	}
	base, err := unmask.ResolveBaseIndex(serverURL, apiKey, info.RepoID)
	if err != nil || base == nil {
		return skip("skipped (no ready base index)")
	}
	lag := indexLag{branch: branch, head: head, baseRef: base.BaseRefName, baseSHA: base.BaseRefSHA, ahead: -1}
	if n, err := git.CountCommits(repoRoot, base.BaseRefSHA, head); err == nil {
		lag.ahead = n
	}
	if st := readSyncState(repoRoot); syncCoversHead(st, branch, head) {
		lag.synced, lag.syncedAt = true, st.SyncedAt
	}
	return lag.verdict()
}

// syncCoversHead: the recorded attempt finished and sent HEAD itself — not one
// in flight or failed, and not the pushed-base fallback for an unpushed HEAD.
func syncCoversHead(st *syncState, branch, head string) bool {
	return st != nil && st.ToRef == branch && st.ToSHA == head && st.SentSHA == "" &&
		(st.Status == "" || st.Status == syncDone)
}
