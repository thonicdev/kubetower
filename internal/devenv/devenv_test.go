package devenv

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// What `task dev:password` appends to a .env that has none of the three.
const devPasswordLines = "\n# Written by `task dev:password`\n" +
	"KT_PASSWORD_HASH='$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234'\n" +
	"KT_SESSION_SECRET='00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff'\n" +
	"KT_STATE_DIR='@STATE@'\n"

// withState puts an absolute state directory, valid on this platform, where
// devPasswordLines has a placeholder: `task dev:password` always writes one.
func withState(t *testing.T, content string) string {
	t.Helper()
	return strings.ReplaceAll(content, "@STATE@", filepath.ToSlash(filepath.Join(t.TempDir(), ".kubetower-dev")))
}

// TestADotEnvCopiedFromTheExampleGivesTheDesktopOnlyTheThree is asserted
// against the real `.env.example`.
//
// The documented container setup is `cp .env.example .env`, and the example
// sets KT_FORWARD_PORTS uncommented. A desktop process given it binds every
// port-forward on 0.0.0.0. So: copy the example, add what `task dev:password`
// writes, and what reaches the process must be the three variables and nothing
// else - KT_FORWARD_PORTS above all.
func TestADotEnvCopiedFromTheExampleGivesTheDesktopOnlyTheThree(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	// The test is only worth something while the example really carries the
	// dangerous value; if it stops, say so rather than passing on nothing.
	if v, ok := Effective(Lines(example), "KT_FORWARD_PORTS"); !ok || v == "" {
		t.Fatal(".env.example no longer sets KT_FORWARD_PORTS, so this test proves nothing: " +
			"point it at whichever container-only variable the example now sets")
	}

	dotenv := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(dotenv, append(example, withState(t, devPasswordLines)...), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := Load(dotenv)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for key := range fromFile.Values {
		got = append(got, key)
	}
	sort.Strings(got)
	want := append([]string(nil), Keys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("taken from a .env copied from the example: %v, want exactly %v", got, want)
	}

	environ, _, _, err := Apply([]string{"PATH=/usr/bin"}, fromFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		for _, forbidden := range []string{"KT_FORWARD_PORTS", "HOST_KUBE_DIR", "HOST_AWS_DIR", "KT_PORT"} {
			if strings.EqualFold(name, forbidden) {
				t.Errorf("%s reached the desktop process's environment: %q", forbidden, entry)
			}
		}
	}
}

// The shell wins, including when it set a variable to the empty string: that is
// how `KT_PASSWORD_HASH= task password` and `KT_STATE_DIR=... task dev` work.
func TestTheEnvironmentWinsOverTheFile(t *testing.T) {
	fromFile := Parse([]byte(withState(t, devPasswordLines)))
	environ, added, shadowed, err := Apply([]string{"KT_STATE_DIR=/from/shell", "KT_PASSWORD_HASH="}, fromFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "KT_SESSION_SECRET" {
		t.Errorf("added %v, want only KT_SESSION_SECRET", added)
	}
	if strings.Join(shadowed, ",") != "KT_PASSWORD_HASH,KT_STATE_DIR" {
		t.Errorf("shadowed %v, want KT_PASSWORD_HASH and KT_STATE_DIR", shadowed)
	}
	joined := strings.Join(environ, "\n")
	if strings.Contains(joined, "kubetower-dev") || strings.Contains(joined, "$2a$") {
		t.Errorf("the file overrode the environment:\n%s", joined)
	}
}

// A missing .env is an empty answer: a clone without one runs on a drawn
// password, as a downloaded binary does.
func TestAMissingFileTakesNothing(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(got.Values) != 0 || !got.Missing {
		t.Errorf("Load of a missing file = %+v, %v; want Missing, empty, nil", got, err)
	}
}

// TestNoTaskLoadsTheWholeDotEnv holds the Taskfile to the same rule: no task
// may use Task's own `dotenv`, which loads every line, and the two tasks that
// start the console must go through devwatch's `-dotenv`, which takes Keys.
func TestNoTaskLoadsTheWholeDotEnv(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var taskfile struct {
		Dotenv []string `json:"dotenv"`
		Tasks  map[string]struct {
			Dotenv []string `json:"dotenv"`
			Cmds   []any    `json:"cmds"`
		} `json:"tasks"`
	}
	if err := yaml.Unmarshal(raw, &taskfile); err != nil {
		t.Fatalf("parsing Taskfile.yaml: %v", err)
	}
	if len(taskfile.Dotenv) > 0 {
		t.Errorf("Taskfile.yaml has a top-level dotenv %v: every task would get the whole file", taskfile.Dotenv)
	}
	for name, task := range taskfile.Tasks {
		if len(task.Dotenv) > 0 {
			t.Errorf("task %q loads %v whole with Task's dotenv. A .env copied from .env.example "+
				"carries KT_FORWARD_PORTS, and a desktop process given it binds every forward on "+
				"0.0.0.0. Use devwatch's -dotenv, which takes only internal/devenv.Keys", name, task.Dotenv)
		}
	}
	// And not through the shell either: `.`, `source` or `set -a` hand the
	// process every line just as `dotenv` did. See sourcesTheWholeFile.
	for i, line := range Lines(raw) {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if sourcesTheWholeFile.MatchString(line) {
			t.Errorf("Taskfile.yaml:%d loads .env through the shell, every line of it: %q. A .env "+
				"copied from .env.example carries KT_FORWARD_PORTS; use devwatch's -dotenv", i+1, line)
		}
	}
	for _, name := range []string{"dev:api", "password"} {
		task, ok := taskfile.Tasks[name]
		if !ok {
			t.Errorf("task %q is gone; this test names it on purpose", name)
			continue
		}
		found := false
		for _, cmd := range task.Cmds {
			if s, ok := cmd.(string); ok && strings.Contains(s, "./cmd/devwatch") && strings.Contains(s, "-dotenv .env") {
				found = true
			}
		}
		if !found {
			t.Errorf("task %q does not start the console through `./cmd/devwatch -dotenv .env`", name)
		}
	}
}
