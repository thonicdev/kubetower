package terminal

// `port-forward` and kubectl plugins: two ways past the read-only override
// that no word on the list sees. Literal command lines through a real /bin/sh,
// because a table generated from `mutatingVerbs` is blind to a word that is
// not in it.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `kubectl port-forward` opens a TCP path into a pod for whatever is sent
// through it, and it would reach the real binary on a marked cluster.
func TestTheShimRefusesPortForward(t *testing.T) {
	for _, args := range [][]string{
		{"port-forward", "svc/postgres", "5432"},
		{"port-forward", "pod/api-0", "8080:80", "-n", "prod"},
		{"--context", "prod", "port-forward", "deploy/api", "9090"},
		{"-n", "kube-system", "port-forward", "svc/metrics-server", "4443:443"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			allowed, out := runShim(t, args...)
			if allowed {
				t.Fatalf("a read-only session forwarded a port: kubectl %s\n%s", strings.Join(args, " "), out)
			}
			if !strings.Contains(out, "'port-forward' is refused, this session is read-only") {
				t.Errorf("refused, but the sentence does not name port-forward: %q", out)
			}
		})
	}

	// And the assistant's read-only override, which is generated from the same
	// list: an assistant session is read-only by default.
	reached, out := runAssistantShim(t, "kubectl", true, "port-forward", "svc/postgres", "5432")
	if reached {
		t.Fatalf("the assistant's read-only override forwarded a port: %q", out)
	}
}

// pluginDir writes stand-in kubectl plugins that say they ran, and returns the
// directory, to be put on the PATH the shim runs with.
func pluginDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		body := "#!/bin/sh\necho REACHED_PLUGIN " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("writing the stand-in plugin %s: %v", name, err)
		}
	}
	return dir
}

// runShimWithPlugins runs an override, as `script` renders it, with the plugin
// directory first on PATH, and reports whether a plugin or the real binary ran.
func runShimWithPlugins(t *testing.T, script func(realBin string) string, plugins string, args ...string) (ran bool, out string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this platform")
	}
	dir := t.TempDir()
	realBin := filepath.Join(dir, "real")
	// The stand-in for kubectl runs the plugin itself, resolved the way kubectl
	// resolves one (the longest run of leading words, `-` spelt `_`), so "let
	// through" is observed as the plugin running and not inferred.
	stand := "#!/bin/sh\nname=kubectl\nfound=\nfor a in \"$@\"; do\n" +
		"  case \"$a\" in -*) break ;; esac\n" +
		"  name=\"$name-$(printf '%s' \"$a\" | tr - _)\"\n" +
		"  command -v \"$name\" >/dev/null 2>&1 && found=$name\n" +
		"done\n[ -n \"$found\" ] && exec \"$found\"\necho REACHED_REAL_BINARY\n"
	if err := os.WriteFile(realBin, []byte(stand), 0o755); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}
	shim := filepath.Join(dir, "shim")
	if err := os.WriteFile(shim, []byte(script(realBin)), 0o755); err != nil {
		t.Fatalf("writing the shim: %v", err)
	}
	cmd := exec.Command(sh, append([]string{shim}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+plugins+string(os.PathListSeparator)+os.Getenv("PATH"))
	raw, _ := cmd.CombinedOutput()
	return strings.Contains(string(raw), "REACHED_PLUGIN"), string(raw)
}

func readOnlyKubectl(realBin string) string { return buildReadOnlyShim("kubectl", realBin) }

// A plugin is somebody else's program behind kubectl's name, and its verbs are
// its own: `kubectl destroyer wipe` would match no word and be exec'd.
func TestTheShimRefusesAKubectlPlugin(t *testing.T) {
	plugins := pluginDir(t, "kubectl-destroyer", "kubectl-foo-bar", "kubectl-view_secret", "kubectl-krew")

	for _, c := range []struct {
		args   []string
		plugin string
	}{
		{[]string{"destroyer"}, "kubectl-destroyer"},
		{[]string{"destroyer", "wipe", "--all"}, "kubectl-destroyer"},
		// Two words, one plugin: kubectl joins the leading words with `-`.
		{[]string{"foo", "bar", "-n", "x"}, "kubectl-foo-bar"},
		// A `-` inside a word is looked up as `_`, as kubectl does.
		{[]string{"view-secret", "api-tls"}, "kubectl-view_secret"},
		// krew is a plugin like any other, so every krew command is refused, not
		// only `krew install` (which `install` on the word list already was).
		{[]string{"krew", "update"}, "kubectl-krew"},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			ran, out := runShimWithPlugins(t, readOnlyKubectl, plugins, c.args...)
			if ran {
				t.Fatalf("a read-only session ran a kubectl plugin: kubectl %s\n%s", strings.Join(c.args, " "), out)
			}
			for _, want := range []string{"runs the kubectl plugin " + c.plugin, "krew", "read-only"} {
				if !strings.Contains(out, want) {
					t.Errorf("the refusal does not say %q: %q", want, out)
				}
			}
		})
	}
}

