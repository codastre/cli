package cmd

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/unmask"
	"github.com/spf13/cobra"
)

var maskingKeyCmd = &cobra.Command{
	Use:     "masking-key",
	Aliases: []string{"mask-key"},
	Short:   "Copy a repo's HMAC masking key to the clipboard (hex)",
	Long: `Resolve the repo for the current directory (or --repo-url), look up its
masking key in the OS keychain — fetching from the server if not cached — and
copy it to the clipboard as hex. Use --rev to pick a specific key revision and
--print to write to stdout instead of the clipboard.

The masking key is a secret: copying it exposes it to any app that can read the
clipboard. Clear your clipboard when you're done.`,
	RunE: runMaskingKey,
}

var (
	maskingKeyServerURL string
	maskingKeyRepoURL   string
	maskingKeyRev       int
	maskingKeyPrint     bool
)

func init() {
	maskingKeyCmd.Flags().StringVar(&maskingKeyServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	maskingKeyCmd.Flags().StringVar(&maskingKeyRepoURL, "repo-url", "", "Repo remote URL (default: the current directory's origin)")
	maskingKeyCmd.Flags().IntVar(&maskingKeyRev, "rev", -1, "Key revision (default: the repo's current rev)")
	maskingKeyCmd.Flags().BoolVar(&maskingKeyPrint, "print", false, "Print the key to stdout instead of copying to the clipboard")
	rootCmd.AddCommand(maskingKeyCmd)
}

func runMaskingKey(cmd *cobra.Command, _ []string) error {
	serverURL := strings.TrimRight(maskingKeyServerURL, "/")
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
		return fmt.Errorf("no API key for %s — run `codastre login` first", host)
	}

	// Resolve the repo (and its current rev) from --repo-url or the cwd's origin.
	normalized, err := resolveRepoURL(maskingKeyRepoURL)
	if err != nil {
		return err
	}
	info, err := unmask.ResolveRepo(serverURL, apiKey, normalized)
	if err != nil {
		return fmt.Errorf("resolve repo %s: %w", normalized, err)
	}
	if info == nil {
		return fmt.Errorf("no repo visible to you matches %s", normalized)
	}
	if !info.IsMasked() {
		return fmt.Errorf("repo %s has masking disabled — there is no key", normalized)
	}

	rev := maskingKeyRev
	if rev < 0 {
		rev = info.MaskKeyRev
	}

	// Keychain first; fall back to the server (which also caches it).
	key, kerr := store.GetMaskKey(host, info.RepoID, rev)
	if kerr != nil || len(key) == 0 {
		fetched, ok, ferr := fetchMaskKey(serverURL, apiKey, host, info.RepoID, rev, false, store)
		if ferr != nil {
			return fmt.Errorf("fetch masking key rev %d: %w", rev, ferr)
		}
		if !ok || len(fetched) == 0 {
			return fmt.Errorf("no masking key for %s rev %d", normalized, rev)
		}
		key = fetched
	}
	keyHex := hex.EncodeToString(key)

	if maskingKeyPrint {
		fmt.Fprintln(cmd.OutOrStdout(), keyHex)
		return nil
	}
	if err := copyToClipboard(keyHex); err != nil {
		return fmt.Errorf("copy to clipboard: %w (use --print to write it to stdout)", err)
	}
	// Confirmation only — never echo the key itself to a stream.
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Copied masking key rev %d for %s to the clipboard (%d hex chars).\n"+
			"It is a secret — clear your clipboard when you're done.\n",
		rev, normalized, len(keyHex))
	return nil
}

// resolveRepoURL returns the normalized remote URL: the explicit override when
// given, else the origin of the current directory's git repo.
func resolveRepoURL(override string) (string, error) {
	if override != "" {
		if n, err := git.Normalize(override); err == nil {
			return n, nil
		}
		// Already-canonical "host/owner/repo" (as shown in the repos list) isn't a
		// recognized URL for Normalize; accept it as-is.
		return strings.TrimRight(override, "/"), nil
	}
	repoRoot, err := findGitRoot(".")
	if err != nil {
		return "", fmt.Errorf("not in a git repo — pass --repo-url")
	}
	remote, err := getRemoteURL(repoRoot)
	if err != nil {
		return "", fmt.Errorf("no origin remote — pass --repo-url: %w", err)
	}
	normalized, err := git.Normalize(remote)
	if err != nil {
		return "", fmt.Errorf("normalize %q: %w", remote, err)
	}
	return normalized, nil
}

// copyToClipboard writes s to the OS clipboard, shelling out to the platform's
// clipboard utility (no cgo / no extra dependency).
func copyToClipboard(s string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("pbcopy")
	case "windows":
		c = exec.Command("clip")
	default: // linux / *bsd
		switch {
		case lookPath("wl-copy"):
			c = exec.Command("wl-copy")
		case lookPath("xclip"):
			c = exec.Command("xclip", "-selection", "clipboard")
		case lookPath("xsel"):
			c = exec.Command("xsel", "--clipboard", "--input")
		default:
			return fmt.Errorf("no clipboard utility found (install wl-clipboard, xclip, or xsel)")
		}
	}
	c.Stdin = strings.NewReader(s)
	return c.Run()
}

func lookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
