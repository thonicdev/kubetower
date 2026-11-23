package cluster

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// homeFor is the directory a context is expected to be given.
func homeFor(base, context string) string {
	return filepath.Join(base, awsHomeKey(context))
}

// symlinksWork reports whether this machine lets this process create a directory
// symlink.
//
// Windows without the symlink privilege does not, which is the whole reason the
// separation has a fallback: sharing the SSO token cache into a redirected home
// is the one part that cannot be done any other way, and where it cannot be done
// the redirect must not happen either. So on such a host the tests that are
// *about* that link skip, and the tests about the environment run with no SSO
// directory to share — which is a real configuration, not a contrivance: an
// installation with no SSO profiles has none.
func symlinksWork(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("making a directory to link to: %v", err)
	}
	return os.Symlink(target, filepath.Join(dir, "link")) == nil
}

func requireSymlinks(t *testing.T) {
	t.Helper()
	if !symlinksWork(t) {
		t.Skip("this host does not let this process create a directory symlink, so the shared SSO cache cannot be tested here; the Linux image `docker build` runs go test in does. On Windows the privilege is SeCreateSymbolicLinkPrivilege, which Developer Mode grants — turn it on and this test runs on this machine")
	}
}

// awsHomeForTest points every path at a temporary directory, with a shared SSO
// cache beside it where the machine can link to one.
func awsHomeForTest(t *testing.T) (awsHome, string) {
	t.Helper()
	root := t.TempDir()
	shared := filepath.Join(root, "shared", ".aws")
	if err := os.MkdirAll(filepath.Join(shared, "sso", "cache"), 0o700); err != nil {
		t.Fatalf("making the shared SSO cache: %v", err)
	}
	h := awsHome{
		Base:            filepath.Join(root, "homes"),
		SSODir:          filepath.Join(shared, "sso"),
		ConfigFile:      filepath.Join(shared, "config"),
		CredentialsFile: filepath.Join(shared, "credentials"),
	}
	if !symlinksWork(t) {
		t.Log("no directory symlinks on this host; testing with no shared SSO cache")
		h.SSODir = ""
	}
	return h, root
}

func execConfig(env ...clientcmdapi.ExecEnvVar) *clientcmdapi.ExecConfig {
	return &clientcmdapi.ExecConfig{
		APIVersion:      "client.authentication.k8s.io/v1beta1",
		Command:         "aws",
		Args:            []string{"eks", "get-token", "--cluster-name", "x"},
		Env:             env,
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
	}
}

func envOf(cfg *rest.Config) map[string]string {
	out := map[string]string{}
	if cfg == nil || cfg.ExecProvider == nil {
		return out
	}
	for _, v := range cfg.ExecProvider.Env {
		out[v.Name] = v.Value
	}
	return out
}

