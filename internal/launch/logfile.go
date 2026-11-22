package launch

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// Where the log goes: to a file, always, and to stderr as well whenever that
// reaches anybody.
//
// The Windows binary launched from a shortcut has no console, and a macOS .app
// or a Linux .desktop launch has a stderr nobody reads (consoleAttached is true
// off Windows). Without a file their diagnostics would be unrecoverable.
//
// So every start that serves anything opens it, alongside stderr:
//
//   - Whether anything reads stderr cannot be answered (a terminal, `docker
//     compose logs`, a journal or a discard look alike); writing both makes a
//     wrong guess cost a duplicated line rather than the whole log.
//   - A closed terminal keeps no scrollback.
//   - In a container, `docker compose logs` still carries every line, and the
//     file survives the log driver's rotation and a crash loop.
//
// It costs up to 16 MiB in the state directory (logSizeCap, one previous
// generation).
//
// The first-run password must never reach this file: firstrun.go keeps only a
// bcrypt hash so that no file can give the password back, which is why the
// banner goes to stderr and a dialog, never through log.

// logSizeCap is when the current file is set aside for a fresh one, at start-up
// and while the process runs.
//
// One previous generation: a console open for a week must not fill a disk, and
// the run before the one that misbehaved is often the interesting one.
const logSizeCap = 8 << 20

// logToFile points the standard logger at a file beside the rest of the state and
// returns the path it chose, together with the open file.
//
// The handle is returned so a test can close it (an open file is undeletable on
// Windows); a running console holds it for its life.
//
// A failure returns an empty path rather than exiting: the log is how you find
// out what went wrong, not a thing that must go right.
func logToFile() (string, *logFile) {
	dir := statefile.Dir()
	if err := os.MkdirAll(dir, statefile.DirPerm); err != nil {
		return "", nil
	}
	path := filepath.Join(dir, logFileName)

	rotate(path)

	// Append, not truncate: a crash and a relaunch are two runs of one session.
	file, err := openLogFile(path, logSizeCap)
	if err != nil {
		return "", nil
	}

	// The file goes first: io.MultiWriter stops at the first writer that
	// errors, so a broken stderr (`kubetower | head`) would otherwise take the
	// file's copy of every later line with it.
	//
	// consoleAttached is false only on Windows without a console, where every
	// stderr write fails; elsewhere writing to an unread stderr costs nothing.
	var sink io.Writer = file
	if consoleAttached {
		sink = io.MultiWriter(file, os.Stderr)
	}
	log.SetOutput(sink)

	// Stamped, so runs accumulating in one file have visible boundaries.
	fmt.Fprintf(file, "\n=== %s ===\n", versionLine())
	return path, file
}

// rotate sets the current log aside once it is large enough.
//
// Failures are ignored on purpose: each leaves the existing file in place,
// trading a large file for no file.
func rotate(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < logSizeCap {
		return
	}
	_ = os.Rename(path, path+".1")
}

// logFile is the log's file, set aside whenever the next write would take it
// past its cap, so the bound holds while the process runs: on a server a flood
// of refused requests writes into the state volume the archive shares.
//
// Writes are serialized. A failed rotation never stops the log: a failed rename
// restarts the file from empty, and a failed reopen drops the file's copy until
// the next write, while still reporting success so the stderr behind it in the
// tee is not cut off.
type logFile struct {
	mu     sync.Mutex
	path   string
	cap    int64
	file   *os.File // nil while a reopen has failed
	size   int64
	closed bool
}

func openLogFile(path string, capBytes int64) (*logFile, error) {
	l := &logFile{path: path, cap: capBytes}
	if err := l.openLocked(os.O_APPEND); err != nil {
		return nil, err
	}
	return l, nil
}

// Write appends p, rotating first when p would take the file past its cap. A
// single write longer than the cap is clipped to it, so the bound holds for
// every write.
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if l.closed {
		return n, nil
	}
	if int64(len(p)) > l.cap {
		p = p[:l.cap]
	}
	if l.file == nil {
		_ = l.openLocked(os.O_APPEND)
	}
	if l.file != nil && l.size > 0 && l.size+int64(len(p)) > l.cap {
		l.rotateLocked()
	}
	if l.file == nil {
		return n, nil
	}
	written, err := l.file.Write(p)
	l.size += int64(written)
	if err != nil {
		return written, err
	}
	return n, nil
}

// rotateLocked closes the current file, sets it aside as the one previous
// generation, and opens a fresh one. Closed before the rename because Windows
// will not rename a file that is open.
func (l *logFile) rotateLocked() {
	_ = l.file.Close()
	l.file = nil
	mode := os.O_APPEND
	if os.Rename(l.path, l.path+".1") != nil {
		mode = os.O_TRUNC
	}
	_ = l.openLocked(mode)
}

func (l *logFile) openLocked(mode int) error {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|mode, statefile.FilePerm)
	if err != nil {
		return err
	}
	l.file, l.size = file, 0
	if info, err := file.Stat(); err == nil {
		l.size = info.Size()
	}
	return nil
}

// Close closes the file; anything written after it is dropped.
func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
