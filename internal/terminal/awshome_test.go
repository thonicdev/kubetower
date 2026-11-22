package terminal

// Each terminal session gets its own AWS CLI credential cache.
//
// The AWS CLI's assume-role cache has no lock, no O_TRUNC and no atomic rename,
// so two `aws` processes writing one entry leave two JSON documents end to end
// in it and the profile stays dead until somebody deletes the file. The error
// says nothing — client-go returns `exec: executable aws failed with exit code
// 255` and the CLI's own message never reaches this process — so this cannot be
// caught at runtime, only prevented. `internal/cluster/awscache.go` prevents it
// for the in-process clients; these tests are the dock's terminals, which run
// the same `aws eks get-token` through kubectl and helm.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- fixtures ----------------------------------------------------------------

// fakeOperatorHome points os.UserHomeDir at a temporary directory holding a
// plausible ~/.aws, and clears the two variables compose.dev.yaml normally
// sets so the code under test has to supply them itself.
//
// It returns the home and the token file inside its SSO cache: whether that
// exact file is still reachable through the session's home is the whole question
// the symlink exists to answer.
func fakeOperatorHome(t *testing.T) (home, ssoToken string) {
	t.Helper()

	home = t.TempDir()
	cache := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatalf("building the fake ~/.aws: %v", err)
	}
	ssoToken = filepath.Join(cache, "token.json")
	if err := os.WriteFile(ssoToken, []byte(`{"accessToken":"THE-SHARED-SSO-TOKEN"}`), 0o600); err != nil {
		t.Fatalf("writing the fake SSO token: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[profile prod]\n"), 0o600); err != nil {
		t.Fatalf("writing the fake ~/.aws/config: %v", err)
	}

	// os.UserHomeDir reads HOME everywhere except Windows, where it reads
	// USERPROFILE. Both, so this test does not depend on which one it runs on —
	// the same reason the code under test writes both into the exec block.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	return home, ssoToken
}

// symlinksWork reports whether this machine lets this process create a directory
// symlink. Windows without the symlink privilege does not, and that is exactly
// the case where the separation refuses itself rather than breaking SSO.
func symlinksWork(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("probing for symlink support: %v", err)
	}
	return os.Symlink(target, filepath.Join(dir, "link")) == nil
}

func requireSymlinks(t *testing.T) {
	t.Helper()
	if !symlinksWork(t) {
		t.Skip("this host does not let this process create a directory symlink, so the shared SSO " +
			"cache cannot be tested here; the Linux image `docker build` runs go test in does. " +
			"On Windows the privilege is SeCreateSymbolicLinkPrivilege, which Developer Mode " +
			"grants — turn it on and this test runs on this machine")
	}
}

// eksConfig is a kubeconfig whose one context authenticates the way an EKS
// context does: an exec block running `aws eks get-token`, with AWS_PROFILE in
// its environment and nothing else.
func eksConfig(env ...clientcmdapi.ExecEnvVar) *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["eks"] = &clientcmdapi.Cluster{Server: "https://eks.example:443"}
	cfg.AuthInfos["eks"] = &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			APIVersion: "client.authentication.k8s.io/v1beta1",
			Command:    "aws",
			Args:       []string{"--region", "eu-west-1", "eks", "get-token", "--cluster-name", "example"},
			Env: append([]clientcmdapi.ExecEnvVar{
				{Name: "AWS_PROFILE", Value: "prod-admin"},
			}, env...),
		},
	}
	cfg.Contexts["eks"] = &clientcmdapi.Context{Cluster: "eks", AuthInfo: "eks"}
	cfg.CurrentContext = "eks"
	return cfg
}

// tokenConfig is a context with no exec block: nothing runs `aws` on its behalf
// and there is no cache for it to corrupt.
func tokenConfig() *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["local"] = &clientcmdapi.Cluster{Server: "https://local.example:6443"}
	cfg.AuthInfos["local"] = &clientcmdapi.AuthInfo{Token: "LOCAL-TOKEN"}
	cfg.Contexts["local"] = &clientcmdapi.Context{Cluster: "local", AuthInfo: "local"}
	cfg.CurrentContext = "local"
	return cfg
}