// The table. Each row is a shape the exec block actually arrives in.
func TestTheExecEnvironmentAContextIsGiven(t *testing.T) {
	home, _ := awsHomeForTest(t)

	cases := []struct {
		name    string
		context string
		exec    *clientcmdapi.ExecConfig
		changed bool
		// want is what must be in the environment afterwards; absent is what must
		// not be.
		want   map[string]string
		absent []string
	}{
		{
			name:    "an EKS context gets its own home and the shared files pinned back",
			context: "SHOP-EKS-1_OWNER",
			exec:    execConfig(clientcmdapi.ExecEnvVar{Name: "AWS_PROFILE", Value: "prodadmin"}),
			changed: true,
			want: map[string]string{
				// AWS_PROFILE is the whole of what tells the CLI which role to
				// assume. Losing it is an authentication failure that says nothing
				// about this file.
				"AWS_PROFILE":                 "prodadmin",
				"HOME":                        homeFor(home.Base, "SHOP-EKS-1_OWNER"),
				"USERPROFILE":                 homeFor(home.Base, "SHOP-EKS-1_OWNER"),
				"AWS_CONFIG_FILE":             home.ConfigFile,
				"AWS_SHARED_CREDENTIALS_FILE": home.CredentialsFile,
			},
		},
		{
			name:    "a second context on the same profile gets a different home",
			context: "SHOP-EKS-2_OWNER",
			exec:    execConfig(clientcmdapi.ExecEnvVar{Name: "AWS_PROFILE", Value: "prodadmin"}),
			changed: true,
			want: map[string]string{
				"AWS_PROFILE": "prodadmin",
				"HOME":        homeFor(home.Base, "SHOP-EKS-2_OWNER"),
			},
		},
		{
			name:    "an exec block with no environment at all",
			context: "MI-K3S",
			exec:    execConfig(),
			changed: true,
			want:    map[string]string{"HOME": homeFor(home.Base, "MI-K3S")},
		},
		{
			name:    "an ARN survives becoming a directory name",
			context: "arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1",
			exec:    execConfig(clientcmdapi.ExecEnvVar{Name: "AWS_PROFILE", Value: "prodadmin"}),
			changed: true,
			want: map[string]string{
				"HOME": homeFor(home.Base, "arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1"),
			},
		},
		{
			name:    "an exec block that names HOME itself is left alone",
			context: "HAND-WRITTEN",
			exec:    execConfig(clientcmdapi.ExecEnvVar{Name: "HOME", Value: "/somewhere/deliberate"}),
			changed: false,
			want:    map[string]string{"HOME": "/somewhere/deliberate"},
			// Nothing else either: the whole redirect is off for this context, so
			// pinning the shared files back would be pinning them into a home this
			// code did not choose.
			absent: []string{"USERPROFILE", "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"},
		},
		{
			name:    "an exec block that already names the shared files keeps its own",
			context: "OWN-CONFIG",
			exec: execConfig(
				clientcmdapi.ExecEnvVar{Name: "AWS_CONFIG_FILE", Value: "/etc/aws/config"},
				clientcmdapi.ExecEnvVar{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/etc/aws/creds"},
			),
			changed: true,
			want: map[string]string{
				"AWS_CONFIG_FILE":             "/etc/aws/config",
				"AWS_SHARED_CREDENTIALS_FILE": "/etc/aws/creds",
				"HOME":                        homeFor(home.Base, "OWN-CONFIG"),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &rest.Config{Host: "https://example.invalid", ExecProvider: c.exec}

			dir, changed, err := home.separate(cfg, c.context)
			if err != nil {
				t.Fatalf("separate: %v", err)
			}
			if changed != c.changed {
				t.Errorf("changed = %v, want %v", changed, c.changed)
			}
			if c.changed && dir != homeFor(home.Base, c.context) {
				t.Errorf("home is %q, want %q", dir, homeFor(home.Base, c.context))
			}

			got := envOf(cfg)
			for name, value := range c.want {
				if got[name] != value {
					t.Errorf("%s = %q, want %q", name, got[name], value)
				}
			}
			for _, name := range c.absent {
				if _, ok := got[name]; ok {
					t.Errorf("%s was added and should not have been (= %q)", name, got[name])
				}
			}
		})
	}
}

// The point of the whole thing: no two contexts on one profile share a cache
// entry, because no two of them share a cache directory.
func TestNoTwoContextsGetTheSameCredentialCache(t *testing.T) {
	home, _ := awsHomeForTest(t)

	// The three that share `prodadmin` on this installation, plus the pair that
	// differ only by an _OWNER suffix — which is the collision a clipped name
	// would produce.
	contexts := []string{
		"SHOP-EKS-1", "SHOP-EKS-1_OWNER",
		"SHOP-EKS-2", "SHOP-EKS-2_OWNER",
		"arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1",
		"arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-2",
	}

	seen := map[string]string{}
	for _, name := range contexts {
		cfg := &rest.Config{ExecProvider: execConfig()}
		dir, changed, err := home.separate(cfg, name)
		if err != nil || !changed {
			t.Fatalf("%s: changed=%v err=%v", name, changed, err)
		}
		if other, ok := seen[dir]; ok {
			t.Errorf("%s and %s were given the same credential cache %s", name, other, dir)
		}
		seen[dir] = name

		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("%s: %s is not a directory: %v", name, dir, err)
		}
	}
	if len(seen) != len(contexts) {
		t.Errorf("%d contexts share %d caches", len(contexts), len(seen))
	}
}

