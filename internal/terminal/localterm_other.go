//go:build !windows

package terminal

// No desktop terminal anywhere but Windows, deliberately.
//
// On macOS and Linux the dock's terminal works and is the better answer: it
// survives navigation and needs nothing installed. Windows keeps the feature
// although pty_windows.go gives the dock a ConPTY: it is the answer on Windows
// older than 10 1809, where PTYSupported is false, and a real Windows Terminal
// tab has the operator's font, scrollback, copy-paste, profile and key bindings.
//
// In a Linux container, a "terminal on this machine" would be a window on a
// machine with no display: success reported and nothing seen.
//
// A desktop build for macOS or Linux would fill this file in (`open -a
// Terminal`, `x-terminal-emulator`), and first need to tell a desktop binary from
// a container, which Windows-only answers today.

func localTerminalSupported() bool { return false }

// launchTerminal is unreachable: OpenDesktopTerminal refuses on
// localTerminalSupported first. It returns an error rather than panicking, since
// nothing enforces that promise about call sites.
func launchTerminal(workdir, script string) error { return ErrLocalTerminalUnsupported }
