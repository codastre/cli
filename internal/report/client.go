package report

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/codastre/cli/internal/clientheader"
)

// Client talks to the three Plane 3 endpoints with the caller's own key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// Ack is the server's per-batch acknowledgement.
type Ack struct {
	Received int `json:"received"`
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
}

func (a *Ack) add(b Ack) {
	a.Received += b.Received
	a.Inserted += b.Inserted
	a.Updated += b.Updated
	a.Skipped += b.Skipped
}

// ErrUnsupported reports a server that predates Plane 3 ingestion.
var ErrUnsupported = errors.New("server does not accept usage uploads")

// SessionKey fetches (and on first use, mints) the per-tenant usage key.
func (c *Client) SessionKey(ctx context.Context) ([]byte, error) {
	body, err := c.do(ctx, http.MethodGet, "/v1/me/usage/session-key", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Key       string `json:"key"`
		Algorithm string `json:"algorithm"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode session key: %w", err)
	}
	if out.Algorithm != "" && out.Algorithm != "hmac-sha256" {
		return nil, fmt.Errorf("unsupported session key algorithm %q", out.Algorithm)
	}
	key, err := hex.DecodeString(out.Key)
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("server returned a malformed session key")
	}
	return key, nil
}

// PostEpisodes uploads one batch (≤ MaxBatch) of episodes. Server errors —
// 413 USAGE_BATCH_TOO_LARGE, 422 EPISODE_INVALID/SESSION_INVALID with the
// row index, 503 USAGE_UNAVAILABLE — are surfaced with their body verbatim.
func (c *Client) PostEpisodes(ctx context.Context, rows []EpisodeRow) (Ack, error) {
	return c.post(ctx, "/v1/usage/episodes", map[string]any{"episodes": rows})
}

// PostSessions uploads one batch (≤ MaxBatch) of session rows.
func (c *Client) PostSessions(ctx context.Context, rows []SessionRow) (Ack, error) {
	return c.post(ctx, "/v1/usage/sessions", map[string]any{"sessions": rows})
}

func (c *Client) post(ctx context.Context, path string, payload any) (Ack, error) {
	blob, err := json.Marshal(payload)
	if err != nil {
		return Ack{}, err
	}
	body, err := c.do(ctx, http.MethodPost, path, blob)
	if err != nil {
		return Ack{}, err
	}
	var ack Ack
	if err := json.Unmarshal(body, &ack); err != nil {
		return Ack{}, fmt.Errorf("decode %s response: %w", path, err)
	}
	return ack, nil
}

func (c *Client) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
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
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("not authenticated to %s (HTTP %d): run `codastre login`, "+
			"set $CODASTRE_API_KEY, or pass --key", base, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s does not serve %s (HTTP 404): the server predates usage "+
			"uploads — upgrade it: %w", base, path, ErrUnsupported)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("%s %s failed (%d): %s", method, path, resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	return body, nil
}
