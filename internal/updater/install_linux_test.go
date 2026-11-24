package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type restarted struct{ path string }

func stubRestart(t *testing.T) *restarted {
	t.Helper()
	got := &restarted{}
	was := restart
	restart = func(path string, _ []string) error { got.path = path; return nil }
	t.Cleanup(func() { restart = was })
	return got
}

func TestAnAppImageIsReplacedThenStartedAgain(t *testing.T) {
	r := stubRestart(t)
	dir := t.TempDir()
	image := filepath.Join(dir, "KubeTower.AppImage")
	_ = os.WriteFile(image, []byte("0.1.2"), 0o755)
	file := filepath.Join(t.TempDir(), "kubetower-0.1.3-x86_64.AppImage")
	_ = os.WriteFile(file, []byte("0.1.3"), 0o600)
	err := installers[LinuxAppImage](context.Background(), Plan{File: file, Installation: Installation{Kind: LinuxAppImage, Path: image}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(image); string(got) != "0.1.3" || r.path != image {
		t.Fatalf("%q, restarted %q", got, r.path)
	}
}

func TestATarInstallIsReplacedFromTheArchive(t *testing.T) {
	r := stubRestart(t)
	bin := filepath.Join(t.TempDir(), "kubetower")
	_ = os.WriteFile(bin, []byte("0.1.2"), 0o755)
	archive := tarGz(t, map[string]string{"./kubetower": "0.1.3", "./LICENSE": "x"}, nil)
	if err := installers[LinuxTar](context.Background(), Plan{File: archive, Installation: Installation{Kind: LinuxTar, Path: bin}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(bin); string(got) != "0.1.3" || r.path != bin {
		t.Fatalf("%q, restarted %q", got, r.path)
	}
}

func TestAFailedReplaceDoesNotRestart(t *testing.T) {
	r := stubRestart(t)
	bin := filepath.Join(t.TempDir(), "kubetower")
	_ = os.WriteFile(bin, []byte("0.1.2"), 0o755)
	archive := tarGz(t, map[string]string{"./LICENSE": "x"}, nil)
	if err := installers[LinuxTar](context.Background(), Plan{File: archive, Installation: Installation{Kind: LinuxTar, Path: bin}}); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("%v", err)
	}
	if r.path != "" {
		t.Error("restarted after a failed replace")
	}
}

func TestPkexecRefusalsAreSaidPlainly(t *testing.T) {
	for code, want := range map[string]string{
		"126": "the password was not given",
		"127": "not authorized to install packages",
		"1":   "the package manager refused: E: broken",
	} {
		err := exec.Command("/bin/sh", "-c", "echo 'E: broken'; exit "+code).Run()
		got := pkexecFailure(err, []byte("E: broken\nmore"))
		if !strings.Contains(got.Error(), want) {
			t.Errorf("exit %s: %v, want %q", code, got, want)
		}
	}
}

func TestEveryLinuxInstallationHasAnInstaller(t *testing.T) {
	for _, kind := range []Kind{LinuxAppImage, LinuxTar, LinuxDeb, LinuxRPM} {
		if installers[kind] == nil {
			t.Errorf("no installer for %s", kind)
		}
	}
}

func TestTheRefusalIsTheLineThatSaysWhy(t *testing.T) {
	for out, want := range map[string]string{
		"Reading package lists...\nBuilding dependency tree...\nE: Unable to locate package x\nE: more": "E: Unable to locate package x",
		"Last metadata expiration check\nError: Transaction test error\n":                               "Error: Transaction test error",
		"Loading repository data...\nReading installed packages...\nProblem: nothing provides libfoo\n": "Problem: nothing provides libfoo",
		"": "",
	} {
		if got := refusal(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

// stubPackage makes pkexec and apt-get present, and runs nothing.
func stubPackage(t *testing.T, run func([]string) ([]byte, error)) {
	t.Helper()
	look, priv := lookPath, runPrivileged
	lookPath = func(name string) (string, error) {
		if name == "pkexec" || name == "apt-get" {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	runPrivileged = func(_ context.Context, argv []string) ([]byte, error) { return run(argv) }
	t.Cleanup(func() { lookPath, runPrivileged = look, priv })
}

func TestAnInstalledPackageIsStartedAgain(t *testing.T) {
	r := stubRestart(t)
	var ran []string
	stubPackage(t, func(argv []string) ([]byte, error) { ran = argv; return nil, nil })
	plan := Plan{File: "/s/kubetower_0.1.3_amd64.deb", Installation: Installation{Kind: LinuxDeb, Path: "/usr/bin/kubetower"}}
	if err := installers[LinuxDeb](context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, " ") != "/usr/bin/pkexec /usr/bin/apt-get install -y /s/kubetower_0.1.3_amd64.deb" || r.path != "/usr/bin/kubetower" {
		t.Fatalf("ran %q, restarted %q", ran, r.path)
	}
}

func TestARefusedPackageIsNotStartedAgain(t *testing.T) {
	r := stubRestart(t)
	stubPackage(t, func([]string) ([]byte, error) {
		return []byte("Reading package lists...\nE: broken"), errors.New("exit status 100")
	})
	plan := Plan{File: "/s/x.deb", Installation: Installation{Kind: LinuxDeb, Path: "/usr/bin/kubetower"}}
	err := installers[LinuxDeb](context.Background(), plan)
	if err == nil || err.Error() != "the package manager refused: E: broken" || r.path != "" {
		t.Fatalf("%v, restarted %q", err, r.path)
	}
}

func TestARestartThatFailsIsAnInstalledUpdate(t *testing.T) {
	was := restart
	restart = func(string, []string) error { return errors.New("no /bin/sh") }
	t.Cleanup(func() { restart = was })
	if err := startAgain("/usr/bin/kubetower"); !errors.Is(err, ErrNotRestarted) {
		t.Fatalf("%v, want ErrNotRestarted", err)
	}
}

func TestAPackageWithoutPkexecIsNotSupported(t *testing.T) {
	look := lookPath
	t.Cleanup(func() { lookPath = look })
	for _, tc := range []struct {
		tools []string
		want  bool
	}{{[]string{"apt-get"}, false}, {[]string{"pkexec"}, false}, {[]string{"pkexec", "dpkg"}, true}} {
		lookPath = func(name string) (string, error) {
			for _, tool := range tc.tools {
				if tool == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		}
		u := New(Options{GOOS: "linux", GOARCH: "amd64", Dir: t.TempDir(), Installation: Installation{Kind: LinuxDeb, Path: "/usr/bin/kubetower"}})
		if got := u.Status().Supported; got != tc.want {
			t.Errorf("%v: supported %v, want %v", tc.tools, got, tc.want)
		}
	}
}

func TestTheRelaunchWaitGivesUp(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ran")
	run := func(pid int, checks string) (time.Duration, error) {
		start := time.Now()
		// Without the bound the wait never ends: the context ends it, as a failure.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := exec.CommandContext(ctx, "/bin/sh", "-c", waitThenRun, "sh", strconv.Itoa(pid), checks, "/bin/sh", "-c", "touch "+out).Run()
		return time.Since(start), err
	}
	// This test's own pid never exits during the wait: it gives up, runs nothing.
	took, err := run(os.Getpid(), "3")
	if err == nil || took > 5*time.Second {
		t.Fatalf("%v after %v, want a refusal within the bound", err, took)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("ran the command although the process was still there")
	}
	// A process already gone: the command runs.
	gone := exec.Command("/bin/true")
	_ = gone.Run()
	if _, err := run(gone.Process.Pid, "3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("did not run the command once the process was gone")
	}
}
