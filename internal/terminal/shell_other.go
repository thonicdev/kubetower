//go:build !windows

package terminal

// resolveDefaultShell is `/bin/bash --norc --noprofile -i` on every platform
// creack/pty supports; defaultSessionShell in terminal.go says why the two flags
// matter.
//
// macOS stays bash although zsh is its default shell: `/bin/bash` there is
// 3.2, and what this package hands a session is bash syntax (those flags, and a
// `PS1` with `\[\033[36m\]` escapes and `\w`). zsh's prompt language is
// `%F{cyan}` and `%~` and its no-rc flags are `-d -f`, a fourth dialect on a
// platform this project is not developed on. This is not the operator's login
// shell but one KubeTower opens with a narrowed KUBECONFIG, no init files and its
// own prompt. Options.Shell is exported for anyone who wants zsh, and fence.go
// measures the result.
func resolveDefaultShell() []string {
	return []string{"/bin/bash", "--norc", "--noprofile", "-i"}
}
