//go:build !windows

package transcript

import (
	"os"
	"syscall"
)

// inodeOf returns the file's inode, or 0 on a filesystem that does not expose
// one. A zero inode just means identity falls back to size.
func inodeOf(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
