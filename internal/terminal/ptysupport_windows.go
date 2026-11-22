//go:build windows

package terminal

import "golang.org/x/sys/windows"

// ptySupported is whether this Windows machine has a pseudo-terminal.
//
// CreatePseudoConsole arrived in Windows 10 1809; on anything older it is not
// in kernel32, and x/sys/windows resolves it lazily, so the failure would come
// from inside a terminal the console had already offered. It is measured once by
// asking kernel32 for the export: a version check is only a proxy, and
// GetVersionEx lies to processes without a manifest.
var ptySupported = conptyAvailable()

// conptyAvailable reports whether kernel32 exports CreatePseudoConsole. Find
// resolves the address without calling anything, so the start-up probe starts
// no pseudo-console.
func conptyAvailable() bool {
	return windows.NewLazySystemDLL("kernel32.dll").NewProc("CreatePseudoConsole").Find() == nil
}
