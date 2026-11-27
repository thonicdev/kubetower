package updater

import "golang.org/x/sys/windows"

// systemRoot is the Windows folder as the system gives it, never
// `%SystemRoot%`: a per-user environment value would choose the folder the
// update's powershell and explorer are taken from, as PATH did (#178).
func systemRoot() string {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return ""
	}
	return dir
}