// The other half: what is not a plugin still reaches the binary, and a flag
// before the word is kubectl's own refusal to run a plugin - the shim does not
// second-guess it.
func TestTheShimLetsWhatIsNotAPluginThrough(t *testing.T) {
	plugins := pluginDir(t, "kubectl-destroyer")
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this platform")
	}
	for _, args := range [][]string{
		{"get", "pods"},
		{"get", "pods", "-n", "destroyer"},
		{"describe", "pod", "destroyer-0"},
		// `kubectl --context x destroyer` is "flags cannot be placed before
		// plugin name" from kubectl itself (measured, readonly.go).
		{"--context", "prod", "get", "destroyer"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := t.TempDir()
			realBin := filepath.Join(dir, "real")
			if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho REACHED_REAL_BINARY\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			shim := filepath.Join(dir, "shim")
			if err := os.WriteFile(shim, []byte(buildReadOnlyShim("kubectl", realBin)), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(sh, append([]string{shim}, args...)...)
			cmd.Env = append(os.Environ(), "PATH="+plugins+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			if !strings.Contains(string(out), "REACHED_REAL_BINARY") {
				t.Fatalf("a read was refused: kubectl %s\n%s", strings.Join(args, " "), out)
			}
		})
	}
}

// The assistant's override refuses a plugin whatever KT_CLAUDE_READONLY
// says: a plugin can read a Secret by a door no pattern names -
// krew's `view-secret` exists to - so the no-Secrets guard refuses it, not only
// the read-only fence. Literal command lines, both overrides, and the sentence
// is the no-Secrets one: the read-only one would point at KT_CLAUDE_READONLY,
// which does not lift this.
func TestTheAssistantRefusesAKubectlPluginWhetherOrNotItMayWrite(t *testing.T) {
	plugins := pluginDir(t, "kubectl-view_secret", "kubectl-krew", "kubectl-foo-bar", "kubectl-destroyer")

	for _, denyWrites := range []bool{false, true} {
		assistant := func(real string) string { return buildAssistantShim("kubectl", real, denyWrites) }
		for _, c := range []struct {
			line   string
			args   []string
			plugin string
		}{
			{"kubectl view-secret x", []string{"view-secret", "x"}, "kubectl-view_secret"},
			{"kubectl view-secret api-tls -n prod", []string{"view-secret", "api-tls", "-n", "prod"}, "kubectl-view_secret"},
			{"kubectl krew install view-secret", []string{"krew", "install", "view-secret"}, "kubectl-krew"},
			{"kubectl foo bar -n x", []string{"foo", "bar", "-n", "x"}, "kubectl-foo-bar"},
			{"kubectl destroyer wipe", []string{"destroyer", "wipe"}, "kubectl-destroyer"},
		} {
			t.Run(fmt.Sprintf("denyWrites=%t/%s", denyWrites, c.line), func(t *testing.T) {
				ran, out := runShimWithPlugins(t, assistant, plugins, c.args...)
				if ran {
					t.Fatalf("an assistant session (denyWrites=%t) ran a kubectl plugin: %s\n%s", denyWrites, c.line, out)
				}
				// Under the fence `krew install` meets the write scan first, which
				// refuses `install` as a verb; that is a refusal too, and says so.
				if denyWrites && c.args[1] == "install" {
					if !strings.Contains(out, "read-only") {
						t.Errorf("refused, but not by either scan: %q", out)
					}
					return
				}
				for _, want := range []string{"runs the kubectl plugin " + c.plugin, "view-secret", "does not read Secrets"} {
					if !strings.Contains(out, want) {
						t.Errorf("the refusal does not say %q: %q", want, out)
					}
				}
				if strings.Contains(out, "KT_CLAUDE_READONLY") || strings.Contains(out, "this session is read-only") {
					t.Errorf("the plugin refusal points at a setting that does not lift it: %q", out)
				}
			})
		}
	}

	// What is not a plugin is unchanged: a writable assistant session still
	// writes, and reads reach the binary with plugins on PATH.
	writable := func(real string) string { return buildAssistantShim("kubectl", real, false) }
	for _, args := range [][]string{
		{"get", "pods", "-n", "destroyer"},
		{"delete", "pod", "api-0"},
		{"--context", "prod", "get", "destroyer"},
	} {
		if _, out := runShimWithPlugins(t, writable, plugins, args...); !strings.Contains(out, "REACHED_REAL_BINARY") {
			t.Errorf("a writable assistant session was refused something that is not a plugin: kubectl %s\n%s",
				strings.Join(args, " "), out)
		}
	}

	// Counter-proof: the override without the plugin scan - a writable
	// session's, the Secret scan and no plugin scan - and the same harness sees
	// the plugin run. Without it, "not ran" above could be a stand-in that never
	// runs anything.
	old := func(real string) string {
		return fmt.Sprintf(shimHeadPOSIX, real) +
			secretScanFor("kubectl") + shimTailPOSIX
	}
	for _, args := range [][]string{{"view-secret", "x"}, {"krew", "install", "view-secret"}, {"foo", "bar"}} {
		if ran, out := runShimWithPlugins(t, old, plugins, args...); !ran {
			t.Fatalf("the old override refused kubectl %s, so the harness proves nothing: %q", strings.Join(args, " "), out)
		}
	}

	// The human terminal's override is untouched: still the read-only sentence.
	if !strings.Contains(buildReadOnlyShim("kubectl", "/usr/bin/kubectl"), "can do anything: refused, this session is read-only") {
		t.Error("the read-only shell's plugin refusal changed")
	}
	// And helm's overrides have none: helm's plugins are not on PATH.
	if strings.Contains(buildReadOnlyShim("helm", "/usr/bin/helm"), "kubectl plugin") {
		t.Error("helm's override carries kubectl's plugin scan")
	}
	for _, denyWrites := range []bool{false, true} {
		if strings.Contains(buildAssistantShim("helm", "/usr/bin/helm", denyWrites), "kubectl plugin") {
			t.Errorf("helm's assistant override (denyWrites=%t) carries kubectl's plugin scan", denyWrites)
		}
	}
}

