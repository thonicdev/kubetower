//go:build windows

package launch

import (
	"os"
	"syscall"
	"time"
)

// accessTime is when info's file was last read, as NTFS keeps it - lazily,
// and not at all where last-access updates are turned off.
func accessTime(info os.FileInfo) (time.Time, bool) {
	d, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, d.LastAccessTime.Nanoseconds()), true
}
