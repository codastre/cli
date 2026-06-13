package git

import (
	"fmt"
	"net/url"
	"strings"
)

// defaultPorts maps scheme to its default port string.
var defaultPorts = map[string]string{
	"https": "443",
	"http":  "80",
	"ssh":   "22",
	"git":   "9418",
}

// Normalize converts any supported remote URL form to "host/owner/repo" (impl-spec §2.4).
// Rules: scp-like git@host:path, https/ssh/git schemes; lowercase host only; strip default
// ports (443, 22, 9418); strip one trailing .git and trailing /; percent-decode path.
func Normalize(rawURL string) (string, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}

	// SCP-like: [user@]host:path — no "://" present, contains ":"
	if !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(raw, ":")
		if !ok {
			return "", fmt.Errorf("unrecognized URL: %s", rawURL)
		}
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		host = strings.ToLower(host)
		path = stripGitSuffix(strings.TrimSuffix(path, "/"))
		return host + "/" + path, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}

	// Lowercase host only; preserve path case.
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && port != defaultPorts[u.Scheme] {
		host = host + ":" + port
	}

	// u.Path is already percent-decoded by url.Parse.
	path := strings.TrimPrefix(u.Path, "/")
	path = stripGitSuffix(strings.TrimSuffix(path, "/"))

	return host + "/" + path, nil
}

func stripGitSuffix(s string) string {
	if strings.HasSuffix(s, ".git") {
		return s[:len(s)-4]
	}
	return s
}
