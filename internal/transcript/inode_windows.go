//go:build windows

package transcript

import "os"

// inodeOf has no Windows equivalent worth trusting, so file identity there is
// (path, size) alone: a truncated file still invalidates its offset.
func inodeOf(os.FileInfo) uint64 { return 0 }
