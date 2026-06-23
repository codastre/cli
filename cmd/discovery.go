package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// serverDiscovery is the public, unauthenticated configuration the server
// exposes at GET /v1/system/discovery so the CLI can auto-discover deployment
// URLs instead of relying on hardcoded localhost defaults.
type serverDiscovery struct {
	DashboardURL  string `json:"dashboard_url"`
	ServerBaseURL string `json:"server_base_url"`
	Version       string `json:"version"`
}

// discover fetches the server's public discovery document. It is best-effort:
// any error — an older server without the endpoint, a network failure, or a
// malformed body — yields nil so callers fall back to their own defaults.
func discover(ctx context.Context, serverURL string) *serverDiscovery {
	base := strings.TrimRight(serverURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/system/discovery", nil)
	if err != nil {
		return nil
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var d serverDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&d); err != nil {
		return nil
	}
	return &d
}
