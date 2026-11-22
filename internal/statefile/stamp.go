package statefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Which version of KubeTower last wrote this state directory.
//
// Binaries of different versions can share one state directory, and the
// failure is silent: every store loads defensively, turning a file it cannot
// parse into the defaults, so an older binary meeting a newer format replaces
// the operator's arrangement, and the newer binary then believes the empty one.
// So the directory carries a version stamp, and an older binary warns.
//
// Warn rather than refuse: a console that will not open cannot be used to find
// out what went wrong, and downgrading to check whether a defect is new is
// legitimate. The caller says it in the log and on the Infos tab in Settings.
//
// The comparison is by version, not by file time, which two machines, a
// restored backup or a moved clock make a lie.

// stampFile sits beside the rest of the state.
const stampFile = "version.json"

// Stamp is what the file holds.
type Stamp struct {
	// Version is what the binary that last wrote this directory called itself,
	// the same string `--version` prints.
	Version string `json:"version"`
}

// Older reports whether `running` is an earlier release than what last wrote the
// state directory, and returns that recorded version.
//
// The recorded version comes back whatever the answer, so a caller can name it;
// empty means no stamp yet, which is not a problem.
//
// A version this cannot parse, such as `dev` from an unstamped build, compares
// as not older, so a developer is not warned about their own build.
func Older(running string) (recorded string, older bool) {
	raw, err := os.ReadFile(filepath.Join(Dir(), stampFile))
	if err != nil {
		return "", false
	}
	var held Stamp
	if err := json.Unmarshal(raw, &held); err != nil {
		// Unreadable is treated as absent: refusing to start over a cosmetic
		// file is the trade this package avoids.
		return "", false
	}
	return held.Version, compare(running, held.Version) < 0
}

// Record writes this binary's version into the state directory.
//
// Failures are returned, and the caller logs them without stopping: the note is
// a diagnostic, not a lock.
func Record(running string) error {
	if strings.TrimSpace(running) == "" {
		return nil
	}
	raw, err := json.MarshalIndent(Stamp{Version: running}, "", "  ")
	if err != nil {
		return err
	}
	return Write(filepath.Join(Dir(), stampFile), raw)
}

// compare orders two dotted versions: -1 if a is earlier, 0 if equal, 1 if later.
//
// Anything it cannot read compares as equal, "no opinion": `dev`, a git
// describe or an empty string is not a release, and a warning about it could
// not be acted on.
func compare(a, b string) int {
	left, okA := parse(a)
	right, okB := parse(b)
	if !okA || !okB {
		return 0
	}
	for i := 0; i < 3; i++ {
		switch {
		case left[i] < right[i]:
			return -1
		case left[i] > right[i]:
			return 1
		}
	}
	return 0
}

// parse reads `major.minor.patch`, and refuses anything else rather than guessing.
func parse(version string) ([3]int, bool) {
	// `--version` follows the number with the revision and build date, and an
	// older stamp may carry an extra word. Only the first word counts, and only
	// if it is three numbers.
	version = strings.TrimSpace(version)
	if i := strings.IndexAny(version, " \t("); i > 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
