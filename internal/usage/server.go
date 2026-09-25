package usage

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

// ServerSummary is the subset of GET /v1/me/usage the CLI renders. Unknown and
// future fields are ignored here and preserved in Raw, so an older CLI against
// a newer server prints what it understands rather than failing.
type ServerSummary struct {
	Window          string        `json:"window"`
	Calls           int           `json:"calls"`
	EnvelopeTokens  int           `json:"envelope_tokens"`
	ReposTouchedGE2 int           `json:"repos_touched_ge2"`
	Receipt         ServerReceipt `json:"receipt"`
	// Episodes is the Plane 3 block. The server omits it (rather than
	// zeroing it) when the caller has no episodes, and so does the rendering.
	Episodes *ServerEpisodes `json:"episodes"`

	// Raw is the server's response body, byte for byte, for --json passthrough.
	Raw json.RawMessage `json:"-"`
}

// ServerReceipt is the machine-readable transparency block. Its strings are
// rendered verbatim: the CLI never composes its own explanation of a server
// number, because that is how the CLI and the dashboard drift.
type ServerReceipt struct {
	Formula    string          `json:"formula"`
	Inputs     json.RawMessage `json:"inputs"`
	N          *int            `json:"n"`
	Coverage   json.RawMessage `json:"coverage"`
	Provenance string          `json:"provenance"`
	Caveat     string          `json:"caveat"`
	RerunCmd   string          `json:"rerun_cmd"`
}

// ServerEpisodes is the caller's own episode outcomes (Plane 3). It carries
// its own receipt: the planes are never combined into one figure.
type ServerEpisodes struct {
	Source                  string         `json:"source"`
	Total                   int            `json:"total"`
	CodastreOnly            int            `json:"codastre_only"`
	FallbackAfterCodastre   int            `json:"fallback_after_codastre"`
	CodastreFailed          int            `json:"codastre_failed"`
	TextSearchOnly          int            `json:"text_search_only"`
	Unclassified            int            `json:"unclassified"`
	AnsweredWithoutFallback *float64       `json:"answered_without_fallback"`
	Receipt                 *ServerReceipt `json:"receipt"`
}

// ErrServerSourceUnsupported reports a deployment with no /v1/me/usage.
var ErrServerSourceUnsupported = errors.New("server has no /v1/me/usage endpoint")

