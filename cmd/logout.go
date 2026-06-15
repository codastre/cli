package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/codastre/cli/internal/keychain"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Revoke the stored API key server-side and remove it from the keychain",
	RunE:  runLogout,
}

var (
	logoutServerURL string
	logoutLocalOnly bool
)

func init() {
	logoutCmd.Flags().StringVar(&logoutServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	logoutCmd.Flags().BoolVar(&logoutLocalOnly, "local-only", false, "Only remove the local key; do not revoke it server-side")
	rootCmd.AddCommand(logoutCmd)
}

type meResp struct {
	PrincipalID string `json:"principal_id"`
}

func runLogout(cmd *cobra.Command, _ []string) error {
	serverURL := strings.TrimRight(logoutServerURL, "/")
	host := extractHost(serverURL)

	store, isFallback, err := keychain.Open()
	if err != nil {
		return fmt.Errorf("open keychain: %w", err)
	}
	if isFallback {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: OS keychain unavailable; using file storage")
	}

	apiKey, err := store.GetAPIKey(host)
	if err != nil || apiKey == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Not logged in for %s — nothing to do.\n", host)
		return nil
	}

	// Revoke server-side unless the caller opted out.
	if !logoutLocalOnly {
		if revokeErr := revokeServerKey(serverURL, apiKey); revokeErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: could not revoke key server-side (%v); removing local key anyway.\n"+
					"         The key may still be valid — revoke it from the dashboard if needed.\n",
				revokeErr)
		}
	}

	if err := store.DeleteAPIKey(host); err != nil {
		return fmt.Errorf("remove local key: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Logged out. API key removed for %s\n", host)
	return nil
}

// revokeServerKey resolves the key's own id via GET /v1/me, then revokes it via
// DELETE /v1/api-keys/{key_id}. principal_id from /v1/me is the key_id.
func revokeServerKey(serverURL, apiKey string) error {
	req, err := http.NewRequest(http.MethodGet, serverURL+"/v1/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET /v1/me failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var me meResp
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return fmt.Errorf("decode /v1/me: %w", err)
	}
	if me.PrincipalID == "" {
		return fmt.Errorf("/v1/me returned no principal_id")
	}

	delReq, err := http.NewRequest(http.MethodDelete, serverURL+"/v1/api-keys/"+me.PrincipalID, nil)
	if err != nil {
		return err
	}
	delReq.Header.Set("Authorization", "Bearer "+apiKey)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		return err
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(delResp.Body)
		return fmt.Errorf("DELETE /v1/api-keys/%s failed (%d): %s",
			me.PrincipalID, delResp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
