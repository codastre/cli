package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/codastre/cli/internal/checkouts"
	"github.com/codastre/cli/internal/clientheader"
	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/transcript"
	"github.com/codastre/cli/internal/usage"
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

	// 2b. GitHub App availability (advisory): one-click private/org connect needs a
	// deployment-level App. Operator-fixable, so warn rather than error; skip if the
	// probe can't run (no key / server down / request error) to avoid noise.
	if serverOK && apiKey != "" {
		if configured, ok := checkGitHubApp(doctorServerURL, apiKey); ok {
			detail := ""
			if !configured {
				detail = "not configured — one-click private/org connect unavailable " +
					"(ask an operator to run `codastre-admin github-app-key`)"
			}
			findings = append(findings, finding{
				label:   "github app",
				ok:      configured,
				warning: true,
				detail:  detail,
			})
		}
	}

	// 2c. Enabled languages (informational): reports the deployment-wide set of
	// languages with full chunking/graph support. Never fails the run; skip the
	// finding if the probe can't run to avoid noise.
	if serverOK && apiKey != "" {
		if langs, ok := checkLanguages(doctorServerURL, apiKey); ok {
			detail := "none enabled"
			if len(langs) > 0 {
				detail = "enabled: " + strings.Join(langs, ", ")
			}
			findings = append(findings, finding{
				label:   "languages",
				ok:      true,
				warning: true,
				detail:  detail,
			})
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

	// 4b. Checkout registry: without an entry for a repo, its QUERY results come
	// back as bare locators — indexed and findable, but with no source. Warn
	// rather than error, since search still works.
	registered := checkouts.All()
	switch {
	case len(registered) == 0:
		findings = append(findings, finding{
			label:   "checkouts",
			ok:      false,
			warning: true,
			detail:  "none registered — results will have no snippets; run `codastre checkout scan <root>`",
		})
	default:
		detail := countLabel(len(registered), "registered checkout")
		ok := true
		if _, url, err := repoAt("."); err == nil {
			if _, known := registered[url]; !known {
				detail += "; this repo is not one of them — run `codastre checkout add`"
				ok = false
			}
		}
		findings = append(findings, finding{label: "checkouts", ok: ok, warning: true, detail: detail})
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

	// 6b. Local usage tracking (informational): names in one line whether the
	// plugin is logging search usage locally, and whether anything is being
	// uploaded. Never fails the run — both answers are legitimate, and the
	// point is that "is anything leaving my machine" is stated, not inferred.
	findings = append(findings, finding{
		label:   "usage tracking",
		ok:      true,
		warning: true,
		detail:  usageTrackingDetail(),
	})

	// 6c. OTel content logging must stay unset. Prompts and responses are
	// redacted by default and that default is load-bearing: with any of these
	// five on, a telemetry pipeline starts carrying source code and prompts.
	// This is the one usage finding that fails the run.
	if leaked := otelContentVars(); len(leaked) > 0 {
		findings = append(findings, finding{
			label:  "otel content logging",
			ok:     false,
			detail: "enabled: " + strings.Join(leaked, ", ") + " — unset these; prompts and tool output must not be exported",
		})
	} else {
		findings = append(findings, finding{label: "otel content logging", ok: true, detail: "off"})
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
	resp, err := http.Get(serverURL + "/healthz")
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
	req.Header.Set("X-Codastre-Client", clientheader.Value())
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

// checkGitHubApp reports whether a GitHub App is configured on the deployment.
// The second return is false when the probe couldn't run (so the caller skips the
// finding rather than reporting a false negative).
func checkGitHubApp(serverURL, apiKey string) (configured bool, ok bool) {
	req, err := http.NewRequest(http.MethodGet, serverURL+"/v1/github/app-config", nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Codastre-Client", clientheader.Value())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var body struct {
		Configured bool `json:"configured"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, false
	}
	return body.Configured, true
}

// checkLanguages reports the deployment-wide set of enabled languages from the
// tenant-visible security-posture endpoint. The second return is false when the
// probe couldn't run (so the caller skips the finding rather than reporting a
// false negative).
func checkLanguages(serverURL, apiKey string) (enabledLanguages []string, ok bool) {
	req, err := http.NewRequest(http.MethodGet, serverURL+"/v1/system/security-posture", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Codastre-Client", clientheader.Value())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var body struct {
		EnabledLanguages []string `json:"enabled_languages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, false
	}
	return body.EnabledLanguages, true
}

func getRemoteURL(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no remote 'origin' configured")
	}
	return strings.TrimSpace(string(out)), nil
}

// remoteURLs returns the fetch URLs of every configured remote, ordered with the
// conventional upstreams first — origin, then upstream — followed by the rest in
// git's stable (alphabetical) order. A clone may carry several remotes pointing at
// mirrors of the same repo (e.g. origin and upstream); only one is registered with
// the server, so auto-target resolution tries them in this order. Errors when the
// repo has no remotes.
func remoteURLs(repoPath string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoPath, "remote").Output()
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return nil, fmt.Errorf("no remotes configured")
	}
	sort.SliceStable(names, func(i, j int) bool {
		return remoteRank(names[i]) < remoteRank(names[j])
	})
	var urls []string
	for _, name := range names {
		u, err := exec.Command("git", "-C", repoPath, "remote", "get-url", name).Output()
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(u)); s != "" {
			urls = append(urls, s)
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no remote URLs configured")
	}
	return urls, nil
}

// remoteRank orders remotes so the conventional upstream names are tried first.
func remoteRank(name string) int {
	switch name {
	case "origin":
		return 0
	case "upstream":
		return 1
	default:
		return 2
	}
}

// usageTrackingDetail describes the local token log and the upload flag, in
// that order: what is recorded, then what is shared. Both are opt-in and
// independent — tracking writes to disk, upload is a separate decision that
// nothing in the CLI acts on yet.
func usageTrackingDetail() string {
	parts := []string{}
	if usage.TrackingEnabled() {
		parts = append(parts, "local log on (CODASTRE_TRACK_TOKENS=1)")
	} else {
		parts = append(parts, "local log off — set CODASTRE_TRACK_TOKENS=1 for `codastre savings`")
	}
	if usage.UploadEnabled() {
		parts = append(parts, "upload on (CODASTRE_USAGE_REPORT=1)")
	} else {
		parts = append(parts, "upload off — nothing leaves this machine")
	}
	if path := usage.DefaultLogPath(); path != "" {
		if info, err := os.Stat(path); err == nil {
			parts = append(parts, fmt.Sprintf("%s (%d KB)", path, info.Size()/1024))
		}
	}
	if n := len(transcript.LoadState(transcript.StatePath()).Sessions); n > 0 {
		parts = append(parts, fmt.Sprintf("%s collected from transcripts", countLabel(n, "session")))
	} else {
		parts = append(parts, "no transcripts collected — run `codastre collect`")
	}
	// Named last because it is the one source that leaves the machine, and
	// only on an explicit flag.
	parts = append(parts, "server counters on request (`codastre savings --source server`)")
	return strings.Join(parts, "; ")
}

// otelContentVars lists the Claude Code telemetry variables that would export
// prompts, responses or tool content. All five must stay unset.
func otelContentVars() []string {
	var on []string
	for _, name := range []string{
		"OTEL_LOG_USER_PROMPTS",
		"OTEL_LOG_ASSISTANT_RESPONSES",
		"OTEL_LOG_TOOL_DETAILS",
		"OTEL_LOG_TOOL_CONTENT",
		"OTEL_LOG_RAW_API_BODIES",
	} {
		if v := os.Getenv(name); v != "" && v != "0" && !strings.EqualFold(v, "false") {
			on = append(on, name)
		}
	}
	return on
}
