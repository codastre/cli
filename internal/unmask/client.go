package unmask

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RepoInfo is the subset of GET /v1/repos a client needs to set up unmasking.
type RepoInfo struct {
	RepoID        string `json:"repo_id"`
	RemoteURL     string `json:"remote_url"`
	MaskingScheme string `json:"masking_scheme"`
	MaskKeyRev    int    `json:"mask_key_rev"`
}

// IsMasked reports whether the repo uses HMAC path masking.
func (r RepoInfo) IsMasked() bool { return r.MaskingScheme == "hmac" }

type repoPage struct {
	Items      []RepoInfo `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

// ResolveRepo finds the repo whose remote_url matches normalizedRemote by paging
// GET /v1/repos. Returns (nil, nil) when no visible repo matches — the caller
// then proceeds without unmasking rather than failing.
func ResolveRepo(serverURL, apiKey, normalizedRemote string) (*RepoInfo, error) {
	base := strings.TrimRight(serverURL, "/")
	cursor := ""
	for {
		u := base + "/v1/repos?limit=200"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := getJSON[repoPage](u, apiKey)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			if page.Items[i].RemoteURL == normalizedRemote {
				return &page.Items[i], nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return nil, nil
		}
		cursor = *page.NextCursor
	}
}

// ListRepos returns every repo visible to the API key by paging GET /v1/repos.
// The serve proxy uses it to build a repo_id → {remote_url, masking_scheme}
// snapshot so federated hits can be hydrated per-repo (including cleartext
// `none`-scheme repos, whose path_token is already the real path). Best-effort:
// callers fall back to CWD-only hydration on error.
func ListRepos(serverURL, apiKey string) ([]RepoInfo, error) {
	base := strings.TrimRight(serverURL, "/")
	cursor := ""
	var all []RepoInfo
	for {
		u := base + "/v1/repos?limit=200"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := getJSON[repoPage](u, apiKey)
		if err != nil {
			return all, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, nil
		}
		cursor = *page.NextCursor
	}
}

// IndexInfo is the subset of GET /v1/repos/{id}/indexes a SYNC needs: the base
// index to diff a feature branch against.
type IndexInfo struct {
	IndexID     string `json:"index_id"`
	BaseRefName string `json:"base_ref_name"`
	BaseRefSHA  string `json:"base_ref_sha"`
	Status      string `json:"status"`
}

var defaultBranchNames = map[string]bool{
	"main": true, "master": true, "trunk": true,
}

// ResolveBaseIndex returns the ready base index for repoID, preferring one whose
// base_ref is a conventional default branch (main/master/trunk). Returns
// (nil, nil) when the repo has no ready base index yet.
func ResolveBaseIndex(serverURL, apiKey, repoID string) (*IndexInfo, error) {
	u := strings.TrimRight(serverURL, "/") + "/v1/repos/" + url.PathEscape(repoID) + "/indexes"
	indexes, err := getJSON[[]IndexInfo](u, apiKey)
	if err != nil {
		return nil, err
	}
	var fallback *IndexInfo
	for i := range indexes {
		if indexes[i].Status != "ready" {
			continue
		}
		if defaultBranchNames[indexes[i].BaseRefName] {
			return &indexes[i], nil
		}
		if fallback == nil {
			fallback = &indexes[i]
		}
	}
	return fallback, nil
}

type maskingKeyResponse struct {
	Revs map[string]string `json:"revs"` // rev-string → hex key
}

// FetchMaskingKeys returns every live masking-key revision for repoID, decoded
// from the hex the server returns. A 204 (masking disabled) yields an empty map.
func FetchMaskingKeys(serverURL, apiKey, repoID string) (map[int][]byte, error) {
	u := strings.TrimRight(serverURL, "/") + "/v1/repos/" + url.PathEscape(repoID) + "/masking-key"
	body, status, err := getRaw(u, apiKey)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return map[int][]byte{}, nil
	}
	if status >= 400 {
		return nil, fmt.Errorf("GET masking-key failed (%d): %s", status, strings.TrimSpace(string(body)))
	}

	var resp maskingKeyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode masking-key response: %w", err)
	}
	out := make(map[int][]byte, len(resp.Revs))
	for revStr, keyHex := range resp.Revs {
		rev, err := strconv.Atoi(revStr)
		if err != nil {
			return nil, fmt.Errorf("invalid rev %q in masking-key response", revStr)
		}
		key, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid hex key for rev %d: %w", rev, err)
		}
		out[rev] = key
	}
	return out, nil
}

// getJSON performs an authenticated GET and decodes the JSON body into T.
func getJSON[T any](u, apiKey string) (T, error) {
	var zero T
	body, status, err := getRaw(u, apiKey)
	if err != nil {
		return zero, err
	}
	if status >= 400 {
		return zero, fmt.Errorf("GET %s failed (%d): %s", u, status, strings.TrimSpace(string(body)))
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("decode %s: %w", u, err)
	}
	return out, nil
}

// getRaw performs an authenticated GET and returns the raw body and status code.
func getRaw(u, apiKey string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
