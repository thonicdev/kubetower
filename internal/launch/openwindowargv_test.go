package launch

// No command line openWindow
// starts ever carries a code. This runs openWindow itself, with the native
// window refused and every process start recorded instead of made: nothing is
// drawn and no browser is started.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoCommandLineOpenWindowStartsCarriesACode(t *testing.T) {
	t.Setenv("KT_STATE_DIR", t.TempDir())
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	listen := strings.TrimPrefix(srv.URL, "http://")
	const code = "a-one-time-code-that-must-stay-off-every-command-line"
	mint := func(bool) (string, error) { return code, nil }

	previousNative, previousStart := drawNative, startProcess
	t.Cleanup(func() { drawNative, startProcess = previousNative, previousStart })
	drawNative = func(string, windowSources) bool { return false }
	confineNothing(t)

	for name, chromiumStarts := range map[string]bool{
		"Chromium starts":                     true,
		"Chromium fails, the default browser": false,
	} {
		var argvs [][]string
		startProcess = func(cmd string, args ...string) error {
			argvs = append(argvs, append([]string{cmd}, args...))
			if !chromiumStarts && len(argvs) == 1 && cmd != "cmd" && cmd != "open" && cmd != "xdg-open" {
				return errors.New("refused by the test")
			}
			return nil
		}
		if openWindow(context.Background(), listen, mint, windowSources{}) {
			t.Fatalf("%s: openWindow says a native window closed, but none was drawn", name)
		}
		if len(argvs) == 0 {
			t.Fatalf("%s: no process was started, so this test proves nothing", name)
		}
		for _, argv := range argvs {
			if strings.Contains(strings.Join(argv, " "), code) {
				t.Errorf("%s: a command line carries the code: %q", name, argv)
			}
		}
	}
}
