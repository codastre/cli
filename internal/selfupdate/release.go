package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultAPIBase is the GitHub REST API root; the release repo is public, so
	// no token is needed (unauthenticated callers get 60 requests/hour).
	DefaultAPIBase = "https://api.github.com"
	// DefaultRepo is where .goreleaser.yaml publishes CLI releases.
	DefaultRepo = "codastre/cli"

	maxArchiveBytes  = 200 << 20
	maxChecksumBytes = 1 << 20
	maxReleaseBytes  = 4 << 20
	checksumsAsset   = "checksums.txt"
)

// Release is the subset of a GitHub release this package uses.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Version is the release tag without its "v" prefix — the form buildinfo
// reports and the asset names embed.
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func (r Release) asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// ArchiveName is the goreleaser archive name_template for one platform.
func ArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("codastre_%s_%s_%s.%s", version, goos, goarch, ext)
}

// Client talks to the GitHub releases API.
type Client struct {
	APIBase string
	Repo    string
	HTTP    *http.Client
}

// NewClient returns a Client for the public codastre/cli release repo.
func NewClient() *Client {
	return &Client{APIBase: DefaultAPIBase, Repo: DefaultRepo, HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

// Latest returns the newest non-prerelease release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	return c.fetchRelease(ctx, "/releases/latest")
}

// ByVersion returns the release tagged v<version> ("v" optional in input).
func (c *Client) ByVersion(ctx context.Context, version string) (Release, error) {
	return c.fetchRelease(ctx, "/releases/tags/v"+strings.TrimPrefix(version, "v"))
}

func (c *Client) fetchRelease(ctx context.Context, path string) (Release, error) {
	url := strings.TrimRight(c.APIBase, "/") + "/repos/" + c.Repo + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, errors.New("release not found")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("GitHub API rate limit hit (HTTP %d) — retry later", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("fetch release: HTTP %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBytes)).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("decode release: %w", err)
	}
	if rel.Tag == "" {
		return Release{}, errors.New("release has no tag")
	}
	return rel, nil
}

// DownloadArchive fetches the platform archive and verifies its SHA-256
// against the release's checksums.txt. It never returns unverified bytes.
func (c *Client) DownloadArchive(ctx context.Context, rel Release, goos, goarch string) (name string, data []byte, err error) {
	name = ArchiveName(rel.Version(), goos, goarch)
	archive, ok := rel.asset(name)
	if !ok {
		return "", nil, fmt.Errorf("release %s has no asset %s for %s/%s", rel.Tag, name, goos, goarch)
	}
	sums, ok := rel.asset(checksumsAsset)
	if !ok {
		return "", nil, fmt.Errorf("release %s has no %s; refusing an unverifiable download", rel.Tag, checksumsAsset)
	}
	sumData, err := c.download(ctx, sums.URL, maxChecksumBytes)
	if err != nil {
		return "", nil, err
	}
	want, err := checksumFor(sumData, name)
	if err != nil {
		return "", nil, err
	}
	data, err = c.download(ctx, archive.URL, maxArchiveBytes)
	if err != nil {
		return "", nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return "", nil, fmt.Errorf("checksum mismatch for %s", name)
	}
	return name, data, nil
}

func (c *Client) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download %s: exceeds %d bytes", url, limit)
	}
	return data, nil
}

// checksumFor finds name's hex digest in a `sha256sum`-format file.
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s lists no checksum for %s", checksumsAsset, name)
}