// A context with no exec block reaches its cluster without running `aws`. There
// is no cache for it to corrupt and nothing here to do; touching it would be
// putting a HOME on a path that has no process to read it.
func TestAContextWithNoExecBlockIsUntouched(t *testing.T) {
	home, _ := awsHomeForTest(t)

	cfg := &rest.Config{Host: "https://127.0.0.1:6443"}
	dir, changed, err := home.separate(cfg, "docker-desktop")
	if err != nil {
		t.Fatalf("separate: %v", err)
	}
	if changed || dir != "" {
		t.Errorf("a context with no exec block was given %q (changed=%v)", dir, changed)
	}
	if cfg.ExecProvider != nil {
		t.Error("separate invented an exec block")
	}
	if _, err := os.Stat(home.Base); err == nil {
		t.Error("a directory was created for a context that never runs aws")
	}

	// And a nil config is not a dereference, the same way redirectLoopback's is
	// not.
	if _, changed, err := home.separate(nil, "docker-desktop"); changed || err != nil {
		t.Errorf("a nil config reported changed=%v err=%v", changed, err)
	}
}

// Two clients are built per context — one for lists, one for streams — and
// rest.CopyConfig shares the *api.ExecConfig by pointer rather than deep-copying
// it. So this has to be safe to apply twice to one exec block.
func TestApplyingItTwiceLeavesOneHome(t *testing.T) {
	home, _ := awsHomeForTest(t)

	cfg := &rest.Config{ExecProvider: execConfig(clientcmdapi.ExecEnvVar{Name: "AWS_PROFILE", Value: "toolsadmin"})}
	if _, _, err := home.separate(cfg, "BUILD-EKS-1_OWNER"); err != nil {
		t.Fatalf("first separate: %v", err)
	}
	before := len(cfg.ExecProvider.Env)

	if _, changed, err := home.separate(cfg, "BUILD-EKS-1_OWNER"); err != nil || changed {
		t.Fatalf("second separate: changed=%v err=%v", changed, err)
	}
	if got := len(cfg.ExecProvider.Env); got != before {
		t.Errorf("the environment grew from %d to %d entries on a second pass", before, got)
	}

	count := 0
	for _, v := range cfg.ExecProvider.Env {
		if v.Name == "HOME" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d HOME entries; the last one wins in os/exec, but two is a bug either way", count)
	}
}

// The SSO session token is renewed inside ~/.aws/sso/cache, and
// every EKS context on an SSO profile depends on that file. There is no
// environment variable for it, so the shared directory has to be inside the new
// home.
func TestTheSSOTokenCacheStaysShared(t *testing.T) {
	requireSymlinks(t)
	home, _ := awsHomeForTest(t)

	// A token the operator's own `aws sso login` would have written.
	token := filepath.Join(home.SSODir, "cache", "946d9ed4319dab98.json")
	if err := os.WriteFile(token, []byte(`{"accessToken":"redacted"}`), 0o600); err != nil {
		t.Fatalf("writing the shared token: %v", err)
	}

	for _, name := range []string{"SHOP-EKS-1", "BUILD-EKS-1"} {
		cfg := &rest.Config{ExecProvider: execConfig()}
		dir, changed, err := home.separate(cfg, name)
		if err != nil || !changed {
			t.Fatalf("%s: changed=%v err=%v", name, changed, err)
		}

		link := filepath.Join(dir, ".aws", "sso")
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("%s: %s is not a link: %v", name, link, err)
		}
		if target != home.SSODir {
			t.Errorf("%s: the SSO cache points at %q, want %q", name, target, home.SSODir)
		}

		// Reachable through the new home by the path botocore actually builds,
		// which is the only question that matters.
		through := filepath.Join(dir, ".aws", "sso", "cache", "946d9ed4319dab98.json")
		if _, err := os.Stat(through); err != nil {
			t.Errorf("%s: the shared token is not readable at %s: %v", name, through, err)
		}

		// And renewal has to be able to write back into the operator's own copy,
		// not into a private one.
		renewed := filepath.Join(dir, ".aws", "sso", "cache", "renewed.json")
		if err := os.WriteFile(renewed, []byte("{}"), 0o600); err != nil {
			t.Fatalf("%s: renewing through the link: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(home.SSODir, "cache", "renewed.json")); err != nil {
			t.Errorf("%s: a renewed token did not land in the shared cache: %v", name, err)
		}
		if err := os.Remove(renewed); err != nil {
			t.Fatalf("cleaning up: %v", err)
		}
	}
}

