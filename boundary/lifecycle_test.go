package boundary_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/app"
	"go.uber.org/goleak"
	"golang.org/x/crypto/bcrypt"
)

// isolatedConsole prepares the environment app.Run reads - a state directory, a
// kubeconfig whose one context reaches nothing, a password - with every KT_
// variable of the machine running the test unset first, and returns the
// address it will listen on.
func isolatedConsole(t *testing.T) string {
	t.Helper()
	app.Reset()
	t.Cleanup(app.Reset)

	// Not t.TempDir, for the reason TestAnExtensionHandedToRunIsMountedBehindTheGate
	// gives: the log file inside it stays open on Windows.
	state, err := os.MkdirTemp("", "kubetower-lifecycle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	kubeconfig := filepath.Join(state, "kubeconfig")
	writeFile(t, kubeconfig, `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
users:
- name: nobody
  user:
    token: not-a-token
contexts:
- name: nowhere
  context: {cluster: nowhere, user: nobody}
current-context: nowhere
`)
	hash, err := bcrypt.GenerateFromPassword([]byte("lifecycle"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "KT_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	addr := freeAddress(t)
	t.Setenv("KT_IN_CONTAINER", "0")
	t.Setenv("KT_STATE_DIR", state)
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("KT_LISTEN", addr)
	t.Setenv("KT_PASSWORD_HASH", string(hash))
	t.Setenv("KT_SESSION_SECRET", strings.Repeat("s", 32))
	return addr
}

// An address somebody else holds is an error app.Run returns, with the
// sentence that names the way out - never the process ending under the binary
// that embedded the console.
func TestRunOnAPortInUseReturnsAnError(t *testing.T) {
	addr := isolatedConsole(t)
	held, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	done := make(chan error, 1)
	go func() {
		done <- app.Run(context.Background(), app.Options{Args: []string{"--no-window", "--unattended"}})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cannot listen on "+addr) {
			t.Errorf("app.Run on a port in use returned %v, want the sentence naming %s", err, addr)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("app.Run on a port in use did not return within 60s")
	}
}

// Started, answering, then stopped through its context: app.Run returns nil,
// and nothing it started is still running afterwards - the server, the update
// check, the archive, the forwards, the recorder, single sign-on's discovery.
// Goroutines that were already running before it started are not its own.
func TestRunLeavesNothingRunningOnceItReturns(t *testing.T) {
	before := goleak.IgnoreCurrent()
	addr := isolatedConsole(t)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{Args: []string{"--no-window", "--unattended"}})
	}()
	waitForConsole(t, "http://"+addr, done)

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app.Run returned %v after its context was canceled, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within 15s of its context being canceled")
	}

	goleak.VerifyNone(t, before,
		// The signal relay the os/signal package starts on the first Notify and
		// keeps for the life of the process; it is the runtime's, not Run's.
		goleak.IgnoreTopFunction("os/signal.signal_recv"),
		goleak.IgnoreAnyFunction("os/signal.loop"),
	)
}
