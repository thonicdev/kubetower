//go:build !windows

package terminal

import (
	"os"
	"syscall"
)

// fileOwner reads the user id that owns info's file. ok is false where the
// platform's stat carries none.
func fileOwner(info os.FileInfo) (uid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