// execEnvOf reads one variable out of the first exec block in a config.
func execEnvOf(t *testing.T, cfg *clientcmdapi.Config, name string) (string, bool) {
	t.Helper()
	for _, user := range cfg.AuthInfos {
		if user == nil || user.Exec == nil {
			continue
		}
		for _, v := range user.Exec.Env {
			if v.Name == name {
				return v.Value, true
			}
		}
	}
	return "", false
}

// countExecEnv is how idempotence is asserted: two HOMEs in one exec block is
// not "the same as one", it is a config whose meaning depends on which end the
// CLI reads from.
func countExecEnv(cfg *clientcmdapi.Config, name string) int {
	n := 0
	for _, user := range cfg.AuthInfos {
		if user == nil || user.Exec == nil {
			continue
		}
		for _, v := range user.Exec.Env {
			if v.Name == name {
				n++
			}
		}
	}
	return n
}

// --- what separate decides ---------------------------------------------------

func TestTheSessionAWSHomeIsWrittenOnlyWhereItIsNeeded(t *testing.T) {
	requireSymlinks(t)

	for _, c := range []struct {
		name string
		cfg  *clientcmdapi.Config
		// off asks for the zero home, which is what "no home directory to read
		// the shared files out of" produces.
		off      bool
		changed  bool
		wantHome string // "" means "no HOME must have been written"
		why      string
	}{
		{
			name: "an exec block gets its own home", cfg: eksConfig(),
			changed: true, wantHome: "session",
			why: "an EKS context's exec block is `aws eks get-token`, which is the writer " +
				"the shared credential cache race is about",
		},
		{
			name: "no exec block is left alone", cfg: tokenConfig(),
			changed: false, wantHome: "",
			why: "nothing runs `aws` for a token context, so there is no cache to separate " +
				"and no directory worth creating",
		},
		{
			name:    "an exec block naming HOME itself is left alone",
			cfg:     eksConfig(clientcmdapi.ExecEnvVar{Name: "HOME", Value: "/operator/home"}),
			changed: false, wantHome: "/operator/home",
			why: "the operator meant it; overriding it would be this code deciding it knows " +
				"better about somebody's own configuration",
		},
		{
			name: "the separation off writes nothing", cfg: eksConfig(), off: true,
			changed: false, wantHome: "",
			why: "with no home directory to point back at there is nothing to redirect to, " +
				"and a redirect without that is a session that cannot find its own profile",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeOperatorHome(t)
			workdir := t.TempDir()

			home := newSessionAWSHome(workdir)
			if c.off {
				home = sessionAWSHome{}
			}

			changed, err := home.separate(c.cfg)
			if err != nil {
				t.Fatalf("separating: %v", err)
			}
			if changed != c.changed {
				t.Fatalf("separate reported changed=%t, want %t — %s", changed, c.changed, c.why)
			}

			got, set := execEnvOf(t, c.cfg, "HOME")
			switch c.wantHome {
			case "":
				if set {
					t.Fatalf("HOME=%q was written into the exec block — %s", got, c.why)
				}
				if _, err := os.Stat(filepath.Join(workdir, sessionAWSHomeDir)); err == nil {
					t.Errorf("a session AWS home was created at %s although nothing uses it",
						filepath.Join(workdir, sessionAWSHomeDir))
				}
			case "session":
				want := filepath.Join(workdir, sessionAWSHomeDir)
				if got != want {
					t.Fatalf("HOME is %q, want the session's own %q — %s", got, want, c.why)
				}
				// USERPROFILE too: Windows has no password database and
				// ntpath.expanduser never consults HOME.
				if up, _ := execEnvOf(t, c.cfg, "USERPROFILE"); up != want {
					t.Errorf("USERPROFILE is %q, want %q — on Windows that is the only one "+
						"expanduser reads", up, want)
				}
			default:
				if got != c.wantHome {
					t.Fatalf("HOME is %q, want %q — %s", got, c.wantHome, c.why)
				}
			}
		})
	}
}

