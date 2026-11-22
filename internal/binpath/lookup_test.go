package binpath

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// The fact this whole package rests on, asserted rather than reasoned about.
//
// **A `PATH` written into a kubeconfig exec block's `Env` does not help client-go
// find the plugin.** That is not obvious, it is the opposite of what the
// surrounding code makes it look like — `internal/cluster/awscache.go` and
// `internal/terminal/awshome.go` both put `HOME` there and it works — and it is
// why this package changes `os.Setenv("PATH", …)` instead. Done the other way,
// the fix is a silent no-op: the clusters keep failing while the log says the
// fix was applied.
//
// The mechanism: client-go builds the plugin with `exec.Command(a.cmd, …)` and
// assigns `cmd.Env` afterwards (client-go 0.37.0,
// plugin/pkg/client/auth/exec/exec.go:458). Go's `exec.Command` resolves a bare
// name through `LookPath` against **the calling process's** environment at
// construction time; `cmd.Env` is only what the child is handed once it has
// already been located.
//
// The assertion is "found" versus "not found" and never "the request succeeded",
// deliberately: a plugin that runs still has to emit a valid `ExecCredential`,
// and making a portable fake that does — a shell script here, a `.bat` on the
// Windows host — would test the fixture more than the mechanism. Once client-go
// stops saying "not found" it has found the binary, which is the whole question.
func TestOnlyTheProcessPathMakesClientGoFindTheCredentialPlugin(t *testing.T) {
	dir := t.TempDir()
	name := "kt-fake-credential-plugin"
	file := name
	if runtime.GOOS == "windows" {
		// LookPath consults PATHEXT there, so the file on disk needs one of its
		// extensions while the kubeconfig still names the bare command.
		file += ".bat"
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"major":"1","minor":"30"}`))
	}))
	defer srv.Close()

	// probe asks a real clientset for the server version through an exec block
	// naming `name`, and reports whether client-go could find the binary.
	probe := func(execEnv []clientcmdapi.ExecEnvVar) (found bool, message string) {
		cfg := &rest.Config{Host: srv.URL}
		cfg.ExecProvider = &clientcmdapi.ExecConfig{
			Command:         name,
			APIVersion:      "client.authentication.k8s.io/v1beta1",
			Args:            []string{"eks", "get-token"},
			Env:             execEnv,
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		}
		clientset, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			t.Fatalf("building a client: %v", err)
		}
		if _, err = clientset.Discovery().ServerVersion(); err == nil {
			return true, "the request succeeded"
		}
		return !strings.Contains(err.Error(), "not found"), err.Error()
	}

	// A PATH the plugin is not on, so nothing below can pass by accident.
	t.Setenv("PATH", t.TempDir())

	// One: the exec block's own environment names the directory. This is the
	// design that would have shipped, and it changes nothing.
	found, message := probe([]clientcmdapi.ExecEnvVar{{Name: "PATH", Value: dir}})
	if found {
		t.Errorf("client-go found the plugin from a PATH in the exec block's Env.\n"+
			"If that is now true, this package's whole reason for touching the process's own "+
			"PATH is gone and its package comment must be rewritten.\n  got: %s", message)
	}
	if !strings.Contains(message, "not found") {
		t.Errorf("expected a `not found` failure, got: %s", message)
	}

	// Two: the process's own PATH names it, which is what Augment does.
	t.Setenv("PATH", appended(os.Getenv("PATH"), []string{dir}))
	found, message = probe(nil)
	if !found {
		t.Errorf("client-go could not find the plugin even with the directory on this "+
			"process's PATH, which is the only lever this package has.\n  got: %s", message)
	}
}
