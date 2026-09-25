package study

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/codastre/cli/internal/clientheader"
)

// Client talks to the study endpoints with the caller's own key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// ErrUnsupported reports a server that predates Plane 4 studies.
var ErrUnsupported = errors.New("server does not serve paired studies")

// TaskInfo is one pre-registered task as a participant sees it: hashes, and
// no prompt or acceptance text.
type TaskInfo struct {
	Task             string `json:"task"`
	Shape            string `json:"shape"`
	PromptSHA256     string `json:"prompt_sha256"`
	AcceptanceSHA256 string `json:"acceptance_sha256"`
}

// Info is one study in `GET /v1/usage/studies`.
type Info struct {
	StudyID     string     `json:"study_id"`
	Slug        string     `json:"slug"`
	Design      string     `json:"design"`
	ToolArmMode string     `json:"tool_arm_mode"`
	TargetN     int        `json:"target_n"`
	Status      string     `json:"status"`
	CreatedAt   string     `json:"created_at"`
	Tasks       []TaskInfo `json:"tasks"`
}

// Assignment is the server's answer to a start request.
type Assignment struct {
	AssignmentID string `json:"assignment_id"`
	Study        string `json:"study"`
	Task         string `json:"task"`
	Design       string `json:"design"`
	PairRef      string `json:"pair_ref"`
	Arm          string `json:"arm"`
	ArmOrder     int    `json:"arm_order"`
	Mode         string `json:"mode"`
	Prompt       string `json:"prompt"`
	PromptSHA256 string `json:"prompt_sha256"`
	AssignedAt   string `json:"assigned_at"`
	Reused       bool   `json:"reused"`
}

// BlindItem is one entry in the judge's queue: no arm, no order, no pair.
type BlindItem struct {
	AssignmentID     string `json:"assignment_id"`
	Task             string `json:"task"`
	Acceptance       string `json:"acceptance"`
	AcceptanceSHA256 string `json:"acceptance_sha256"`
	Bound            bool   `json:"bound"`
	Correct          *bool  `json:"correct"`
}

// Blind is `GET /v1/usage/studies/{slug}/blind`.
type Blind struct {
	Study string      `json:"study"`
	Items []BlindItem `json:"items"`
}

// List returns the tenant's studies.
func (c *Client) List(ctx context.Context) ([]Info, error) {
	body, err := c.do(ctx, http.MethodGet, "/v1/usage/studies", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Studies []Info `json:"studies"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode studies: %w", err)
	}
	return out.Studies, nil
}

// Assign requests (or re-fetches) the caller's assignment on a task.
func (c *Client) Assign(ctx context.Context, slug, task string) (Assignment, error) {
	payload := map[string]any{}
	if task != "" {
		payload["task"] = task
	}
	body, err := c.do(ctx, http.MethodPost, "/v1/usage/studies/"+url.PathEscape(slug)+"/assignments", payload)
	if err != nil {
		return Assignment{}, err
	}
	var a Assignment
	if err := json.Unmarshal(body, &a); err != nil {
		return Assignment{}, fmt.Errorf("decode assignment: %w", err)
	}
	if a.AssignmentID == "" || a.Mode == "" || a.Prompt == "" {
		return Assignment{}, fmt.Errorf("server returned an incomplete assignment")
	}
	return a, nil
}

// View returns the raw study view body (`GET /v1/usage/study`).
func (c *Client) View(ctx context.Context, slug string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/v1/usage/study?slug="+url.QueryEscape(slug), nil)
}

// Blind returns the judge's queue.
func (c *Client) Blind(ctx context.Context, slug string) (Blind, error) {
	body, err := c.do(ctx, http.MethodGet, "/v1/usage/studies/"+url.PathEscape(slug)+"/blind", nil)
	if err != nil {
		return Blind{}, err
	}
	var b Blind
	if err := json.Unmarshal(body, &b); err != nil {
		return Blind{}, fmt.Errorf("decode blind queue: %w", err)
	}
	return b, nil
}

// Verdict records a blind verdict on one assignment.
func (c *Client) Verdict(ctx context.Context, slug, assignmentID string, correct bool) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/usage/studies/"+url.PathEscape(slug)+"/verdicts",
		map[string]any{"assignment_id": assignmentID, "correct": correct})
	return err
}

func (c *Client) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	var rd io.Reader
	if payload != nil {
		blob, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(blob)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Codastre-Client", clientheader.Value())
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w — check --server or $CODASTRE_SERVER", base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("not authenticated to %s (HTTP 401): run `codastre login`, "+
			"set $CODASTRE_API_KEY, or pass --key", base)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%s %s is admin-only (HTTP 403)", method, path)
	case resp.StatusCode == http.StatusNotFound && isRouteMissing(body):
		return nil, fmt.Errorf("%s does not serve %s (HTTP 404): the server predates paired "+
			"studies — upgrade it: %w", base, path, ErrUnsupported)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("%s %s failed (%d): %s", method, path, resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	return body, nil
}

// isRouteMissing tells FastAPI's bare route-level 404 ({"detail":"Not Found"})
// from a study-level one (STUDY_NOT_FOUND), which must reach the user as is.
func isRouteMissing(body []byte) bool {
	var v struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(body, &v) != nil {
		return true
	}
	s, ok := v.Detail.(string)
	return ok && s == "Not Found"
}