// AWS_PROFILE is the only thing telling the CLI which role to assume. Rebuilding
// the exec environment instead of appending to it takes it away, and the failure
// is an authentication error naming a missing profile rather than anything
// pointing at this file.
func TestTheSessionAWSHomeKeepsTheProfileTheExecBlockCarries(t *testing.T) {
	requireSymlinks(t)
	fakeOperatorHome(t)

	cfg := eksConfig()
	if _, err := newSessionAWSHome(t.TempDir()).separate(cfg); err != nil {
		t.Fatalf("separating: %v", err)
	}
	if profile, _ := execEnvOf(t, cfg, "AWS_PROFILE"); profile != "prod-admin" {
		t.Fatalf("AWS_PROFILE is %q, want %q — the exec environment was rebuilt rather than "+
			"appended to, and the session now assumes no role at all", profile, "prod-admin")
	}
}

// The same rule HOME is under, one variable along: an exec block that already
// names where its configuration lives meant it, and a second AWS_CONFIG_FILE
// appended after the operator's own is a file whose meaning depends on which end
// the CLI reads the list from.
//
// This is the case that exercises addExecEnv's own guard. The loop in separate
// skips a block that names HOME before it gets there, so HOME never reaches it
// twice — a mutation making addExecEnv append unconditionally survives every
// other test in this file, and fails this one.
func TestTheSessionAWSHomeDoesNotSecondGuessAConfigFileTheExecBlockNames(t *testing.T) {
	requireSymlinks(t)
	fakeOperatorHome(t)

	cfg := eksConfig(clientcmdapi.ExecEnvVar{Name: "AWS_CONFIG_FILE", Value: "/operator/aws/config"})
	if _, err := newSessionAWSHome(t.TempDir()).separate(cfg); err != nil {
		t.Fatalf("separating: %v", err)
	}
	if n := countExecEnv(cfg, "AWS_CONFIG_FILE"); n != 1 {
		t.Fatalf("the exec block carries %d AWS_CONFIG_FILEs, want the operator's one", n)
	}
	if got, _ := execEnvOf(t, cfg, "AWS_CONFIG_FILE"); got != "/operator/aws/config" {
		t.Fatalf("AWS_CONFIG_FILE is %q, want the operator's own %q",
			got, "/operator/aws/config")
	}
}

// Applying it twice must leave one HOME, not two.
func TestTheSessionAWSHomeIsIdempotent(t *testing.T) {
	requireSymlinks(t)
	fakeOperatorHome(t)

	cfg := eksConfig()
	home := newSessionAWSHome(t.TempDir())
	if _, err := home.separate(cfg); err != nil {
		t.Fatalf("separating: %v", err)
	}
	if _, err := home.separate(cfg); err != nil {
		t.Fatalf("separating again: %v", err)
	}
	if n := countExecEnv(cfg, "HOME"); n != 1 {
		t.Fatalf("the exec block carries %d HOMEs, want 1", n)
	}
}

// The whole reason this is per session and not per context.
//
// internal/cluster keys its homes on the context, correctly: it holds one client
// per context and every request shares it. A terminal is not shared — two tabs
// on one cluster are two shells, two kubectls and two `aws` processes — so
// keying on the context here would put exactly the racing pair back on one cache
// entry.
func TestTwoSessionsOnOneClusterDoNotShareACacheEntry(t *testing.T) {
	requireSymlinks(t)
	fakeOperatorHome(t)

	first, second := eksConfig(), eksConfig()
	if _, err := newSessionAWSHome(t.TempDir()).separate(first); err != nil {
		t.Fatalf("separating the first session: %v", err)
	}
	if _, err := newSessionAWSHome(t.TempDir()).separate(second); err != nil {
		t.Fatalf("separating the second session: %v", err)
	}

	a, _ := execEnvOf(t, first, "HOME")
	b, _ := execEnvOf(t, second, "HOME")
	if a == "" || b == "" {
		t.Fatalf("one of the two sessions got no home at all: %q and %q", a, b)
	}
	if a == b {
		t.Fatalf("both sessions run under %q, so two terminals on one cluster are two `aws` "+
			"processes on one cache entry again — which is the case that corrupts the cache", a)
	}
}

