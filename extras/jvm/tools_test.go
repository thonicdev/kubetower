package jvm

import (
	"fmt"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

const toolsPath = "/x/kind/jvm/pod/shop/billing-0/app/tools"

func toolsHost(t *testing.T, execs map[string]execAnswer) *fakeHost {
	t.Helper()
	h := newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app", Image: "app"}))
	h.execs = execs
	return h
}

func TestToolsSaysJcmdIsThereWhetherOrNotItListsAJVM(t *testing.T) {
	for name, res := range map[string]extension.ExecResult{
		"a JVM listed": {Stdout: []byte("1 app.Main\n")},
		"none listed":  {Stdout: []byte("40 jdk.jcmd/sun.tools.jcmd.JCmd -l\n")},
		"empty":        {},
	} {
		t.Run(name, func(t *testing.T) {
			h := toolsHost(t, map[string]execAnswer{envPrefix + "jcmd -l": {res: res}})
			rec := serve(t, h, toolsPath)
			if got := decode[toolsAnswer](t, rec); rec.Code != http.StatusOK || !got.JCmd {
				t.Errorf("answered %d %+v", rec.Code, got)
			}
			if got := ran(h); len(got) != 1 || got[0] != envPrefix+"jcmd -l" {
				t.Errorf("ran %q", got)
			}
		})
	}
}

func TestToolsFallsBackWithoutEnv(t *testing.T) {
	h := toolsHost(t, map[string]execAnswer{
		envPrefix + "jcmd -l": {err: fmt.Errorf("%w: \"env\"", extension.ErrNoSuchCommand)},
		"jcmd -l":             {res: extension.ExecResult{Stdout: []byte("1 app.Main\n")}},
	})
	if rec := serve(t, h, toolsPath); rec.Code != http.StatusOK || !decode[toolsAnswer](t, rec).JCmd {
		t.Errorf("answered %d %s", rec.Code, rec.Body)
	}
}

func TestToolsNamesItsFailures(t *testing.T) {
	noEnv := fmt.Errorf("%w: \"env\"", extension.ErrNoSuchCommand)
	noJcmd := fmt.Errorf("%w: \"jcmd\"", extension.ErrNoSuchCommand)
	for _, c := range []struct {
		name   string
		execs  map[string]execAnswer
		status int
		code   string
	}{
		{"no jcmd behind env", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{ExitCode: 127}}},
			http.StatusUnprocessableEntity, CodeNoJcmd},
		{"neither env nor jcmd", map[string]execAnswer{envPrefix + "jcmd -l": {err: noEnv}, "jcmd -l": {err: noJcmd}},
			http.StatusUnprocessableEntity, CodeNoJcmd},
		{"exec denied", map[string]execAnswer{envPrefix + "jcmd -l": {err: fmt.Errorf("review: %w", extension.ErrDenied)}},
			http.StatusForbidden, CodeExecForbidden},
		{"timed out", map[string]execAnswer{envPrefix + "jcmd -l": {err: extension.ErrExecTimedOut}},
			http.StatusGatewayTimeout, CodeExecTimedOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, toolsHost(t, c.execs), toolsPath)
			if got := decode[problem](t, rec); rec.Code != c.status || got.Code != c.code {
				t.Errorf("answered %d %+v, want %d %s", rec.Code, got, c.status, c.code)
			}
		})
	}
}

func TestToolsRunsNothingForAPodThatIsNotThere(t *testing.T) {
	h := toolsHost(t, map[string]execAnswer{})
	rec := serve(t, h, "/x/kind/jvm/pod/shop/gone-0/app/tools")
	if rec.Code != http.StatusNotFound || len(h.ran) != 0 {
		t.Errorf("answered %d and ran %q", rec.Code, ran(h))
	}
}
