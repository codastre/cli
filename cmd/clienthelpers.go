package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/mcpclient"
)

// resolveAPIKey returns the API key for serverURL using the precedence:
//
//	--key flag  >  $CODASTRE_API_KEY  >  OS keychain (or file fallback)
//
// warn is a non-fatal message (e.g. file-fallback notice) to print to stderr;
// it is empty when nothing noteworthy happened.
func resolveAPIKey(serverURL, keyFlag string) (apiKey, warn string, err error) {
	if keyFlag != "" {
		return keyFlag, "", nil
	}
	if v := os.Getenv("CODASTRE_API_KEY"); v != "" {
		return v, "", nil
	}

	store, isFallback, err := keychain.Open()
	if err != nil {
		return "", "", fmt.Errorf("open keychain: %w", err)
	}
	host := extractHost(serverURL)
	key, err := store.GetAPIKey(host)
	if err != nil {
		return "", "", fmt.Errorf("no API key for %s — run `codastre login`, set $CODASTRE_API_KEY, or pass --key", host)
	}
	if isFallback {
		warn = "OS keychain unavailable; using file storage (~/.config/codastre/keys)"
	}
	return key, warn, nil
}

// autoTargets derives the canonical repo_url candidates ("host/owner/repo") for
// the current working directory's git repository — one per configured remote,
// in priority order (origin, upstream, then the rest), normalized and deduped.
// It returns nil when the CWD is not inside a git repo or has no usable remote —
// the caller then falls back to a federated search across all visible repos.
//
// A clone often carries several remotes that mirror the same repo (e.g. origin
// and upstream); only one is registered with the server. The first candidate is
// the primary target; the rest are tried in turn when the server reports
// REPO_NOT_INDEXED for the primary.
func autoTargets() []string {
	root, err := findGitRoot(".")
	if err != nil {
		return nil
	}
	remotes, err := remoteURLs(root)
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, remote := range remotes {
		normalized, err := git.Normalize(remote)
		if err != nil || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

// target captures the resolved search scope (docs/mcp-index-free-access.md).
type target struct {
	indexID   string // a single index by id
	repoURL   string // a specific repo by URL
	federated bool   // all visible repos

	// fallbacks holds additional repo_url candidates (auto mode only). When the
	// server reports REPO_NOT_INDEXED for repoURL, callWithRepoFallback retries
	// these in order before surfacing the error.
	fallbacks []string
}

// describe returns a one-line summary of the resolved mode for human output.
func (t target) describe() string {
	switch {
	case t.indexID != "":
		return "index " + t.indexID
	case t.repoURL != "":
		return "repo " + t.repoURL
	default:
		return "federated (all visible repos)"
	}
}

// apply writes the target's selector into an MCP tool argument map. A federated
// federated target writes nothing (omitting both index_id and repo_url makes
// the server search every visible repo).
func (t target) apply(args map[string]any) {
	switch {
	case t.indexID != "":
		args["index_id"] = t.indexID
	case t.repoURL != "":
		args["repo_url"] = t.repoURL
	}
}

// resolveTarget applies the mode-selection precedence shared by `query` and
// `graph`:
//
//	--index-id  >  --repo-url  >  --all  >  auto (CWD repo, else federated)
//
// Supplying more than one of --index-id / --repo-url / --all is a conflict and
// errors before any network call.
func resolveTarget(indexID, repoURL string, all bool) (target, error) {
	n := 0
	for _, set := range []bool{indexID != "", repoURL != "", all} {
		if set {
			n++
		}
	}
	if n > 1 {
		return target{}, fmt.Errorf("choose at most one of --index-id, --repo-url, --all")
	}

	switch {
	case indexID != "":
		return target{indexID: indexID}, nil
	case repoURL != "":
		// Accept either a clonable URL (https/ssh/scp) or the already-canonical
		// "host/owner/repo" form the server and federated output display. If
		// local normalization can't parse it, pass it through — the server
		// normalizes repo_url again when it resolves the repo.
		if norm, err := git.Normalize(repoURL); err == nil {
			return target{repoURL: norm}, nil
		}
		return target{repoURL: strings.TrimSpace(repoURL)}, nil
	case all:
		return target{federated: true}, nil
	default:
		// No explicit target: resolve from the CWD repo's remotes. The first is the
		// primary; the rest are retried only if the primary is not indexed. An
		// explicit --repo-url never gets this fallback — it targets exactly one repo.
		if cands := autoTargets(); len(cands) > 0 {
			return target{repoURL: cands[0], fallbacks: cands[1:]}, nil
		}
		return target{federated: true}, nil
	}
}

// callWithRepoFallback issues an MCP tool call for tgt and returns the payload
// together with the target that actually resolved. In auto multi-remote mode
// (tgt.fallbacks non-empty) it retries each remaining repo_url candidate when the
// server reports REPO_NOT_INDEXED, so a CWD whose `origin` is unregistered still
// resolves through another remote (e.g. upstream). Any other error, or an
// exhausted candidate list, is returned as-is from the last attempt.
func callWithRepoFallback(
	ctx context.Context,
	cfg mcpclient.Config,
	tool string,
	toolArgs map[string]any,
	tgt target,
) (json.RawMessage, target, error) {
	cur := tgt
	remaining := tgt.fallbacks
	for {
		cur.apply(toolArgs)
		payload, err := mcpclient.Call(ctx, cfg, tool, toolArgs)
		if err == nil {
			return payload, cur, nil
		}
		var te *mcpclient.ToolError
		if errors.As(err, &te) && te.Code == "REPO_NOT_INDEXED" && len(remaining) > 0 {
			cur = target{repoURL: remaining[0]}
			remaining = remaining[1:]
			continue
		}
		return nil, cur, err
	}
}
