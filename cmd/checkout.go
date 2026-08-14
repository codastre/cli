package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/codastre/cli/internal/checkouts"
	"github.com/codastre/cli/internal/git"
	"github.com/spf13/cobra"
)

// The checkout registry maps a normalized repo URL to a local clone. Two things
// depend on it, and both fail quietly without it: `query`/`graph` unmask masked
// path_tokens by enumerating a working tree, and `serve` hydrates QUERY results
// with source snippets only for repos it can find on disk. Until now the only
// way to get an entry was to happen to run `codastre query` from inside the
// repo, so an agent talking to the MCP server could search a repo for weeks and
// never register it — getting locators with no code, and no hint why.
//
// These subcommands make the registry explicit. `scan` is the one that matters:
// developers keep their clones under one root, so pointing at that root once
// registers everything, instead of visiting each repo in turn.

var checkoutCmd = &cobra.Command{
	Use:   "checkout",
	Short: "Manage the local checkout registry (enables snippet hydration and path unmasking)",
	Long: "Manage the map from repo URL to local clone.\n\n" +
		"Codastre needs a local clone to turn a search hit into actual source: `serve`\n" +
		"hydrates QUERY snippets from it, and `query`/`graph` unmask masked paths\n" +
		"against it. A repo that is indexed but not registered still matches searches,\n" +
		"but returns locations without code.\n\n" +
		"Most developers keep every clone under one root, so the fastest setup is:\n\n" +
		"  codastre checkout scan ~/src\n",
}

var checkoutListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List registered checkouts and flag stale entries",
	RunE:    runCheckoutList,
}

var checkoutAddCmd = &cobra.Command{
	Use:   "add [dir...]",
	Short: "Register one or more clones (default: the current repo)",
	Long: "Register each directory's repo. With no argument, registers the current\n" +
		"repository. Each directory must be a git clone with an 'origin' remote.",
	RunE: runCheckoutAdd,
}

var checkoutScanCmd = &cobra.Command{
	Use:   "scan [root]",
	Short: "Find every clone under a root directory and register them all",
	Long: "Walk a root directory and register every git clone found, so a whole\n" +
		"machine is set up in one command.\n\n" +
		"With no argument, scans the parent of the current repository — the common\n" +
		"case of one directory holding every clone. Descent stops at each repo found,\n" +
		"so nested clones, vendored trees, and linked worktrees are left alone.",
	RunE: runCheckoutScan,
}

var checkoutRemoveCmd = &cobra.Command{
	Use:     "remove <repo-url|dir>",
	Aliases: []string{"rm", "forget"},
	Short:   "Unregister a checkout by repo URL or directory",
	Args:    cobra.ExactArgs(1),
	RunE:    runCheckoutRemove,
}

var checkoutPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Drop entries whose directory is gone or no longer matches the repo",
	RunE:  runCheckoutPrune,
}

var (
	checkoutScanDepth  int
	checkoutScanDryRun bool
)

func init() {
	checkoutScanCmd.Flags().IntVar(&checkoutScanDepth, "depth", 2,
		"How many directory levels below root to search (2 covers root/org/repo layouts)")
	checkoutScanCmd.Flags().BoolVar(&checkoutScanDryRun, "dry-run", false,
		"Report what would be registered without writing the registry")

	checkoutCmd.AddCommand(checkoutListCmd, checkoutAddCmd, checkoutScanCmd,
		checkoutRemoveCmd, checkoutPruneCmd)
	rootCmd.AddCommand(checkoutCmd)
}

func runCheckoutList(cmd *cobra.Command, _ []string) error {
	entries := checkouts.All()
	out := cmd.OutOrStdout()
	if len(entries) == 0 {
		fmt.Fprint(out, "No checkouts registered.\n\n"+
			"Register every clone under one root:\n"+
			"  codastre checkout scan ~/src\n")
		return nil
	}

	urls := make([]string, 0, len(entries))
	for u := range entries {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	stale := 0
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, u := range urls {
		note := ""
		if !dirMatchesRepo(entries[u], u) {
			note = "\tstale — run `codastre checkout prune`"
			stale++
		}
		fmt.Fprintf(tw, "%s\t%s%s\n", u, displayPath(entries[u]), note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%s registered", countLabel(len(entries), "checkout"))
	if stale > 0 {
		fmt.Fprintf(out, ", %d stale", stale)
	}
	fmt.Fprintln(out, ".")
	return nil
}

func runCheckoutAdd(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		args = []string{"."}
	}
	out := cmd.OutOrStdout()
	added := 0
	for _, dir := range args {
		root, url, err := repoAt(dir)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "skipped %s: %v\n", dir, err)
			continue
		}
		if prev, ok := checkouts.Lookup(url); ok && prev == root {
			fmt.Fprintf(out, "already registered  %s -> %s\n", url, displayPath(root))
			continue
		}
		if err := checkouts.Remember(url, root); err != nil {
			return fmt.Errorf("register %s: %w", url, err)
		}
		fmt.Fprintf(out, "registered  %s -> %s\n", url, displayPath(root))
		added++
	}
	// A single directory that isn't a clone at all is very often the root that
	// *holds* the clones — the mistake worth catching, since `add ~/src` is the
	// natural first guess. A worktree or a remote-less repo is a different
	// error, so don't misdirect those toward scan.
	if added == 0 && len(args) == 1 {
		if _, _, err := repoAt(args[0]); errors.Is(err, errNotARepo) {
			fmt.Fprintf(out, "\nIf %s holds your clones rather than being one, scan it:\n"+
				"  codastre checkout scan %s\n", args[0], args[0])
		}
	}
	return nil
}

