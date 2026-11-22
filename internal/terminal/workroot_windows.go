//go:build windows

package terminal

import "os"

// fileOwner has no user id to read on Windows: ownership there is a security
// descriptor, and the work root sits in the per-account temp directory.
func fileOwner(os.FileInfo) (uid int, ok bool) { return 0, false }
