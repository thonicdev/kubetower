package extras_test

import (
	"os"
	"strings"
	"testing"
)

// A replace binds only the module that declares it. boundary/ builds the core
// as a dependency, so every replace the core declares for a dependency must be
// declared there too, pointing at the same tree; otherwise boundary resolves
// the published module and misses the core's patch (#84: go-webview2's Error,
// which only GOOS=windows compiles, so a Linux CI stays green).
func TestBoundaryRepeatsTheCoresReplaces(t *testing.T) {
	core := readMod(t, "../go.mod")
	boundary := readMod(t, "../boundary/go.mod")
	if len(core) == 0 {
		t.Fatal("no replace read from the core's go.mod")
	}
	for old, want := range core {
		if strings.HasPrefix(want, "./") {
			want = "../" + strings.TrimPrefix(want, "./")
		}
		if got, ok := boundary[old]; !ok || got != want {
			t.Errorf("boundary/go.mod replaces %s with %q; the core replaces it with %q", old, got, want)
		}
	}
}

// readMod is a go.mod's one-line replaces, module path to target. Neither
// file uses a replace block or a version on the left.
func readMod(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[0] == "replace" && fields[2] == "=>" {
			out[fields[1]] = fields[3]
		}
	}
	return out
}
