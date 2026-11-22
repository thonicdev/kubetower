//go:build windows

package terminal

// A pseudo-terminal on Windows, over ConPTY, through golang.org/x/sys/windows
// directly rather than a third-party wrapper: x/sys is already a direct
// requirement of this module and declares CreatePseudoConsole,
// ResizePseudoConsole, ClosePseudoConsole, PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
// StartupInfoEx, ComposeCommandLine and the attribute list, so only the
// capability probe in ptysupport_windows.go needs a LazySystemDLL.
//
// # Standard handles
//
// A redirected standard handle in the parent shadows the pseudoconsole in the
// child, silently. The documented shape (Microsoft's sample, Windows Terminal's
// ConptyConnection) leaves STARTF_USESTDHANDLES unset, which works when the
// parent's handles are a console or NULL. This process's stderr can be a pipe
// (`docker compose logs`), a file (internal/launch/logfile.go) or NUL, and with
// a pipe CreateProcess copies the handle values into the child even with
// bInheritHandles FALSE, where they name nothing. Measured with a probe asking
// the child:
//
//	no STARTF_USESTDHANDLES     redirected=True   width-err=invalid handle   title=powershell.exe
//	STARTF_USESTDHANDLES, NULL  redirected=False  width=100                 title=powershell.exe
//
// In the first row the child is attached to the pseudoconsole (it reads the
// title) yet writes to a handle nobody holds: the pane shows conhost's 16-byte
// preamble and then nothing, with no error anywhere.
//
// So STARTF_USESTDHANDLES is set with all three handles NULL, meaning "do not
// copy mine". Those lines look removable; `go test` runs with its handles on a
// pipe, the failing shape, so TestTheWindowsTerminalCarriesTheShellsOutput fails
// without them ("the shell's output never reached the terminal").
//
// # Lifetime
//
// On Unix the shell exiting ends the stream: the read fails, and serveShell
// removes the directory holding live credentials. conhost instead holds the
// output pipe open after its client has gone (measured: after `exit` the pipe
// stayed open until ClosePseudoConsole), so this type watches the process and
// tears the pseudoconsole down when it exits, producing the EOF the rest of the
// package relies on.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winPTY is one ConPTY and the process attached to it.
type winPTY struct {
	// out is conhost's rendered output: everything the pane draws.
	out *os.File
	// in is the console's input. Written to, never read.
	in *os.File

	// mu guards hpc, proc and thread, each closed exactly once, from either the
	// waiter or Close.
	mu     sync.Mutex
	hpc    windows.Handle
	proc   windows.Handle
	thread windows.Handle

	// exited is closed once the process has been reaped and the pseudoconsole torn
	// down, in that order: the teardown ends the output stream.
	exited chan struct{}

	closeOnce sync.Once
}

func startPTYSession(cmd *exec.Cmd, cols, rows uint16) (ptySession, error) {
	if !ptySupported {
		return nil, errors.New("this Windows build has no ConPTY: " +
			"CreatePseudoConsole is missing from kernel32, which means a Windows older than 10 1809")
	}

	// Two pipes: conhost keeps the input's read end and the output's write end;
	// we keep the other two.
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("terminal input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, fmt.Errorf("terminal output pipe: %w", err)
	}

	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(
		windows.Coord{X: int16(cols), Y: int16(rows)}, inRead, outWrite, 0, &hpc); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("creating a pseudo-console: %w", err)
	}

	// The attribute list attaches the child to the console. Its value is the
	// handle itself, not a pointer to it, as documented and as Microsoft's sample
	// passes it.
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("process attribute list: %w", err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		consoleAttributeValue(hpc), unsafe.Sizeof(hpc)); err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("attaching the pseudo-console: %w", err)
	}

	var siEx windows.StartupInfoEx
	siEx.ProcThreadAttributeList = attrs.List()
	// sizeof(STARTUPINFOEXW), not STARTUPINFOW: EXTENDED_STARTUPINFO_PRESENT
	// promises the extended form, and a wrong size is silently ignored.
	siEx.StartupInfo.Cb = uint32(unsafe.Sizeof(siEx))
	// See the file comment: stops this process's redirected handles being copied
	// into the child over the console.
	siEx.StartupInfo.Flags |= windows.STARTF_USESTDHANDLES
	siEx.StartupInfo.StdInput = 0
	siEx.StartupInfo.StdOutput = 0
	siEx.StartupInfo.StdErr = 0

	// argv[0] is cmd.Path, not cmd.Args[0]: exec.Command resolves the first
	// through LookPath, while Options.Shell may hold a bare name. With a nil
	// lpApplicationName, CreateProcess takes the executable from the command line,
	// so the line must carry the resolved path.
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("building the command line: %w", err)
	}
	var dir *uint16
	if cmd.Dir != "" {
		if dir, err = windows.UTF16PtrFromString(cmd.Dir); err != nil {
			windows.ClosePseudoConsole(hpc)
			windows.CloseHandle(inRead)
			windows.CloseHandle(inWrite)
			windows.CloseHandle(outRead)
			windows.CloseHandle(outWrite)
			return nil, fmt.Errorf("session directory: %w", err)
		}
	}
	block := environmentBlock(cmd.Env)

	var pi windows.ProcessInformation
	err = windows.CreateProcess(nil, cmdline, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		&block[0], dir, &siEx.StartupInfo, &pi)
	// Closed after CreateProcess, as Microsoft's sample says: conhost has
	// duplicated both, and closing earlier is reported to leave the console
	// half-built.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return nil, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}

	p := &winPTY{
		out:    os.NewFile(uintptr(outRead), "conpty-out"),
		in:     os.NewFile(uintptr(inWrite), "conpty-in"),
		hpc:    hpc,
		proc:   pi.Process,
		thread: pi.Thread,
		exited: make(chan struct{}),
	}
	go p.reap()
	return p, nil
}

