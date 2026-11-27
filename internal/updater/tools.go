package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// systemTool is the absolute path of a system program the updater starts
// (#178, C14.3 and C15.1): the package tools and pkexec, xdg-open, open,
// explorer.exe and powershell.exe. It is looked for only where the system
// keeps it, never through PATH: an update runs these with the person's
// session, some as root through pkexec, and a PATH entry the person or a
// shell profile put first would choose what runs.
func systemTool(name string) (string, error) {
	for _, candidate := range toolCandidates(runtime.GOOS, name, os.Getenv("SystemRoot")) {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not installed in the system's own folders: %w", name, os.ErrNotExist)
}

// toolCandidates are the places name may be on goos, in order. On Windows
// they are under systemRoot (`%SystemRoot%`, `C:\Windows` when it is unset);
// elsewhere they are the system's bin folders.
func toolCandidates(goos, name, systemRoot string) []string {
	if goos == "windows" {
		if systemRoot == "" {
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
