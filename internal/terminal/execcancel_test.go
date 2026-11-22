package terminal

import (
	"os"
	"strings"
	"testing"
)

// The stream must be able to end the command, not merely stop reading it.
//
// **What this guards is a privileged pod surviving a closed tab.** `runExec`
// stopped at `<-done`, waiting for the remote command; EOF on stdin only stops
// something that *reads* stdin, and a silent foreground command — `sleep 3600`, a
// quiet `helm upgrade --wait` — never does. Nothing else was going to unblock it,
// because `stream.go`'s own comment records that the WebSocket hijack leaves
// `r.Context()` uncanceled, and that is the context both callers pass.
//
// So closing the tab during such a command would leak the session slot, the session
// kubeconfig and — for a node shell — a `hostPID`/`hostIPC` pod holding the node's
// root filesystem at /host, which would run to its four-hour deadline with
// nothing on screen and nothing in the log — at the one call site with no
// heartbeat to save it.
//
// A source assertion, and the reason is worth stating: reproducing it needs a real
// API server holding a real exec stream open, which no unit test has. What a test
// *can* see is the two halves of the hazard — a context that is only as
// good as its caller's, and a wait with nothing to release it.
func TestTheExecStreamCanEndTheCommandItStarted(t *testing.T) {
	raw, err := os.ReadFile("exec.go")
	if err != nil {
		t.Fatalf("exec.go: %v", err)
	}
	src := string(raw)

	body := src[strings.Index(src, "func (t *Terminal) streamExec("):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	// It derives its own cancellation rather than trusting the one it is handed.
	if !strings.Contains(body, "context.WithCancel(ctx)") {
		t.Error("streamExec uses the context it was given. Both callers pass r.Context(), which " +
			"the WebSocket upgrade has already made permanent — so nothing cancels the " +
			"remote command when the browser goes")
	}
	// And it uses it before waiting, not merely defers it: a `defer cancel()`
	// alone fires *after* `<-done`, which is the line that never returns.
	cancelAt := strings.Index(body, "\tcancel()\n")
	// The *last* `<-done`: the first is inside the goroutine at the top of the
	// function and is not the one that blocks. Comparing against that one made this
	// test fail on correct code — a small lesson of its own about assertions over
	// source text.
	waitAt := strings.LastIndex(body, "<-done")
	if cancelAt < 0 {
		t.Fatal("nothing cancels before the wait, so `defer cancel()` fires after `<-done` — " +
			"which is precisely the line that never returns")
	}
	if waitAt < 0 {
		t.Fatal("the wait on `done` is gone; this test no longer describes the function")
	}
	if cancelAt > waitAt {
		t.Error("the cancel comes after the wait it is supposed to release")
	}

	// The stdin close and the size queue stay: they are how a command that *is*
	// reading stdin ends cleanly, and canceling is the harsher path.
	for _, want := range []string{"stdinWriter.Close()", "sizes.close()"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s is gone — a command that reads stdin should still get EOF and end "+
				"of its own accord rather than being canceled", want)
		}
	}
}