// The bad trade this must never make: fixing the assume-role cache by breaking
// SSO. If the shared token cache cannot be put inside the new home, the redirect
// does not happen at all.
func TestTheRedirectIsRefusedWhenTheSSOCacheCannotBeShared(t *testing.T) {
	requireSymlinks(t)
	home, _ := awsHomeForTest(t)

	// A real directory where the link belongs — the shape a half-finished earlier
	// run would leave, and the one case where deleting would destroy tokens.
	dir := homeFor(home.Base, "SHOP-EKS-1")
	if err := os.MkdirAll(filepath.Join(dir, ".aws", "sso", "cache"), 0o700); err != nil {
		t.Fatalf("setting up: %v", err)
	}

	cfg := &rest.Config{ExecProvider: execConfig(clientcmdapi.ExecEnvVar{Name: "AWS_PROFILE", Value: "prodDevOps"})}
	_, changed, err := home.separate(cfg, "SHOP-EKS-1")
	if err == nil {
		t.Fatal("a home whose SSO cache could not be shared was accepted")
	}
	if changed {
		t.Error("the redirect happened anyway, which breaks every SSO context to fix the assume-role ones")
	}
	if got := envOf(cfg); got["HOME"] != "" {
		t.Errorf("HOME was set to %q despite the failure", got["HOME"])
	}
	if got := envOf(cfg)["AWS_PROFILE"]; got != "prodDevOps" {
		t.Errorf("AWS_PROFILE is %q, want %q — the refusal must leave the block as it found it", got, "prodDevOps")
	}
}

