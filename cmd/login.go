package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/codastre/cli/internal/keychain"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate via RFC 8628 device-code flow; stores key in OS keychain",
	RunE:  runLogin,
}

var loginServerURL string

func init() {
	loginCmd.Flags().StringVar(&loginServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	rootCmd.AddCommand(loginCmd)
}

type deviceCodeResp struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

type tokenResp struct {
	APIKey string `json:"api_key"`
	Error  string `json:"error"`
}

func runLogin(cmd *cobra.Command, args []string) error {
	serverURL := strings.TrimRight(loginServerURL, "/")

	resp, err := http.Post(serverURL+"/v1/auth/device", "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, body)
	}

	var dc deviceCodeResp
	if err := json.Unmarshal(body, &dc); err != nil {
		return fmt.Errorf("decode device code response: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Open:  %s\nCode:  %s\n\nWaiting for authorization...\n",
		dc.VerificationURI, dc.UserCode)

	interval := time.Duration(dc.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		apiKey, done, delay, err := pollToken(serverURL, dc.DeviceCode)
		if err != nil {
			return err
		}
		if delay > 0 {
			interval += delay
			continue
		}
		if !done {
			continue
		}

		store, isFallback, err := keychain.Open()
		if err != nil {
			return fmt.Errorf("open keychain: %w", err)
		}
		if isFallback {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: OS keychain unavailable; using file storage")
		}
		host := extractHost(serverURL)
		if err := store.SetAPIKey(host, apiKey); err != nil {
			return fmt.Errorf("store API key: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Logged in. API key stored for %s\n", host)
		return nil
	}

	return fmt.Errorf("authorization timed out")
}

// pollToken polls the token endpoint once. Returns (apiKey, done, extraDelay, err).
func pollToken(serverURL, deviceCode string) (string, bool, time.Duration, error) {
	u := serverURL + "/v1/auth/device/token?device_code=" + url.QueryEscape(deviceCode)
	resp, err := http.Get(u)
	if err != nil {
		return "", false, 0, fmt.Errorf("poll token: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", false, 0, fmt.Errorf("decode token response: %w", err)
	}

	// RFC 8628: error field is present on 4xx responses.
	// Guard on status code first so an empty "error" field never appears done.
	if resp.StatusCode >= 400 {
		switch tr.Error {
		case "authorization_pending", "":
			return "", false, 0, nil
		case "slow_down":
			return "", false, 5 * time.Second, nil
		default:
			return "", false, 0, fmt.Errorf("token error: %s", tr.Error)
		}
	}

	if tr.APIKey == "" {
		return "", false, 0, fmt.Errorf("token response missing api_key")
	}
	return tr.APIKey, true, 0, nil
}

// extractHost returns the host (and port if non-standard) from a URL string.
func extractHost(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}
	return u.Host
}
