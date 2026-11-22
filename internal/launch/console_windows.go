//go:build windows

package launch

import (
	"log"
	"os"
	"syscall"
)

// Getting the standard output back after `-H windowsgui` has taken it away.
//
// The Windows binary is a GUI program because the Start-menu shortcut opens a
// native window, and a console-subsystem program always gets a cmd window
// behind it. But a GUI program started from a terminal is detached from it, so
// `kubetower.exe --version` would print nothing. AttachConsole
// (ATTACH_PARENT_PROCESS) joins the console of whatever started this, if any:
// a terminal gets the output, the shortcut (whose parent is Explorer) gets none.
//
// The shell does not wait for a GUI program, so its next prompt may print
// before this process's output. Plain syscalls through DLLs every process
// already has loaded: no dependency.

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procAllocConsole     = kernel32.NewProc("AllocConsole")

	user32Console  = syscall.NewLazyDLL("user32.dll")
	procShowWindow = user32Console.NewProc("ShowWindow")
)

// swHide is ShowWindow's SW_HIDE.
const swHide = 0

// attachParentProcess is ATTACH_PARENT_PROCESS, defined as (DWORD)-1.
const attachParentProcess = ^uintptr(0)

// initConsole works out whether this process can talk to a person through
// stderr, attaching to the parent's console first if it has none of its own.
//
// The question is "is there a console", not "did AttachConsole succeed": under
// `go run` or a console-subsystem build the process already owns one, and
// AttachConsole then fails with ERROR_ACCESS_DENIED. Reading that as "no
// console" would hide the log from a developer watching it.
func initConsole() {
	if procGetConsoleWindow.Find() == nil {
		if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
			// Already has one: a console-subsystem build, or `go run`.
			consoleAttached = true
			return
		}
	}

	if ret, _, _ := procAttachConsole.Call(attachParentProcess); ret == 0 {
		// No parent console: Explorer, the Start menu or a service. Nobody
		// reads stderr.
		consoleAttached = false
		hiddenConsole()
		return
	}

	// Go captured the standard handles at initialization, and under the GUI
	// subsystem they point nowhere; CONOUT$ is the console's output device,
	// opened by name.
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		// Should not happen: treat it as no console, so the log goes to a file.
		consoleAttached = false
		return
	}

	// Only the handles with nowhere else to go: a redirection the parent set
	// up (`--version > version.txt`) passes a real handle the child inherits,
	// and must be kept. Each is tested on its own, since a shell may redirect
	// one and not the other.
	if !inherited(os.Stdout) {
		os.Stdout = out
	}
	if !inherited(os.Stderr) {
		os.Stderr = out
		// The standard logger captured os.Stderr at initialization, so it is
		// moved separately.
		log.SetOutput(out)
	}
	consoleAttached = true
}

// inherited says whether a standard handle came from the parent.
//
// Under the console subsystem, or when the parent redirected, GetStdHandle is
// a real handle. Otherwise (a double-click, a shortcut) it is NULL, and Go wraps
// that as a file whose writes vanish without error.
func inherited(f *os.File) bool {
	if f == nil {
		return false
	}
	handle := syscall.Handle(f.Fd())
	return handle != 0 && handle != syscall.InvalidHandle
}

// hiddenConsole gives this process a console nobody can see, which is the only
// way to stop child processes opening windows of their own.
//
// A console program started by a parent with no console gets a new, visible
// one. KubeTower reaches an EKS cluster by running `aws eks get-token`, a
// console program, through client-go's exec plugin, so a GUI-subsystem build
// would open a cmd window per credential fetch.
//
// CREATE_NO_WINDOW would have to be set by the caller of CreateProcess, which is
// client-go's plugin with no hook for SysProcAttr. A hidden console on the
// parent covers every child.
//
// The window exists between AllocConsole and ShowWindow, so a flicker at
// startup is possible. consoleAttached stays false: no person can read this
// console, so neither the log nor the first-run password goes to it.
func hiddenConsole() {
	if procAllocConsole.Find() != nil {
		return
	}
	if ret, _, _ := procAllocConsole.Call(); ret == 0 {
		// Already has one, or the station refuses: nothing more to try.
		return
	}
	if procGetConsoleWindow.Find() != nil || procShowWindow.Find() != nil {
		return
	}
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, swHide)
}
