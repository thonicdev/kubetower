//go:build !windows

package updater

// systemRoot is only read on Windows.
func systemRoot() string { return "" }
