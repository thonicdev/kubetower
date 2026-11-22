//go:build linux

package launch

import (
	"os"
	"syscall"
	"time"
)

// accessTime is when info's file was last read, as the file system keeps it.
func accessTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)), true
}
