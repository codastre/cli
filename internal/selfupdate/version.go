// Package selfupdate replaces the running codastre binary with a release
// published to the codastre/cli GitHub mirror (see .goreleaser.yaml): resolve
// the release, download the platform archive, verify it against the release's
// checksums.txt, extract the binary and swap it in atomically.
package selfupdate

import (
	"strconv"
	"strings"
)

// semver is a parsed MAJOR.MINOR.PATCH[-pre] version. Build metadata is
// ignored; a pre-release sorts before its release, per SemVer §11.
type semver struct {
	major, minor, patch int
	pre                 string
}

// parseSemver accepts "1.2.3", "v1.2.3" and "1.2.3-rc.1". It rejects the
// non-SemVer strings buildinfo falls back to ("dev", a short commit).
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, true
}

// compare returns -1, 0 or 1. Pre-release identifiers compare lexically —
// enough to order this project's tags without a full §11 implementation.
func (a semver) compare(b semver) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return sign(strings.Compare(a.pre, b.pre))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Status is the outcome of comparing the running version against a release.
type Status int

const (
	// UpToDate: current ≥ release.
	UpToDate Status = iota
	// Outdated: current < release.
	Outdated
	// Unknown: current is not a release version (dev build, short commit,
	// "-dirty" tree), so it cannot be ordered against the release.
	Unknown
)

// Compare classifies current against release. A "-dirty" suffix makes the
// current version Unknown: a locally modified build is not that release.
func Compare(current, release string) Status {
	if strings.HasSuffix(current, "-dirty") {
		return Unknown
	}
	cur, ok := parseSemver(current)
	if !ok {
		return Unknown
	}
	rel, ok := parseSemver(release)
	if !ok {
		return Unknown
	}
	if cur.compare(rel) < 0 {
		return Outdated
	}
	return UpToDate
}
