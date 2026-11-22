package terminal

import (
	"os"
	"strings"
	"testing"
)

// Nothing on the boot path may run an unbounded subprocess.
//
// `claudeVersion` runs `claude --version` from the code that writes the Claude
// configuration — which runs before the listener exists and before
// `/kt-healthz` can answer. Without a deadline, a `claude` binary that waits on
// a terminal it does not have, or on a network call it cannot finish, would
// hang startup with **no log line and no health endpoint**.
//
// `skew.go` guards the identical pattern with the identical deadline and
// explains why there too. What the timeout costs when it fires is one key in a
// config file — exactly what an error already costs — so there is no
// trade-off to weigh.
//
// A source assertion, and the reason is the same one `execcancel_test.go` gives:
// reproducing it needs a binary that hangs, which no unit test has. What a test
// *can* see is whether the call is bounded at all.
func TestNothingOnTheBootPathRunsAnUnboundedSubprocess(t *testing.T) {
	raw, err := os.ReadFile("claude.go")
	if err != nil {
		t.Fatalf("claude.go: %v", err)
	}
	src := string(raw)

	body := src[strings.Index(src, "func claudeVersion("):]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	if body == "" {
		t.Fatal("claudeVersion is gone; this test no longer describes the boot path")
	}

	if strings.Contains(body, "exec.Command(") {
		t.Error("claudeVersion uses exec.Command, which has no deadline. This runs before " +
			"the listener and before /kt-healthz exists, so a binary that does not return " +
			"hangs startup with nothing anywhere saying so")
	}
	if !strings.Contains(body, "exec.CommandContext(") {
		t.Error("claudeVersion no longer runs its subprocess under a context")
	}
	if !strings.Contains(body, "context.WithTimeout(") {
		t.Error("claudeVersion derives no deadline, so its context is only as good as the " +
			"one it was handed — and on the boot path that is context.Background()")
	}
}
