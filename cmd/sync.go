package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Watch HEAD and sync on changes (--once for a single eager sync then exit)",
	RunE:  runSync,
}

var syncOnce bool

func init() {
	syncCmd.Flags().BoolVar(&syncOnce, "once", false, "Single sync then exit")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	if syncOnce {
		return doSync()
	}
	return watchAndSync()
}

// watchAndSync watches .git/HEAD and related ref files; debounces 5 s before calling doSync.
// Falls back to 10 s mtime polling if fsnotify is unavailable (impl-spec §2.3).
func watchAndSync() error {
	repoRoot, err := findGitRoot(".")
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return pollSync(repoRoot)
	}
	defer watcher.Close()

	watchTargets := []string{
		filepath.Join(repoRoot, ".git", "HEAD"),
		filepath.Join(repoRoot, ".git", "refs", "heads"),
		filepath.Join(repoRoot, ".git", "packed-refs"),
	}
	for _, p := range watchTargets {
		_ = watcher.Add(p) // ignore errors for paths that may not exist yet
	}

	var debounce <-chan time.Time
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			debounce = time.After(5 * time.Second)
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watch error: %v\n", watchErr)
		case <-debounce:
			if err := doSync(); err != nil {
				fmt.Fprintf(os.Stderr, "sync error: %v\n", err)
			}
			debounce = nil
		}
	}
}

// pollSync is the 10 s mtime polling fallback (impl-spec §2.3).
func pollSync(repoRoot string) error {
	fmt.Fprintln(os.Stderr, "warning: fsnotify unavailable, falling back to 10 s polling")
	headPath := filepath.Join(repoRoot, ".git", "HEAD")
	var lastMtime time.Time
	for {
		stat, err := os.Stat(headPath)
		if err == nil {
			mt := stat.ModTime()
			if !lastMtime.IsZero() && mt.After(lastMtime) {
				if err := doSync(); err != nil {
					fmt.Fprintf(os.Stderr, "sync error: %v\n", err)
				}
			}
			lastMtime = mt
		}
		time.Sleep(10 * time.Second)
	}
}

// doSync performs one SYNC round-trip with the server.
func doSync() error {
	// TODO: full SYNC implementation (diff compute → mask → POST MCP SYNC)
	fmt.Println("sync: triggered")
	return nil
}

// findGitRoot walks up from start until it finds a .git directory.
func findGitRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a git repository")
		}
		dir = parent
	}
}