// A machine that has never signed in to AWS SSO has no ~/.aws/sso when the
// console starts, and the first `aws sso login` creates it afterwards. The
// private home built before that login must still see the session it writes:
// otherwise the plugin reads an empty cache of its own and the context fails
// as an expired session until the console is restarted.
func TestASessionCreatedAfterStartIsSeenThroughThePrivateHome(t *testing.T) {
	requireSymlinks(t)
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	t.Setenv("AWS_CONFIG_FILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	shared := filepath.Join(user, ".aws", "sso")

	home := defaultAWSHome()
	home.Base = filepath.Join(t.TempDir(), "homes")
	if home.Off != "" {
		t.Fatalf("the separation is off on a host that can symlink: %s", home.Off)
	}

	cfg := &rest.Config{ExecProvider: execConfig()}
	dir, changed, err := home.separate(cfg, "SHOP-EKS-1")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if info, err := os.Stat(shared); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("the operator's SSO cache directory was not made 0700: %v %v", info, err)
	}

	// `aws sso login`, in a terminal, after the client was built.
	if err := os.MkdirAll(filepath.Join(shared, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "cache", "946d9ed4319dab98.json"), []byte(`{"accessToken":"redacted"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	through := filepath.Join(dir, ".aws", "sso", "cache", "946d9ed4319dab98.json")
	if _, err := os.Stat(through); err != nil {
		t.Fatalf("the session written after start is not readable where the plugin looks (%s): %v", through, err)
	}
}

// A home directory that cannot hold ~/.aws/sso refuses the redirect, as a link
// that cannot be made does: the plugin then reads the operator's own files.
func TestNoSSODirectoryThatCanBeMadeRefusesTheRedirect(t *testing.T) {
	home, root := awsHomeForTest(t)
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home.SSODir = filepath.Join(blocker, "sso")

	cfg := &rest.Config{ExecProvider: execConfig()}
	if _, changed, err := home.separate(cfg, "STAGING-EKS-1_OWNER"); err == nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := envOf(cfg)["HOME"]; got != "" {
		t.Errorf("HOME was set to %q despite the failure", got)
	}
}

// An empty SSODir shares nothing, and that is not a failure.
func TestNoSSODirectoryIsNotAFailure(t *testing.T) {
	home, _ := awsHomeForTest(t)
	home.SSODir = ""

	cfg := &rest.Config{ExecProvider: execConfig()}
	dir, changed, err := home.separate(cfg, "STAGING-EKS-1_OWNER")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".aws", "sso")); err == nil {
		t.Error("a link was made to nothing")
	}
}

// A plugin that is not the AWS CLI (kubelogin, gke-gcloud-auth-plugin) has no
// SSO cache to share: nothing is created in the operator's home for it.
func TestNoSSOCacheIsCreatedForAnotherPlugin(t *testing.T) {
	home, root := awsHomeForTest(t)
	home.SSODir = filepath.Join(root, "user", ".aws", "sso")

	exec := execConfig()
	exec.Command = "kubelogin"
	exec.Args = []string{"get-token"}
	cfg := &rest.Config{ExecProvider: exec}
	if _, changed, err := home.separate(cfg, "AKS-1"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "user", ".aws")); err == nil {
		t.Error("~/.aws was created for a plugin that is not the AWS CLI")
	}
}

// Where the link cannot be made (a Windows host without the symlink
// privilege), a directory created here is taken back out and the context
// keeps its private home without the link, as such a host always had.
func TestASSOCacheThatCannotBeLinkedIsTakenBackOut(t *testing.T) {
	home, root := awsHomeForTest(t)
	user := filepath.Join(root, "user")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	home.SSODir = filepath.Join(user, ".aws", "sso")
	// A real directory where the link belongs: the link fails on every platform.
	if err := os.MkdirAll(filepath.Join(homeFor(home.Base, "SHOP-EKS-1"), ".aws", "sso"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &rest.Config{ExecProvider: execConfig()}
	_, changed, err := home.separate(cfg, "SHOP-EKS-1")
	if err != nil || !changed {
		t.Fatalf("the context lost its private home: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(user, ".aws")); err == nil {
		t.Error("~/.aws was left behind by a link that could not be made")
	}
}

// Base empty is the switch that turns the whole thing off — no home directory to
// read the shared config out of, so no redirect that could find a profile.
func TestWithNoBaseNothingIsRedirected(t *testing.T) {
	cfg := &rest.Config{ExecProvider: execConfig(clientcmdapi.ExecEnvVar{Name: "AWS_PROFILE", Value: "prodadmin"})}

	if _, changed, err := (awsHome{}).separate(cfg, "SHOP-EKS-1_OWNER"); changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := envOf(cfg); len(got) != 1 || got["AWS_PROFILE"] != "prodadmin" {
		t.Errorf("the exec block was modified with the separation disabled: %v", got)
	}
}

func TestADirectoryNameIsRecognisableAndUnique(t *testing.T) {
	long := "arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1"

	key := awsHomeKey(long)
	if len(key) > awsHomeNameLen+1+awsHomeKeyHexLen {
		t.Errorf("%q is longer than the cap allows", key)
	}
	if strings.ContainsAny(key, `:/\`) {
		t.Errorf("%q is not a filename", key)
	}
	if !strings.HasPrefix(key, "arn_aws_eks_eu-central-1_123456789012_cluster") {
		t.Errorf("%q does not name the context it belongs to", key)
	}
	if awsHomeKey(long) != key {
		t.Error("the same context got two different directories")
	}

	// Two names that reduce to the same readable half, and must not reduce to the
	// same directory. This is what the digest is for.
	a := awsHomeKey("arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1-with-a-very-long-tail-A")
	b := awsHomeKey("arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1-with-a-very-long-tail-B")
	if a == b {
		t.Errorf("two contexts differing past the clip collided on %q", a)
	}

	// And a name with nothing usable left in it still gets a directory.
	if got := safeDirName("::::"); got != "context" {
		t.Errorf("safeDirName(%q) = %q", "::::", got)
	}
}

// -----------------------------------------------------------------------------
// The path that matters.
//
// Every test above reads the *config*, and reading the config is exactly the
// check that would not have caught the mistake this is guarding against: the
// injection has to happen before rest.HTTPClientFor, because that is where
// client-go hands the exec configuration to the credential plugin's
// authenticator. An injection made afterwards sits in Client.Config, satisfies
// every assertion above, and never reaches the `aws` process.
//
// So this one goes the whole way: a real Manager over a real kubeconfig, a real
// rest.Config, a real request through client-go, and the credential plugin —
// this test binary in helper mode — writing down the HOME it was actually
// started with.
// -----------------------------------------------------------------------------

// pluginRecordEnv names the file the helper appends the HOME it received to.
// It travels through the process environment, which is what client-go's exec
// authenticator puts *before* the exec block's own entries.
const pluginRecordEnv = "KT_TEST_PLUGIN_RECORD"

// TestExecPluginHelperProcess is the credential plugin. Outside the helper role
// it is not a test at all and returns immediately.
func TestExecPluginHelperProcess(t *testing.T) {
	record := os.Getenv(pluginRecordEnv)
	if record == "" {
		return
	}

	f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintf(f, "%s\n", os.Getenv("HOME"))
		f.Close()
	}

	fmt.Print(`{"kind":"ExecCredential","apiVersion":"client.authentication.k8s.io/v1beta1",` +
		`"status":{"token":"token-from-the-plugin"}}`)
	// Exit before the testing framework prints anything of its own: client-go
	// decodes the whole of stdout, and a trailing "PASS" makes it two documents —
	// the same shape as a corrupted cache entry.
	os.Exit(0)
}

func TestTheExecPluginRunsWithItsOwnHome(t *testing.T) {
	// httptest listens on the loopback, and inside the image that runs `go test`
	// the loopback rewrite would send this somewhere else entirely.
	t.Setenv("KT_IN_CONTAINER", "0")

	seen := make(chan string, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	record := filepath.Join(t.TempDir(), "homes.txt")
	t.Setenv(pluginRecordEnv, record)

	// Two contexts, one shared AWS profile, byte-identical exec blocks and one
	// cluster. That is the corrupting shape: without the separation client-go
	// files both under one authenticator and both `aws` runs write one cache
	// entry.
	const alpha, beta = "SHOP-EKS-1_OWNER", "SHOP-EKS-2_OWNER"
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte(kubeconfigYAML(server.URL, self, alpha, beta)), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}

	m, err := NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	home, _ := awsHomeForTest(t)
	m.awsHome = home

	for _, name := range []string{alpha, beta} {
		client, err := m.Client(name)
		if err != nil {
			t.Fatalf("%s: Client: %v", name, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if _, err := client.Clients.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(ctx); err != nil {
			cancel()
			t.Fatalf("%s: the request through the exec plugin failed: %v", name, err)
		}
		cancel()

		if auth := <-seen; auth != "Bearer token-from-the-plugin" {
			t.Errorf("%s: the server saw Authorization %q — the plugin's token did not get used", name, auth)
		}
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the credential plugin never ran: %v", err)
	}
	got := strings.Fields(string(raw))
	sort.Strings(got)
	want := []string{homeFor(home.Base, alpha), homeFor(home.Base, beta)}
	sort.Strings(want)

	if len(got) != 2 {
		t.Fatalf("the plugin ran %d time(s) with HOME %v; two contexts sharing one profile must run it twice, "+
			"under two different homes — one run means they shared a credential cache", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("the plugin ran with HOME %q, want %q", got[i], want[i])
		}
	}
	t.Logf("the credential plugin ran under:\n  %s", strings.Join(got, "\n  "))
}

// kubeconfigYAML writes two contexts onto one cluster with identical exec
// blocks, which is the shape two contexts sharing an AWS profile arrive in.
func kubeconfigYAML(server, command, alpha, beta string) string {
	// httptest hands back a URL; the kubeconfig wants it verbatim.
	if _, err := url.Parse(server); err != nil {
		panic(err)
	}
	user := func(name string) string {
		return fmt.Sprintf(`- name: %s
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: %q
      args: ["-test.run=^TestExecPluginHelperProcess$"]
      env:
      - name: AWS_PROFILE
        value: prodadmin
      interactiveMode: Never
`, name, command)
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: shared
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: %s
  context:
    cluster: shared
    user: alpha
- name: %s
  context:
    cluster: shared
    user: beta
users:
%s%s`, server, alpha, beta, user("alpha"), user("beta"))
}

// TestASeparationThatCannotWorkCreatesNothing.
//
// On a Windows host where the separation cannot succeed, nothing may be left
// under the temporary AWS base — not one directory per context per start.
//
// The mechanism needs a symlink to share the SSO token cache into each private
// home, and a host either grants that privilege or does not: it is one answer for
// the whole process, not a question to ask per cluster. Off is that answer, and
// this is what it must produce — no directory, no error, and no change to the
// exec block.
func TestASeparationThatCannotWorkCreatesNothing(t *testing.T) {
	base := t.TempDir()
	h := awsHome{
		Base:   base,
		SSODir: filepath.Join(base, "sso-source"),
		Off:    "this host cannot create the symlink that shares the SSO token cache",
	}
	cfg := &rest.Config{ExecProvider: &clientcmdapi.ExecConfig{
		Command: "aws",
		Env:     []clientcmdapi.ExecEnvVar{{Name: "AWS_PROFILE", Value: "prod"}},
	}}

	dir, changed, err := h.separate(cfg, "SHOP-EKS-1")
	if err != nil {
		t.Fatalf("a mechanism that is switched off reported an error: %v", err)
	}
	if changed || dir != "" {
		t.Errorf("separate reported a change (%q, %v) while switched off", dir, changed)
	}
	if len(cfg.ExecProvider.Env) != 1 || cfg.ExecProvider.Env[0].Name != "AWS_PROFILE" {
		t.Errorf("the exec block was rewritten: %v", cfg.ExecProvider.Env)
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("separate left %q behind. One per context per start, under a temporary "+
			"directory nothing prunes, is how fourteen of them accumulated", e.Name())
	}
}

// TestARefusedSeparationTakesItsDirectoryBackOut. The other half: when the
// per-host answer was yes and this particular context still fails, what was
// created for it must go. Nothing will ever read it — the redirect was refused —
// so leaving it is pure accumulation.
func TestARefusedSeparationTakesItsDirectoryBackOut(t *testing.T) {
	base := t.TempDir()
	// An SSO source that cannot be made, so sharing it fails **on every
	// platform**: its parent is a file. Outside base, so that what is left under
	// base is only what the refusal failed to take back.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := awsHome{Base: base, SSODir: filepath.Join(blocker, "sso")}
	cfg := &rest.Config{ExecProvider: &clientcmdapi.ExecConfig{Command: "aws"}}

	if _, changed, err := h.separate(cfg, "SHOP-EKS-1"); err == nil {
		t.Fatal("sharing an SSO directory that cannot exist was reported as success")
	} else if changed {
		t.Error("separate reported a change on a path that failed")
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a refused separation left %q behind", e.Name())
	}
}

// TestTheSymlinkProbeCleansUpAfterItself. It exists for one syscall's worth of an
// answer; a probe that leaves a directory is the defect it exists to prevent.
func TestTheSymlinkProbeCleansUpAfterItself(t *testing.T) {
	base := filepath.Join(t.TempDir(), "made-by-the-probe")

	err := canSymlink(base)
	// The answer itself is the host's business — Windows without the privilege
	// says no, and that is a valid outcome rather than a test failure.
	t.Logf("canSymlink on this host: %v", err)

	entries, readErr := os.ReadDir(base)
	if readErr != nil {
		t.Fatalf("the probe did not create its base: %v", readErr)
	}
	for _, e := range entries {
		t.Errorf("the probe left %q behind", e.Name())
	}
}