// --- the SSO half, which is what makes the redirect safe ----------------------

// The SSO *token* cache must stay shared, since SSO contexts renew in it. It is
// derived from the same `~` as the assume-role cache — three separate
// os.path.expanduser calls in botocore, none of them with an environment
// variable — so a bare HOME redirect moves it too and every SSO context stops
// renewing. The link is what keeps the token shared.
func TestTheSessionAWSHomeKeepsTheSSOTokenCacheShared(t *testing.T) {
	requireSymlinks(t)
	_, ssoToken := fakeOperatorHome(t)

	workdir := t.TempDir()
	cfg := eksConfig()
	if _, err := newSessionAWSHome(workdir).separate(cfg); err != nil {
		t.Fatalf("separating: %v", err)
	}

	home, _ := execEnvOf(t, cfg, "HOME")
	// Read the operator's token through the path the CLI would compute from the
	// redirected HOME. Reading the link is not enough: what matters is that the
	// bytes arrive.
	through := filepath.Join(home, ".aws", "sso", "cache", "token.json")
	got, err := os.ReadFile(through)
	if err != nil {
		t.Fatalf("the SSO token cache is not reachable from the session's home (%s): %v — "+
			"every SSO context would stop renewing", through, err)
	}
	if !strings.Contains(string(got), "THE-SHARED-SSO-TOKEN") {
		t.Fatalf("%s does not hold the operator's token; the session has its own SSO cache and "+
			"the login in it is nobody's", through)
	}
	// And the reverse direction, which is what "shared" means: a renewal written
	// by the session has to be the operator's renewal.
	if err := os.WriteFile(through, []byte(`{"accessToken":"RENEWED"}`), 0o600); err != nil {
		t.Fatalf("writing a renewal through the session's home: %v", err)
	}
	back, err := os.ReadFile(ssoToken)
	if err != nil {
		t.Fatalf("reading the operator's own token file back: %v", err)
	}
	if !strings.Contains(string(back), "RENEWED") {
		t.Fatal("a renewal written by the session did not reach the operator's own SSO cache, " +
			"so the token is a copy and it expires separately")
	}
}

// If the link cannot be made, the redirect must not happen: fixing the
// assume-role cache by breaking every SSO context is the worse trade, and it is
// a trade nobody would see being made.
func TestTheSessionAWSHomeRefusesRatherThanBreakingSSO(t *testing.T) {
	requireSymlinks(t)
	fakeOperatorHome(t)

	workdir := t.TempDir()
	// A real directory where the link belongs. shareAWSDirectory refuses to
	// delete one — it is under a directory of ours, but somebody's session
	// tokens might be in it — so this is the failure, deterministically and
	// without needing a host that forbids symlinks.
	if err := os.MkdirAll(filepath.Join(workdir, sessionAWSHomeDir, ".aws", "sso"), 0o700); err != nil {
		t.Fatalf("seeding the obstruction: %v", err)
	}

	cfg := eksConfig()
	changed, err := newSessionAWSHome(workdir).separate(cfg)
	if err == nil {
		t.Fatal("the SSO cache could not be linked and separate said nothing about it")
	}
	if changed {
		t.Fatal("separate reported a change it could not complete")
	}
	if home, set := execEnvOf(t, cfg, "HOME"); set {
		t.Fatalf("HOME=%q was written although the SSO token cache could not be shared: every "+
			"SSO context in that session would fail to renew, and the assume-role cache it "+
			"bought is not worth that", home)
	}
}

