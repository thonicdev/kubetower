//go:build !windows

package launch

// initConsole has nothing to do off Windows, where there is no GUI subsystem to
// be detached by: stderr is whatever the terminal or the service unit gave.
// Whether anyone reads it cannot be told (see console.go), so logfile.go writes
// the log unconditionally.
func initConsole() {}