// FetchServer reads the caller's own usage counters from the control plane.
// It returns actionable errors: an unauthenticated key, an unreachable server
// and an endpoint-less (older) server are three different fixes.
func FetchServer(ctx context.Context, serverURL, apiKey, window string) (ServerSummary, error) {
	base := strings.TrimRight(serverURL, "/")
	u := base + "/v1/me/usage?window=" + url.QueryEscape(window)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ServerSummary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Codastre-Client", clientheader.Value())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ServerSummary{}, fmt.Errorf(
			"cannot reach %s: %w — check --server or $CODASTRE_SERVER, or use "+
				"`--source transcript` / `--source log` for local numbers", base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ServerSummary{}, fmt.Errorf("read /v1/me/usage response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return ServerSummary{}, fmt.Errorf(
			"not authenticated to %s (HTTP %d): run `codastre login`, set $CODASTRE_API_KEY, "+
				"or pass --key", base, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return ServerSummary{}, fmt.Errorf(
			"%s does not serve GET /v1/me/usage (HTTP 404): the server predates per-developer "+
				"usage metrics — upgrade it, or use `--source transcript` / `--source log`: %w",
			base, ErrServerSourceUnsupported)
	case resp.StatusCode >= 400:
		return ServerSummary{}, fmt.Errorf("GET /v1/me/usage failed (%d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var s ServerSummary
	if err := json.Unmarshal(body, &s); err != nil {
		return ServerSummary{}, fmt.Errorf("decode /v1/me/usage: %w", err)
	}
	s.Raw = json.RawMessage(body)
	return s, nil
}

// RenderServer prints the server's counters and its receipt. Counters only:
// no saved figure, no baseline, no counterfactual, and no USD derived from
// these numbers — the server did not measure any of those.
func RenderServer(w io.Writer, serverURL string, s ServerSummary) {
	window := s.Window
	if window == "" {
		window = "unknown"
	}
	fmt.Fprintf(w, "codastre savings — server, %s window\n", window)
	fmt.Fprintf(w, "source: %s /v1/me/usage (your own rows)\n\n", strings.TrimRight(serverURL, "/"))

	fmt.Fprintf(w, "  %-26s %s\n", "calls", thousands(s.Calls))
	fmt.Fprintf(w, "  %-26s %s\n", "envelope tokens", thousands(s.EnvelopeTokens))
	fmt.Fprintf(w, "  %-26s %s\n", "repos touched (≥2)", thousands(s.ReposTouchedGE2))

	renderReceipt(w, s.Receipt)
	if s.Episodes != nil {
		renderServerEpisodes(w, *s.Episodes)
	}
	fmt.Fprintln(w, "\nNo counterfactual is computed: these are counts of what ran.")
}

// renderServerEpisodes prints the episode outcomes with the denominator and
// both failure rows visible, so the bad news can be computed by the reader.
func renderServerEpisodes(w io.Writer, e ServerEpisodes) {
	source := e.Source
	if source == "" {
		source = "unknown"
	}
	fmt.Fprintf(w, "\nEpisodes — source: %s\n", source)
	denom := e.CodastreOnly + e.FallbackAfterCodastre + e.CodastreFailed
	fmt.Fprintf(w, "  %-26s %s\n", "episodes", thousands(e.Total))
	fmt.Fprintf(w, "  %-26s %s\n", "with a codastre call", thousands(denom))
	fmt.Fprintf(w, "  ├─ %-23s %s   ← numerator\n", "codastre only", thousands(e.CodastreOnly))
	fmt.Fprintf(w, "  ├─ %-23s %s\n", "fallback after codastre", thousands(e.FallbackAfterCodastre))
	fmt.Fprintf(w, "  └─ %-23s %s\n", "codastre failed", thousands(e.CodastreFailed))
	fmt.Fprintf(w, "  %-26s %s\n", "text search only", thousands(e.TextSearchOnly))
	fmt.Fprintf(w, "  %-26s %s   (in no rate)\n", "unclassified", thousands(e.Unclassified))
	if e.AnsweredWithoutFallback != nil {
		fmt.Fprintf(w, "  answered without fallback: %d / %d = %.1f%%\n",
			e.CodastreOnly, denom, *e.AnsweredWithoutFallback*100)
	} else {
		fmt.Fprintln(w, "  answered without fallback: n/a (no episode with a codastre call)")
	}
	if e.Receipt != nil {
		renderReceipt(w, *e.Receipt)
	}
}

// renderReceipt prints the server's transparency block verbatim — the strings
// are the server's, unedited, so the CLI and the dashboard explain the same
// number the same way.
func renderReceipt(w io.Writer, r ServerReceipt) {
	fmt.Fprintln(w, "\nReceipt")
	if r.Formula != "" {
		fmt.Fprintf(w, "  formula: %s\n", r.Formula)
	}
	if inputs := compactJSON(r.Inputs); inputs != "" {
		fmt.Fprintf(w, "  inputs: %s\n", inputs)
	}
	if r.N != nil {
		fmt.Fprintf(w, "  n: %d\n", *r.N)
	}
	if cov := compactJSON(r.Coverage); cov != "" {
		fmt.Fprintf(w, "  coverage: %s\n", cov)
	}
	if r.Provenance != "" {
		fmt.Fprintf(w, "  provenance: %s\n", r.Provenance)
	}
	if r.Caveat != "" {
		fmt.Fprintf(w, "  caveat: %s\n", r.Caveat)
	}
	if r.RerunCmd != "" {
		fmt.Fprintf(w, "  rerun: %s\n", r.RerunCmd)
	}
}

// compactJSON renders a raw JSON value on one line, dropping null and absent
// values. It never reshapes the server's data — only whitespace.
func compactJSON(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return s
	}
	return buf.String()
}
