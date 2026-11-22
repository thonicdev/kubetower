//go:build !windows

package launch

// showFirstRunPassword has no dialog to draw off Windows, and returns false.
//
// Every other build has an ordinary stderr, where announce prints the banner;
// a dialog would need a toolkit for a problem these platforms do not have. A
// stub claiming success would turn announce's refusal into a lock-out. Since a
// Finder or .desktop launch's stderr goes nowhere, the one-time file
// (passwordonce.go) is the channel that does not depend on it.
func showFirstRunPassword(drawnPassword) bool { return false }

// showMessage has no box here either: report has already written the text to
// stderr, where it can be read and selected.
func showMessage(title, body string) bool { return false }
