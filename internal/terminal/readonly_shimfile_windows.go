//go:build windows

package terminal

// The override file, on the platform that resolves through PATHEXT. `.cmd`,
// since an extensionless `kubectl` is not executable on Windows and the session
// would walk past the override (see readOnlyShimCmdTemplate). The name must be
// exactly `kubectl.cmd`: an override is the file a PATH search finds first
// under the name the operator types.

func shimFileName(binary string) string { return binary + ".cmd" }

func shimContent(binary, realPath string) string { return buildReadOnlyShimCmd(binary, realPath) }

// assistantShimContent is the same file for an assistant session, which also
// declines to read Secrets. See nosecrets.go.
func assistantShimContent(binary, realPath string, denyWrites bool) string {
	return buildAssistantShimCmd(binary, realPath, denyWrites)
}

// shimMode has no meaning on Windows, where PATHEXT decides. Kept at the POSIX
// value so the file is not group- or world-writable on a shared temp directory.
const shimMode = 0o755
