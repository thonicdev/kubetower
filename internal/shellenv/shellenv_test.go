//go:build unix

package shellenv

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeShell writes a shell that refuses to run unless started as a login,
// interactive shell (`-l -i -c script`), runs rc, then the script it was given.
func fakeShell(t *testing.T, rc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zsh")
	body := "#!/bin/sh\n" +
		`[ "$1" = -l ] && [ "$2" = -i ] && [ "$3" = -c ] || exit 3` + "\n" +
		rc + "\n" +
		`eval "$4"` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readWithin(t *testing.T, shell string, d time.Duration) (map[string]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return read(ctx, shell)
}

// launchd's environment: what a .app opened from the Dock starts with.
func launchdEnv() map[string]string {
	return map[string]string{
		"PATH":       "/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME":       "/Users/op",
		"AWS_REGION": "eu-west-3",
	}
}

func applyTo(env, process map[string]string) ([]string, bool) {
	return apply(env,
		func(name string) (string, bool) { v, ok := process[name]; return v, ok },
		func(name, value string) error { process[name] = value; return nil })
}

func TestTheLoginShellsAWSProfileReachesAProcessLaunchdStarted(t *testing.T) {
	shell := fakeShell(t, `echo "Last login: Mon" ; echo banner >&2
export AWS_PROFILE=sso-dev
export AWS_CONFIG_FILE=/Users/op/.aws/config.work
export KUBECONFIG=/Users/op/.kube/work
export AWS_REGION=us-east-1
export KT_LISTEN=0.0.0.0:1
export PATH=/opt/homebrew/bin:/usr/bin:/bin`)

	env, err := readWithin(t, shell, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	process := launchdEnv()
	imported, pathChanged := applyTo(env, process)

	for _, name := range []string{"AWS_PROFILE", "AWS_CONFIG_FILE", "KUBECONFIG"} {
		if !slices.Contains(imported, name) {
			t.Errorf("%s was not imported: %v", name, imported)
		}
	}
	if process["AWS_PROFILE"] != "sso-dev" || process["KUBECONFIG"] != "/Users/op/.kube/work" {
		t.Errorf("wrong values: AWS_PROFILE=%q KUBECONFIG=%q", process["AWS_PROFILE"], process["KUBECONFIG"])
	}
	// Already set by whoever started the process: kept.
	if process["AWS_REGION"] != "eu-west-3" || slices.Contains(imported, "AWS_REGION") {
		t.Errorf("AWS_REGION was overwritten: %q", process["AWS_REGION"])
	}
	// Read by the console before this runs, and session noise.
	for _, name := range []string{"KT_LISTEN", "PWD", "SHLVL", "_"} {
		if slices.Contains(imported, name) {
			t.Errorf("%s was imported", name)
		}
	}
	if !pathChanged || process["PATH"] != "/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Errorf("PATH = %q (changed %v), want the shell's order then launchd's leftovers", process["PATH"], pathChanged)
	}
	if !slices.IsSorted(imported) {
		t.Errorf("imported is not sorted: %v", imported)
	}
}

func TestAValueWithANewlineAndAnRCBannerAreReadExactly(t *testing.T) {
	shell := fakeShell(t, `printf 'X=1\000Y=2\000'
export MULTI="$(printf 'a\nb=c')"`)
	env, err := readWithin(t, shell, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if env["MULTI"] != "a\nb=c" {
		t.Errorf("MULTI = %q", env["MULTI"])
	}
	if _, ok := env["X"]; ok {
		t.Error("output printed before the marker was read as the environment")
	}
}

func TestAShellThatHangsLeavesTheEnvironmentAlone(t *testing.T) {
	shell := fakeShell(t, `sleep 30`)
	started := time.Now()
	env, err := readWithin(t, shell, 300*time.Millisecond)
	if err == nil || env != nil {
		t.Fatalf("a hanging shell answered: %v %v", env, err)
	}
	if !strings.Contains(err.Error(), "in time") {
		t.Errorf("error = %v", err)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("waited %s for a shell past its deadline", took)
	}
}

func TestAShellThatFailsBeforePrintingLeavesTheEnvironmentAlone(t *testing.T) {
	shell := fakeShell(t, `exit 7`)
	if env, err := readWithin(t, shell, 5*time.Second); err == nil || env != nil {
		t.Fatalf("a failed shell answered: %v %v", env, err)
	}
}

func TestAnRCFileWhoseLastCommandFailsStillCounts(t *testing.T) {
	shell := fakeShell(t, `export AWS_PROFILE=p`)
	// The script's own trailing command fails after the environment is printed.
	body, _ := os.ReadFile(shell)
	if err := os.WriteFile(shell, append(body, []byte("false\n")...), 0o700); err != nil {
		t.Fatal(err)
	}
	env, err := readWithin(t, shell, 5*time.Second)
	if err != nil || env["AWS_PROFILE"] != "p" {
		t.Fatalf("env[AWS_PROFILE]=%q err=%v", env["AWS_PROFILE"], err)
	}
}

func TestNothingIsAskedFromATerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	if got := Import(); got.Shell != "" || got.Err != nil || len(got.Imported) > 0 {
		t.Errorf("Import from a terminal did something: %+v", got)
	}
}

func TestLoginShell(t *testing.T) {
	for in, want := range map[string]string{"": "/bin/zsh", "zsh": "/bin/zsh", "/opt/homebrew/bin/fish": "/opt/homebrew/bin/fish"} {
		if got := loginShell(in); got != want {
			t.Errorf("loginShell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergePath(t *testing.T) {
	cases := []struct{ shell, current, want string }{
		{"", "/usr/bin:/bin", "/usr/bin:/bin"},
		{"/a:/usr/bin", "/usr/bin:/bin", "/a:/usr/bin:/bin"},
		{"/a::/a/", "", "/a"},
	}
	for _, c := range cases {
		if got := mergePath(c.shell, c.current); got != c.want {
			t.Errorf("mergePath(%q, %q) = %q, want %q", c.shell, c.current, got, c.want)
		}
	}
}
