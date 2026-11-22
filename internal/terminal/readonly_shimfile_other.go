//go:build !windows

package terminal

// The override file, on a platform that runs `#!`. No extension: a POSIX shell
// resolves the bare name, and `kubectl.sh` is not what `kubectl` resolves to.

func shimFileName(binary string) string { return binary }

func shimContent(binary, realPath string) string { return buildReadOnlyShim(binary, realPath) }

// assistantShimContent is the same file for an assistant session, which also
// declines to read Secrets. See nosecrets.go.
func assistantShimContent(binary, realPath string, denyWrites bool) string {
	return buildAssistantShim(binary, realPath, denyWrites)
}

// shimMode is the permission the file is written with. Executable, because a
// POSIX PATH search skips an entry failing X_OK (fence.go catches a noexec
// mount that way).
const shimMode = 0o755
