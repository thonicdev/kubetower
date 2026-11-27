package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// systemTool is the absolute path of a system program the updater starts
// (#178, C14.3 and C15.1): the package tools and pkexec, xdg-open, open,
// explorer.exe and powershell.exe. It is looked for only where the system
// keeps it, never through PATH: an update runs these with the person's
// session, some as root through pkexec, and a PATH entry the person or a
// shell profile put first would choose what runs.
func systemTool(name string) (string, error) {
	for _, candidate := range toolCandidates(runtime.GOOS, name, systemRoot()) {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not installed in the system's own folders: %w", name, os.ErrNotExist)
}

// toolCandidates are the places name may be on goos, in order. On Windows
// they are under systemRoot, the Windows folder (`C:\Windows` when it is
// unknown or not absolute); elsewhere they are the system's bin folders.
func toolCandidates(goos, name, systemRoot string) []string {
	if goos == "windows" {
		if !windowsAbs(systemRoot) {
			systemRoot = `C:\Windows`
		}
		switch name {
		case "powershell.exe":
			return []string{systemRoot + `\System32\WindowsPowerShell\v1.0\powershell.exe`}
		case "explorer.exe":
			return []string{systemRoot + `\explorer.exe`}
		}
		return []string{systemRoot + `\System32\` + name}
	}
	var out []string
	for _, dir := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// windowsAbs says whether p is an absolute Windows path, `C:\…` or `\\host\…`,
// whatever the OS reading it.
func windowsAbs(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return strings.HasPrefix(p, `\\`)
}