// consoleAttributeValue is the HPCON as UpdateProcThreadAttribute wants it:
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE takes the handle by value in its PVOID
// parameter, and HPCON is itself a pointer-sized handle.
//
// Through the address of a local rather than unsafe.Pointer(hpc), which `go
// vet`'s unsafeptr check flags. A kernel handle is never a Go heap address, so
// there is nothing for the collector to track.
func consoleAttributeValue(h windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&h))
}

// reap waits for the shell, then ends the stream. conhost holds the output pipe
// open after its client has gone, so nothing downstream would see EOF;
// ClosePseudoConsole closes it, and only this knows the process has exited.
func (p *winPTY) reap() {
	_, _ = windows.WaitForSingleObject(p.proc, windows.INFINITE)

	p.mu.Lock()
	if p.thread != 0 {
		windows.CloseHandle(p.thread)
		p.thread = 0
	}
	if p.proc != 0 {
		windows.CloseHandle(p.proc)
		p.proc = 0
	}
	if p.hpc != 0 {
		windows.ClosePseudoConsole(p.hpc)
		p.hpc = 0
	}
	p.mu.Unlock()

	close(p.exited)
}

func (p *winPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *winPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *winPTY) Resize(cols, rows uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hpc == 0 {
		return errors.New("the terminal is closed")
	}
	return windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (p *winPTY) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == 0 {
		// Already reaped: session.close calls this after Close, when the shell is
		// normally gone.
		return nil
	}
	return windows.TerminateProcess(p.proc, 1)
}

// closeGrace bounds how long Close waits for the shell to go, reached only
// when TerminateProcess did not work (a process stuck in a kernel-mode wait).
// Close runs on the request goroutine, and hanging would hold the handler, a
// session slot and the session directory with its credentials.
const closeGrace = 5 * time.Second

func (p *winPTY) Close() error {
	p.closeOnce.Do(func() {
		// Input first: a shell reading its console gets EOF and exits on its own,
		// running whatever it does on exit.
		_ = p.in.Close()
		_ = p.Kill()
		select {
		case <-p.exited:
		case <-time.After(closeGrace):
			// reap never finished. Tear the console down anyway, so the reader is not left
			// blocked on a pipe conhost still holds.
			p.mu.Lock()
			if p.hpc != 0 {
				windows.ClosePseudoConsole(p.hpc)
				p.hpc = 0
			}
			p.mu.Unlock()
		}
		_ = p.out.Close()
	})
	return nil
}

func (p *winPTY) Wait() error {
	select {
	case <-p.exited:
	case <-time.After(closeGrace):
	}
	return nil
}

// environmentBlock renders an environment for CreateProcess.
//
// Doubly NUL-terminated UTF-16, as CREATE_UNICODE_ENVIRONMENT means, and never
// empty: a zero-length block is not "no block", and &block[0] on an empty slice
// panics.
//
// De-duplicated, keeping the last of each name, case-insensitively.
// sessionEnv appends `PATH=` after os.Environ(), which already has one, and on
// the read-only path that value is the guardrail. A POSIX shell keeps the last
// entry, but GetEnvironmentVariable returns the first match, so without this
// the child would see the original Path and no shims. Case-insensitively because
// Windows spells it `Path` and this package writes `PATH=`.
func environmentBlock(env []string) []uint16 {
	// `=C:=C:\somewhere` entries are real (Windows keeps a per-drive current
	// directory under an empty name); keyed on the whole entry so they do not
	// collide.
	last := make(map[string]int, len(env))
	for i, entry := range env {
		if strings.IndexByte(entry, 0) >= 0 {
			// A NUL would end the block there and drop everything after it.
			continue
		}
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			last[strings.ToUpper(entry)] = i
			continue
		}
		last[strings.ToUpper(name)] = i
	}

	var block []uint16
	for i, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		key := strings.ToUpper(entry)
		if ok && name != "" {
			key = strings.ToUpper(name)
		}
		if last[key] != i {
			continue
		}
		u, err := windows.UTF16FromString(entry)
		if err != nil {
			continue
		}
		block = append(block, u...)
	}
	// The terminating empty string.
	return append(block, 0)
}
