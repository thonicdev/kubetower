//go:build !linux && !darwin && !windows

package launch

import (
	"os"
	"time"
)

// accessTime is not read here: the page is kept for the whole wait.
func accessTime(os.FileInfo) (time.Time, bool) { return time.Time{}, false }
