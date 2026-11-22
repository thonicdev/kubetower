package terminal

// The one seam between this package and a pseudo-terminal.
//
// github.com/creack/pty has no Windows implementation (start_windows.go
// returns ErrUnsupported), so the dock's cluster shell and Claude session need a
// second one on Windows. This seam is the two verbs the callers use plus the
// process lifetime, which on Windows cannot be *exec.Cmd's: ConPTY needs
// CreateProcess with PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE in a STARTUPINFOEX,
// which os/exec cannot pass.
//
// Pod exec and the node shell do not come through here: their pseudo-terminal
// is allocated by the kubelet in the pod and the bytes arrive over client-go's
// remotecommand, so PTYSupported must not hide them.

import (
	"io"
	"os/exec"
)

// ptySession is a process with a pseudo-terminal in front of it.
//
// The three methods past the byte stream are what session.close needs, in its
// order: close the terminal, kill what is behind it, reap it. A ConPTY absorbs
// more of that in Close than a Unix pty, and the later calls are then no-ops,
// which is why their errors go unread.
type ptySession interface {
	// Read and Write are the terminal's own stream: what the shell printed and
	// what the operator typed. stream.copyFrom and stream.pump take an io.Reader and
	// an io.Writer, so nothing here needs an *os.File.
	io.ReadWriteCloser

	// Resize tells the terminal how large the browser's pane is, columns first.
	Resize(cols, rows uint16) error

	// Kill ends the process behind the terminal. Idempotent.
	Kill() error

	// Wait blocks until that process has been reaped; otherwise it lingers as a
	// zombie holding this session's descriptors.
	Wait() error
}

// startPTY starts cmd with a pseudo-terminal of the given size.
//
// cmd only carries Path, Args, Env and Dir: on Windows it is never started, as
// ConPTY needs a CreateProcess this package makes itself, so Stdin, SysProcAttr
// or ExtraFiles set on it are ignored there.
//
// The size is passed in because ConPTY takes it at creation; a shell started
// believing it has 0 columns draws its first prompt wrongly.
func startPTY(cmd *exec.Cmd, cols, rows uint16) (ptySession, error) {
	return startPTYSession(cmd, cols, rows)
}

// initialCols and initialRows are the size a session starts at, before the
// browser says what it has: 80x24, what a shell assumes anyway. xterm.js sends
// a resize as soon as it has measured the pane.
const (
	initialCols uint16 = 80
	initialRows uint16 = 24
)