// --- through minifyKubeconfig, on both paths out of it ------------------------

// execLoopbackConfig is an exec-authenticated context whose certificate
// authority names a file that is not there, which is what makes FlattenConfig
// fail, which sends minifyKubeconfig down its rebuild branch.
func execLoopbackConfig(unreadableCA bool) *clientcmdapi.Config {
	cfg := eksConfig()
	if unreadableCA {
		cfg.Clusters["eks"].CertificateAuthority = filepath.Join("does-not-exist", "ca.crt")
	}
	return cfg
}

// The flatten fallback throws `cfg` away and rebuilds it from `raw`, so
// anything applied before it is silently undone on exactly the path nobody
// drives.
//
// This is the mutation to try when editing minifyKubeconfig: move the
// aws.separate call above FlattenConfig and the second subtest fails alone. A
// mutation that breaks one of two cases reads like a flaky test rather than a
// caught bug.
func TestTheSessionKubeconfigCarriesItsOwnAWSHomeOnEveryPath(t *testing.T) {
	requireSymlinks(t)

	for _, c := range []struct {
		name       string
		flattening bool
	}{
		{"the flatten succeeded", true},
		{"the flatten failed and the config was rebuilt", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeOperatorHome(t)
			workdir := t.TempDir()

			content, err := minifyKubeconfig(execLoopbackConfig(!c.flattening), "eks",
				newSessionAWSHome(workdir), nil)
			if err != nil {
				t.Fatalf("minifying: %v", err)
			}
			got, err := clientcmd.Load(content)
			if err != nil {
				t.Fatalf("the result does not parse as a kubeconfig: %v", err)
			}

			want := filepath.Join(workdir, sessionAWSHomeDir)
			home, set := execEnvOf(t, got, "HOME")
			if !set {
				t.Fatalf("the session kubeconfig's exec block carries no HOME, so this session's "+
					"`aws eks get-token` writes the container's shared credential cache — two "+
					"terminals on one cluster then corrupt it between themselves, which is "+
					"the race this exists for (config: %+v)", got.AuthInfos)
			}
			if home != want {
				t.Fatalf("HOME is %q, want this session's %q", home, want)
			}
		})
	}
}

// The fallback branch above has to actually be the fallback branch, or the test
// is checking one path twice. Same alarm as
// TestAnUnreadableCertificateFallsBackRatherThanFailing, for this fixture.
func TestTheExecFixtureStillFailsTheFlatten(t *testing.T) {
	var logged []string
	if _, err := minifyKubeconfig(execLoopbackConfig(true), "eks", sessionAWSHome{},
		func(format string, args ...any) { logged = append(logged, format) }); err != nil {
		t.Fatalf("minifying: %v", err)
	}
	for _, line := range logged {
		if strings.Contains(line, "could not inline the certificates") {
			return
		}
	}
	t.Fatalf("execLoopbackConfig(true) no longer makes FlattenConfig fail, so the test above "+
		"exercises one branch twice: %v", logged)
}

// The session directory is removed when the socket closes, and the SSO link
// lives inside it. os.RemoveAll must unlink the link rather than walk through it
// — otherwise closing one terminal deletes the operator's SSO session and every
// EKS cluster goes dark at once.
func TestClosingASessionDoesNotTakeTheOperatorsSSOCache(t *testing.T) {
	requireSymlinks(t)
	_, ssoToken := fakeOperatorHome(t)

	workdir := t.TempDir()
	if _, err := newSessionAWSHome(workdir).separate(eksConfig()); err != nil {
		t.Fatalf("separating: %v", err)
	}
	if err := os.RemoveAll(workdir); err != nil {
		t.Fatalf("removing the session directory: %v", err)
	}
	if _, err := os.Stat(ssoToken); err != nil {
		t.Fatalf("the operator's SSO token is gone after one terminal closed: %v — every EKS "+
			"cluster goes dark at once and nothing says why", err)
	}
}