// The Windows override cannot run here, and no CI job runs
// readonly_windows_test.go, so its plugin scan is pinned on the text: present
// for kubectl, inside the loop and before the shift, absent for helm.
func TestTheWindowsOverrideCarriesThePluginScan(t *testing.T) {
	kubectl := buildReadOnlyShimCmd("kubectl", `C:\bin\kubectl.exe`)
	for _, want := range []string{
		`set "ktplugin=kubectl"`,
		`call set "ktplugin=%%ktplugin%%-%%ktword:-=_%%"`,
		`for %%E in (%PATHEXT%) do for %%P in ("%ktplugin%%%E") do if not "%%~$PATH:P"=="" goto kubetower_refuse_plugin`,
		":kubetower_refuse_plugin",
		"krew",
	} {
		if !strings.Contains(kubectl, want) {
			t.Errorf("the Windows kubectl override does not carry %q:\n%s", want, kubectl)
		}
	}
	scan := strings.Index(kubectl, "goto kubetower_refuse_plugin")
	shift := strings.Index(kubectl, "shift\r\n")
	if shift < 0 {
		shift = strings.Index(kubectl, "shift\n")
	}
	if scan < 0 || shift < 0 || scan > shift {
		t.Errorf("the plugin scan is not inside the argument loop, before the shift:\n%s", kubectl)
	}
	if strings.Contains(buildReadOnlyShimCmd("helm", `C:\bin\helm.exe`), "kubetower_refuse_plugin") {
		t.Error("helm's Windows override carries kubectl's plugin scan")
	}
	// The assistant's, read-only or not, with the
	// no-Secrets sentence and the scan inside the loop.
	for _, denyWrites := range []bool{false, true} {
		script := buildAssistantShimCmd("kubectl", `C:\bin\kubectl.exe`, denyWrites)
		for _, want := range []string{
			`goto kubetower_refuse_plugin`,
			":kubetower_refuse_plugin",
			"krew's view-secret or any other) can read a Secret: refused, this session does not read Secrets.",
			"KT_NO_SECRETS_WHY",
		} {
			if !strings.Contains(script, want) {
				t.Errorf("the assistant's Windows override (denyWrites=%t) does not carry %q:\n%s", denyWrites, want, script)
			}
		}
		if n := strings.Count(script, ":kubetower_refuse_plugin"); n != 1 {
			t.Errorf("the assistant's Windows override (denyWrites=%t) has %d plugin refusal labels, want 1", denyWrites, n)
		}
		scan := strings.Index(script, "goto kubetower_refuse_plugin")
		shift := strings.Index(script, "shift\r\n")
		if shift < 0 {
			shift = strings.Index(script, "shift\n")
		}
		if scan < 0 || shift < 0 || scan > shift {
			t.Errorf("the assistant's plugin scan (denyWrites=%t) is not inside the loop, before the shift:\n%s", denyWrites, script)
		}
	}
	if strings.Contains(buildAssistantShimCmd("helm", `C:\bin\helm.exe`, false), "kubetower_refuse_plugin") {
		t.Error("helm's assistant Windows override carries kubectl's plugin scan")
	}
}
