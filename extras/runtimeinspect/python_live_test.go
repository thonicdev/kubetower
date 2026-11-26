package runtimeinspect

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Against real Python containers, when KT_TEST_PYTHON names them: the two
// readers' argv run as Host.Exec would run them, and a real
// prometheus_client's /metrics read through the API server's pod proxy.
// Skipped otherwise; the fakes above hold the rest.
//
// KT_TEST_PYTHON is `context/namespace`, where pods labelled
// app=py-metrics (python:3.12-slim with prometheus_client serving :8000) and
// app=py-distroless (gcr.io/distroless/python3-debian12, no shell) run:
//
//	KT_TEST_PYTHON=kind-kubetower/py-demo go test ./extras/runtimeinspect/ -run Live -v
func TestLivePythonReaders(t *testing.T) {
	where := os.Getenv("KT_TEST_PYTHON")
	if where == "" {
		t.Skip("KT_TEST_PYTHON names no Python pods")
	}
	context, namespace, _ := strings.Cut(where, "/")
	kubectl := func(args ...string) (string, error) {
		out, err := exec.Command("kubectl", append([]string{"--context", context, "-n", namespace}, args...)...).Output() //nolint:gosec // a test's own fixed arguments
		return string(out), err
	}
	podOf := func(app string) string {
		name, err := kubectl("get", "pod", "-l", "app="+app, "-o", "jsonpath={.items[0].metadata.name}")
		if err != nil || name == "" {
			t.Fatalf("no pod app=%s: %v", app, err)
		}
		return name
	}

	for _, c := range []struct {
		app    string
		reader []string
		// shellMissing: this image has no sh, so the first reader must fail
		// as a missing program, which is what moves Host.Exec to the next.
		shellMissing bool
	}{
		{app: "py-metrics", reader: shArgv()},
		{app: "py-distroless", reader: python3Argv(), shellMissing: true},
	} {
		t.Run(c.app, func(t *testing.T) {
			pod := podOf(c.app)
			if c.shellMissing {
				if _, err := kubectl(append([]string{"exec", pod, "-c", "app", "--"}, shArgv()...)...); err == nil {
					t.Fatal("sh ran in an image that should have none")
				}
			}
			out, err := kubectl(append([]string{"exec", pod, "-c", "app", "--"}, c.reader...)...)
			if err != nil {
				t.Fatalf("%s: %v", c.reader[0], err)
			}
			procs, _, _ := parsePythonProcs(out)
			if len(procs) == 0 {
				t.Fatalf("no Python process read:\n%s", out)
			}
			p := procs[0]
			if p.Version == "" || p.RSS <= 0 || p.Peak < p.RSS || p.Threads < 1 || p.Started == "" {
				t.Errorf("process %+v", p)
			}
			t.Logf("%s: pid %d, Python %s, %d B resident, %d threads, started %s", c.reader[0], p.Pid, p.Version, p.RSS, p.Threads, p.Started)
		})
	}

	t.Run("prometheus_client", func(t *testing.T) {
		body, err := exec.Command("kubectl", "--context", context, "get", "--raw", //nolint:gosec // a test's own fixed arguments
			"/api/v1/namespaces/"+namespace+"/pods/"+podOf("py-metrics")+":8000/proxy/metrics").Output()
		if err != nil {
			t.Fatal(err)
		}
		m := readPythonMetrics(string(body))
		if m.Version == "" || m.Implementation != "CPython" || len(m.GC) != 3 || m.RSS == nil || m.OpenFDs == nil {
			t.Errorf("metrics %+v", m)
		}
		t.Logf("%s %s, %d GC generations", m.Implementation, m.Version, len(m.GC))
	})
}
