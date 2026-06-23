package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Open the web dashboard in an authenticated session",
	Long: `Open the codastre dashboard already signed in, using the API key from your
OS keychain (or --key / $CODASTRE_API_KEY).

The key is handed to the browser over a one-shot localhost server — it never
appears in a URL, browser history, or server log. The CLI serves the key once to
the dashboard's /cli-auth page (locked to the dashboard origin via CORS and a
single-use nonce), then the local server shuts down.`,
	RunE: runDashboard,
}

var (
	dashboardServerURL string
	dashboardURL       string
	dashboardKeyFlag   string
	dashboardNoBrowser bool
)

func init() {
	dashboardCmd.Flags().StringVar(&dashboardServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	dashboardCmd.Flags().StringVar(&dashboardURL, "dashboard", "", "Dashboard URL (default: auto-discovered from the server) [$CODASTRE_DASHBOARD_URL]")
	dashboardCmd.Flags().StringVar(&dashboardKeyFlag, "key", "", "API key override (default: keychain / $CODASTRE_API_KEY)")
	dashboardCmd.Flags().BoolVar(&dashboardNoBrowser, "no-browser", false, "Print the URL instead of opening a browser")
	rootCmd.AddCommand(dashboardCmd)
}

// resolveDashboardURL determines which dashboard origin to open, by precedence:
//
//	--dashboard flag  >  $CODASTRE_DASHBOARD_URL  >  server auto-discovery  >  localhost
//
// Auto-discovery asks the server (which the CLI already knows) for its configured
// dashboard URL, so a deployment behind a real domain needs no client-side config.
// warn is a non-fatal message printed to stderr when discovery fails and the
// localhost fallback is used; it is empty otherwise.
func resolveDashboardURL(serverURL string) (dash, warn string) {
	if dashboardURL != "" {
		return strings.TrimRight(dashboardURL, "/"), ""
	}
	if v := os.Getenv("CODASTRE_DASHBOARD_URL"); v != "" {
		return strings.TrimRight(v, "/"), ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if d := discover(ctx, serverURL); d != nil && d.DashboardURL != "" {
		return strings.TrimRight(d.DashboardURL, "/"), ""
	}
	return "http://localhost:3000", "could not auto-discover the dashboard URL from " + serverURL +
		"; falling back to http://localhost:3000 (set --dashboard or $CODASTRE_DASHBOARD_URL to override)"
}

func runDashboard(cmd *cobra.Command, _ []string) error {
	serverURL := strings.TrimRight(dashboardServerURL, "/")
	dashURL, dashWarn := resolveDashboardURL(serverURL)
	if dashWarn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", dashWarn)
	}

	apiKey, warn, err := resolveAPIKey(serverURL, dashboardKeyFlag)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warn)
	}

	// CORS is locked to the dashboard's exact origin so no other site can read the
	// key from the loopback server.
	dashOrigin, err := originOf(dashURL)
	if err != nil {
		return fmt.Errorf("invalid --dashboard URL: %w", err)
	}

	nonce, err := randomHex(32)
	if err != nil {
		return err
	}

	// Bind to loopback only, on an OS-assigned free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start local handoff server: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	served := make(chan error, 1)
	srv := &http.Server{Handler: handoffHandler(apiKey, nonce, dashOrigin, served)}
	go func() { _ = srv.Serve(ln) }()

	handoff := fmt.Sprintf("%s/cli-auth?port=%d&nonce=%s", dashURL, port, url.QueryEscape(nonce))

	if dashboardNoBrowser {
		fmt.Fprintf(cmd.OutOrStdout(), "Open this URL to sign in:\n\n  %s\n\n", handoff)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Opening the dashboard… (waiting for the browser to pick up the session)\n")
		if err := openBrowser(handoff); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "could not open a browser (%v). Open this URL manually:\n\n  %s\n\n", err, handoff)
		}
	}

	// Wait for the browser to fetch the key once, or time out.
	var runErr error
	select {
	case <-served:
		fmt.Fprintln(cmd.OutOrStdout(), "Dashboard session established.")
	case <-time.After(2 * time.Minute):
		runErr = fmt.Errorf("timed out waiting for the dashboard to pick up the session")
	}

	// Graceful shutdown — let the in-flight handoff response flush fully to the
	// browser before the listener closes. An immediate Close() races the response
	// write and truncates it, so the page sees a failed fetch ("could not reach the
	// CLI") even though the key was served.
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	return runErr
}

// handoffHandler serves the API key exactly once to a request carrying the
// matching nonce, with CORS restricted to the dashboard origin. It signals
// completion on done and refuses any subsequent or mismatched request.
func handoffHandler(apiKey, nonce, allowOrigin string, done chan<- error) http.Handler {
	var fired bool
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if fired || r.URL.Query().Get("nonce") != nonce {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fired = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"api_key": apiKey})
		// Push the body to the socket before signalling done, so the response is
		// fully on the wire even if shutdown begins immediately.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		done <- nil
	})
	return mux
}

// originOf returns the scheme://host[:port] origin of a URL.
func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("not an absolute URL: %q", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// openBrowser opens url in the platform's default browser (no extra dependency).
func openBrowser(target string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", target)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default: // linux / *bsd
		c = exec.Command("xdg-open", target)
	}
	return c.Start()
}
