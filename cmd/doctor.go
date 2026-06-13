package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/keychain"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics (exit 0=all pass, 1=error, 2=warnings only)",
	// Use Run, not RunE, so we can control exit codes directly (impl-spec §2.1).
	Run: runDoctor,
}

var doctorServerURL string

func init() {
	doctorCmd.Flags().StringVar(&doctorServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	rootCmd.AddCommand(doctorCmd)
}

type finding struct {
	label   string
	ok      bool
	warning bool // false = error (exit 1), true = warning (exit 2)
	detail  string
}

func runDoctor(cmd *cobra.Command, _ []string) {
	out := cmd.OutOrStdout()
	var findings []finding

	// 1. Server reachability.
	serverOK, serverDetail := checkServer(doctorServerURL)
	findings = append(findings, finding{label: "server reachable", ok: serverOK, detail: serverDetail})

	// 2. Auth validity.
	host := extractHost(doctorServerURL)
	store, isFallback, storeErr := keychain.Open()
	var apiKey string
	if storeErr != nil {
		findings = append(findings, finding{label: "auth", ok: false, detail: storeErr.Error()})
	} else {
		var keyErr error
		apiKey, keyErr = store.GetAPIKey(host)
		if keyErr != nil {
			findings = append(findings, finding{label: "auth", ok: false, detail: "no API key — run `codastre login`"})
		} else if serverOK {
			authOK, authDetail := checkAuth(doctorServerURL, apiKey)
			findings = append(findings, finding{label: "auth", ok: authOK, detail: authDetail})
		} else {
			findings = append(findings, finding{label: "auth", ok: false, detail: "skipped (server unreachable)"})
		}
	}

	// 3. Keychain backend.
	if storeErr == nil {
		if isFallback {
			findings = append(findings, finding{
				label:   "keychain",
				ok:      false,
				warning: true,
				detail:  "file fallback active (~/.config/codastre/keys)",
			})
		} else {
			findings = append(findings, finding{label: "keychain", ok: true})
		}
	}

	// 4. Repo discovery and remote URL.
	repoRoot, gitErr := findGitRoot(".")
	if gitErr != nil {
		findings = append(findings, finding{label: "repo", ok: false, detail: "not in a git repository"})
	} else {
		remoteURL, err := getRemoteURL(repoRoot)
		if err != nil {
			findings = append(findings, finding{label: "repo", ok: false, detail: err.Error()})
		} else {
			canonical, err := git.Normalize(remoteURL)
			if err != nil {
				findings = append(findings, finding{label: "repo", ok: false, detail: "cannot normalize remote URL"})
			} else {
				findings = append(findings, finding{label: "repo", ok: true, detail: canonical})
			}
		}
	}

	// 5. Git version (min 2.30).
	if out, err := exec.Command("git", "--version").Output(); err != nil {
		findings = append(findings, finding{label: "git", ok: false, detail: "git not found"})
	} else {
		findings = append(findings, finding{label: "git", ok: true, detail: strings.TrimSpace(string(out))})
	}

	// 6. Shallow clone (warning, not error).
	if repoRoot != "" {
		_, shallowErr := os.Stat(repoRoot + "/.git/shallow")
		isShallow := shallowErr == nil
		findings = append(findings, finding{
			label:   "shallow clone",
			ok:      !isShallow,
			warning: true,
			detail:  map[bool]string{true: "manifest mode will be used for syncs", false: ""}[isShallow],
		})
	}

	// 7. Last sync result (advisory).
	findings = append(findings, finding{label: "last sync", ok: true, detail: "no sync recorded yet"})

	// Render and exit.
	hasError, hasWarning := false, false
	for _, f := range findings {
		status := "ok  "
		if !f.ok {
			if f.warning {
				status = "warn"
				hasWarning = true
			} else {
				status = "err "
				hasError = true
			}
		}
		detail := ""
		if f.detail != "" {
			detail = "  " + f.detail
		}
		fmt.Fprintf(out, "[%s] %s%s\n", status, f.label, detail)
	}

	if hasError {
		os.Exit(1)
	}
	if hasWarning {
		os.Exit(2)
	}
}

func checkServer(serverURL string) (bool, string) {
	resp, err := http.Get(serverURL + "/v1/healthz")
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return true, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

func checkAuth(serverURL, apiKey string) (bool, string) {
	req, err := http.NewRequest(http.MethodGet, serverURL+"/v1/me", nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, "API key invalid"
	}
	return true, ""
}

func getRemoteURL(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no remote 'origin' configured")
	}
	return strings.TrimSpace(string(out)), nil
}
