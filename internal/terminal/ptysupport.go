package terminal

// Whether this build can open a pseudo-terminal on the machine this process
// runs on.
//
// The shell on the cluster (terminal.go) and Claude Code (claude.go) are a PTY
// with a local process behind it; on Windows they go through ConPTY
// (pty_windows.go). This lets the drawer leave off controls that would fail.
//
// Pod exec and the node shell are not PTYs on this machine (the kubelet
// allocates theirs), so internal/web/drawer.go does not hide them on this
// answer.
//
// It is a fact about the machine: false only where CreatePseudoConsole is
// missing, a Windows older than 10 1809 (see ptysupport_windows.go).
//
// The endpoints (`/kt-api/exec`, `/kt-api/claude`, the node shell) still
// refuse in words for anyone reaching them another way; this only stops the
// console offering what it cannot do.

// PTYSupported reports whether this build can start a pseudo-terminal here.
func PTYSupported() bool { return ptySupported }