// --- the call sites ----------------------------------------------------------

// A predicate tested without its call site proves little. The tests above all
// drive sessionAWSHome directly; this
// one builds a real session the way the WebSocket handler does and reads the
// kubeconfig that session's kubectl will actually load.
func TestARealSessionGetsItsOwnAWSHome(t *testing.T) {
	requireSymlinks(t)
	fakeOperatorHome(t)

	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this platform")
	}

	root := t.TempDir()
	source := filepath.Join(root, "kubeconfig")
	if err := clientcmd.WriteToFile(*eksConfig(), source); err != nil {
		t.Fatalf("writing the source kubeconfig: %v", err)
	}

	term := &Terminal{
		opts: Options{
			Kubeconfig: cluster.Kubeconfig{source},
			WorkRoot:   filepath.Join(root, "work"),
			Shell:      []string{shell},
			ReadOnly:   func(string) bool { return false },
		},
		logf: func(string, ...any) {},
	}
	if err := os.MkdirAll(term.opts.WorkRoot, 0o700); err != nil {
		t.Fatalf("work root: %v", err)
	}
	if err := os.MkdirAll(term.claudeWorkspace(), 0o700); err != nil {
		t.Fatalf("claude workspace: %v", err)
	}

	for _, c := range []struct {
		name string
		open func() (*session, error)
	}{
		{"the shell", func() (*session, error) { return term.newSession("eks") }},
		{"a Claude session", func() (*session, error) {
			return term.newClaudeSession(claudeView{Cluster: "eks", ReadOnly: true})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sess, err := c.open()
			if err != nil {
				// creack/pty has no Windows implementation; the image this ships
				// in is Linux and runs go test during the build.
				t.Skipf("no session on this platform: %v", err)
			}
			defer sess.close()

			loaded, err := clientcmd.LoadFromFile(filepath.Join(sess.workdir, "kubeconfig"))
			if err != nil {
				t.Fatalf("reading the session kubeconfig back: %v", err)
			}
			home, set := execEnvOf(t, loaded, "HOME")
			if !set {
				t.Fatalf("%s opened with an exec block carrying no HOME: its `aws eks get-token` "+
					"writes the container's shared credential cache, so two of these on one "+
					"cluster corrupt it between themselves", c.name)
			}
			want := filepath.Join(sess.workdir, sessionAWSHomeDir)
			if home != want {
				t.Fatalf("%s runs its credential plugin under %q, want its own %q",
					c.name, home, want)
			}
			if _, err := os.Stat(filepath.Join(home, ".aws")); err != nil {
				t.Fatalf("%s's AWS home was named but not created: %v", c.name, err)
			}
		})
	}
}

// And the guard against the fourth session type. Two builders call
// writeSessionKubeconfig today; a third one that forgets the home would compile,
// pass every test above, and quietly put its terminal back on the shared cache.
//
// Reading the source rather than the behavior, for the same reason
// TestNothingReimplementsFirstLine does: the defect is an absence, and an
// absence has no code path to drive.
func TestEverySessionKubeconfigIsWrittenWithASessionAWSHome(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}

	calls := 0
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, "writeSessionKubeconfig(") {
				continue
			}
			// The definition itself, and the doc comment above it.
			if strings.Contains(line, "func writeSessionKubeconfig") || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			calls++
			if !strings.Contains(line, "newSessionAWSHome(") {
				t.Errorf("%s:%d writes a session kubeconfig without a session AWS home:\n\t%s\n"+
					"That session's `aws eks get-token` runs under the container's shared HOME, "+
					"so two of these on one cluster corrupt the credential cache between "+
					"themselves — awshome.go is the mechanism it must use.",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if calls < 2 {
		t.Fatalf("found %d session-kubeconfig call sites, expected at least the shell's and "+
			"Claude's — this test has stopped looking at anything", calls)
	}
}
