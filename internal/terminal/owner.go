package terminal

// Who owns a session directory, so a start-up sweep removes only what a
// process that is gone left behind.
//
// Not by age: a live session changes no direct entry after it opens, so its
// directory's mtime stops moving, and an age rule would take another running
// instance's shell for a dead one's. Each directory names its owner (process ID
// and start time, so a reused PID is not mistaken for it), and is removed only
// once that owner is provably gone.
//
// A PID means something only in its own namespace, and a temp directory can be
// shared across PID namespaces (a container with a bind-mounted /tmp beside a
// host process). So the record names its PID namespace (Linux), and a record
// from another is never judged. A live PID whose start time cannot be read is
// kept too. The failure is always a directory kept too long, never one removed
// from under a live session.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ownerFile is the name of the owner record inside a session directory.
const ownerFile = ".kubetower-owner"

// ownerlessTTL is how old a session directory with no owner record must be
// before the sweep removes it. Days, since without a record nothing proves its
// owner is gone.
const ownerlessTTL = 7 * 24 * time.Hour

// sessionDirName is exactly what os.MkdirTemp makes of the two session
// prefixes: the prefix and digits. It must not match `claude-workspace`, the
// assistant's fixed trusted workspaces, which a bare prefix match would.
var sessionDirName = regexp.MustCompile(`^(term|claude)-[0-9]+$`)

var (
	selfOnce  sync.Once
	selfOwner string
)

// self is this process's owner record: "<pid> <start> <pid namespace>", the
// last two possibly "-" where they cannot be read.
func self() string {
	selfOnce.Do(func() {
		pid := os.Getpid()
		_, start := processStart(pid)
		selfOwner = fmt.Sprintf("%d %s %s", pid, orDash(start), orDash(pidNamespace()))
	})
	return selfOwner
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fromDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// claimDir writes this process's owner record into a session directory it has
// just created, atomically (temp name, then rename), so a sweep in another
// process never reads half a record. If the write fails the directory is still
// used, and the sweep treats it as ownerless.
func claimDir(dir string) {
	tmp := filepath.Join(dir, ownerFile+".tmp")
	if err := os.WriteFile(tmp, []byte(self()+"\n"), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, ownerFile)); err != nil {
		_ = os.Remove(tmp)
	}
}

// ownerGone reports whether the process that owns dir is provably gone. known
// is false where dir carries no readable owner record.
func ownerGone(dir string) (gone, known bool) {
	raw, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if err != nil {
		return false, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return false, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return false, false
	}
	recorded, namespace := "", ""
	if len(fields) > 1 {
		recorded = fromDash(fields[1])
	}
	if len(fields) > 2 {
		namespace = fromDash(fields[2])
	}
	// Another PID namespace's PID is not ours to judge: kept.
	if namespace != pidNamespace() {
		return false, true
	}
	alive, start := processStart(pid)
	if !alive {
		return true, true
	}
	// The PID is in use. It is the owner unless both start times are known and
	// differ, which is a reused PID: anything less is not proof.
	return recorded != "" && start != "" && start != recorded, true
}