func runCheckoutScan(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	root, err := scanRoot(args)
	if err != nil {
		return err
	}
	if checkoutScanDepth < 1 {
		return fmt.Errorf("--depth must be at least 1")
	}

	fmt.Fprintf(out, "Scanning %s (depth %d)...\n\n", displayPath(root), checkoutScanDepth)
	found := findRepos(root, checkoutScanDepth)
	if len(found) == 0 {
		fmt.Fprintf(out, "No git clones found under %s.\n"+
			"Try a different root, or raise --depth if your clones are nested deeper.\n",
			displayPath(root))
		return nil
	}

	known := checkouts.All()
	seen := make(map[string]string, len(found))
	var newly, unchanged, dupes int

	// A repo with several clones on disk keeps the one already registered, as
	// long as that entry still resolves. Someone who pointed a repo at a
	// specific clone shouldn't have it moved just because another sorts first.
	for _, r := range found {
		if dir, ok := known[r.url]; ok && dirMatchesRepo(dir, r.url) {
			seen[r.url] = dir
		}
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, r := range found {
		if chosen, ok := seen[r.url]; ok {
			if chosen == r.dir {
				unchanged++
				continue
			}
			// Otherwise this is a second clone of a repo already accounted for:
			// report it so the choice is visible, and leave the registry alone.
			fmt.Fprintf(tw, "  duplicate\t%s\t%s (already using %s)\n",
				r.url, displayPath(r.dir), displayPath(chosen))
			dupes++
			continue
		}
		// First clone of this repo in the walk (sorted, so deterministic).
		seen[r.url] = r.dir
		verb := "  new"
		if _, ok := known[r.url]; ok {
			verb = "  moved"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", verb, r.url, displayPath(r.dir))
		newly++

		if !checkoutScanDryRun {
			if err := checkouts.Remember(r.url, r.dir); err != nil {
				return fmt.Errorf("register %s: %w", r.url, err)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%s found: %d to register, %d already current",
		countLabel(len(seen), "repo"), newly, unchanged)
	if dupes > 0 {
		fmt.Fprintf(out, ", %d duplicate clone(s) ignored", dupes)
	}
	fmt.Fprintln(out, ".")

	if checkoutScanDryRun {
		fmt.Fprintln(out, "\nDry run — nothing written. Re-run without --dry-run to apply.")
	} else if newly > 0 {
		fmt.Fprintln(out, "\nSnippet hydration and path unmasking now work for these repos.")
	}
	return nil
}

func runCheckoutRemove(cmd *cobra.Command, args []string) error {
	target := args[0]

	// Accept either form the user has in hand: the repo URL as `list` prints it,
	// or the directory they know it by.
	url := target
	if _, _, err := repoAt(target); err == nil {
		_, resolved, _ := repoAt(target)
		url = resolved
	} else if norm, nErr := git.Normalize(target); nErr == nil {
		url = norm
	}

	removed, err := checkouts.Forget(url)
	if err != nil {
		return fmt.Errorf("update registry: %w", err)
	}
	if !removed {
		fmt.Fprintf(cmd.OutOrStdout(), "%s was not registered — nothing to do.\n", url)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Unregistered %s\n", url)
	return nil
}

func runCheckoutPrune(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	entries := checkouts.All()

	urls := make([]string, 0, len(entries))
	for u := range entries {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	pruned := 0
	for _, u := range urls {
		if dirMatchesRepo(entries[u], u) {
			continue
		}
		reason := "origin no longer matches"
		if _, err := os.Stat(entries[u]); err != nil {
			reason = "directory is gone"
		}
		if _, err := checkouts.Forget(u); err != nil {
			return fmt.Errorf("update registry: %w", err)
		}
		fmt.Fprintf(out, "pruned  %s -> %s (%s)\n", u, displayPath(entries[u]), reason)
		pruned++
	}
	if pruned == 0 {
		fmt.Fprintf(out, "All %s valid — nothing to prune.\n", countLabel(len(entries), "checkout"))
		return nil
	}
	fmt.Fprintf(out, "\nPruned %d of %d.\n", pruned, len(entries))
	return nil
}

// scanRoot resolves the directory to scan: an explicit argument, else the parent
// of the current repository (the usual "all my clones live here" directory),
// else the CWD when not inside a repo.
func scanRoot(args []string) (string, error) {
	if len(args) > 0 {
		abs, err := filepath.Abs(expandHome(args[0]))
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("cannot scan %s: %w", args[0], err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("cannot scan %s: not a directory", args[0])
		}
		return abs, nil
	}
	if root, err := findGitRoot("."); err == nil {
		return filepath.Dir(root), nil
	}
	return filepath.Abs(".")
}

type foundRepo struct {
	dir string
	url string
}

// errNotARepo marks "this directory has no .git at all", distinguishing the
// common `add <root-of-clones>` mistake from a repo that exists but can't be
// registered (worktree, no remote).
var errNotARepo = errors.New("not a git repository")

// skipDirs are directories that never hold a developer's clones but often hold
// vendored ones, so descending into them wastes time and invites duplicates.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"build":        true,
	"dist":         true,
	"Pods":         true,
	".git":         true,
}

// findRepos returns every git clone at most depth levels below root, in sorted
// order, without descending into a clone once found. root itself is included
// when it is a clone, so scanning a single repo directory does the right thing.
func findRepos(root string, depth int) []foundRepo {
	var out []foundRepo
	if r, url, err := repoAt(root); err == nil {
		return []foundRepo{{dir: r, url: url}}
	}
	walk(root, depth, &out)
	return out
}

func walk(dir string, depth int, out *[]foundRepo) {
	if depth < 1 {
		return
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return // unreadable directory: not worth failing a whole scan over
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for _, e := range names {
		if !e.IsDir() || skipDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if r, url, err := repoAt(child); err == nil {
			*out = append(*out, foundRepo{dir: r, url: url})
			continue // a clone's contents are its own business
		}
		walk(child, depth-1, out)
	}
}

// repoAt reports the git root and normalized origin URL of dir. It requires dir
// itself to be the top of the clone — walking up would make a scan attribute
// every plain subdirectory to its enclosing repo. Linked worktrees (.git as a
// file) are rejected: they share an origin with the main clone, so registering
// one would shadow the real checkout.
func repoAt(dir string) (root, url string, err error) {
	abs, err := filepath.Abs(expandHome(dir))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(filepath.Join(abs, ".git"))
	if err != nil {
		return "", "", errNotARepo
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("linked worktree or submodule, not a main clone")
	}
	remote, err := getRemoteURL(abs)
	if err != nil {
		return "", "", fmt.Errorf("no usable git remote")
	}
	norm, err := git.Normalize(remote)
	if err != nil {
		return "", "", fmt.Errorf("unrecognized remote URL %q", remote)
	}
	return abs, norm, nil
}

// expandHome resolves a leading ~ so shell-quoted paths still work.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}

// displayPath shortens $HOME to ~ so listings stay narrow.
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return "~" + strings.TrimPrefix(p, home)
}

func countLabel(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// hintCheckouts nudges toward `checkout scan` when the registry is empty, and
// toward `checkout add` when the CWD repo specifically is missing. Written to
// the given writer by `login` and `doctor` so setup ends with search that
// returns code, not just file names.
func hintCheckouts(w io.Writer) {
	entries := checkouts.All()
	if len(entries) == 0 {
		root, err := findGitRoot(".")
		suggestion := "~/src"
		if err == nil {
			suggestion = displayPath(filepath.Dir(root))
		}
		fmt.Fprintf(w, "\nNo local checkouts registered yet, so search results will name files\n"+
			"without showing their code. Register every clone under one root:\n"+
			"  codastre checkout scan %s\n", suggestion)
		return
	}
	if _, url, err := repoAt("."); err == nil {
		if _, ok := entries[url]; !ok {
			fmt.Fprintf(w, "\nThis repo (%s) is not registered — its results will have no\n"+
				"snippets. Add it with:\n  codastre checkout add\n", url)
		}
	}
}
