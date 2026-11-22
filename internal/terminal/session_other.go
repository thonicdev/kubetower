//go:build !windows && !linux && !darwin

package terminal

// sessionMembers answers nothing where processes cannot be listed: Kill then
// reaches only the shell's own process group, which holds every job of a shell
// without job control.
func sessionMembers(int) []int { return nil }
