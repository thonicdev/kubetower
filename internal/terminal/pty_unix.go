//go:build !windows

package terminal

// The Unix side of the seam, deliberately thin: exactly what a bare pty.Start
// and pty.Setsize do. Platform differences live on the Windows side.

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// unixPTY is `github.com/creack/pty` with the interface's names on it.
type unixPTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func startPTYSession(cmd *exec.Cmd, cols, rows uint16) (ptySession, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	// Ignored: an unsized terminal still works, and the browser sends its own size
	// within the second.
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	return &unixPTY{ptmx: ptmx, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.ptmx.Write(b) }
func (p *unixPTY) Close() error                { return p.ptmx.Close() }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// Kill ends the shell and everything it started.
//
// Signalling the shell alone would let a background job (`kubectl
// port-forward … &`, a forgotten `watch`) outlive the session, reparented to
// init, still holding the credentials it read and, for a forward, a local port.
//
// pty.Start makes the shell a session leader, and nothing started from it
// leaves that session without asking (setsid; nohup does not). So the session is
// killed: the shell's process group and every other group in the session, since
// a shell with job control gives each job its own group. Members are read before
// anything is signalled. A process that left the session on purpose is left
// alone.
func (p *unixPTY) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	leader := p.cmd.Process.Pid
	var groups []int
	if sid, err := unix.Getsid(leader); err == nil && sid == leader {
		groups = append(groups, leader)
		for _, pid := range sessionMembers(leader) {
			if pgid, err := syscall.Getpgid(pid); err == nil {
				groups = append(groups, pgid)
			}
		}
	}
	err := p.cmd.Process.Kill()
	own := syscall.Getpgrp()
	for _, pgid := range groups {
		// Never this process's own group, nor init's: neither can be in the shell's
		// session, and this makes sure.
		if pgid > 1 && pgid != own {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	return err
}

func (p *unixPTY) Wait() error { return p.cmd.Wait() }
