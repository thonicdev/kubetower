package cluster

import (
	gocontext "context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// eksKubeconfig is one EKS context whose exec block runs `aws eks get-token`
// with extra arguments: the shape `aws eks update-kubeconfig` writes, with
// invented values.
func eksKubeconfig(t *testing.T, path, extra string) {
	t.Helper()
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: example
  cluster:
    server: https://example.invalid
contexts:
- name: example-eks
  context:
    cluster: example
    user: example
users:
- name: example
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: ["--region", "eu-west-3", "eks", "get-token", "--cluster-name", "example-eks"%s]
`, extra)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// awsProfiles points the AWS SDK at a config holding two plain profiles, so
// both exec blocks below are signed in process.
func awsProfiles(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	credentials := filepath.Join(dir, "credentials")
	if err := os.WriteFile(config, []byte("[profile example-a]\nregion = eu-west-3\n[profile example-b]\nregion = eu-west-3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte("[example-a]\naws_access_key_id = AKIAEXAMPLEA\naws_secret_access_key = a\n"+
		"[example-b]\naws_access_key_id = AKIAEXAMPLEB\naws_secret_access_key = b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", config)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentials)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv(EnvAWSExec, "")
}

// An exec block corrected in the kubeconfig is what the next client signs
// with, without restarting the console.
func TestAnEditedExecBlockIsUsedWithoutARestart(t *testing.T) {
	awsProfiles(t)
	path := filepath.Join(t.TempDir(), "config")
	eksKubeconfig(t, path, `, "--profile", "example-a"`)

	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.Client("example-eks")
	if err != nil || before.EKS == nil || before.EKS.Profile != "example-a" {
		t.Fatalf("the first client: %+v, %v", before, err)
	}

	if changed, err := m.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("an unchanged kubeconfig was reloaded: %v %v", changed, err)
	}

	eksKubeconfig(t, path, `, "--prof", "example-b"`)
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("the edit was not picked up: %v %v", changed, err)
	}
	after, err := m.Client("example-eks")
	if err != nil || after.EKS == nil {
		t.Fatalf("the client after the edit: %+v, %v", after, err)
	}
	if after.EKS.Profile != "example-b" {
		t.Errorf("the client still signs as profile %q, want example-b", after.EKS.Profile)
	}
}

// Saving the same bytes again is not a reload: a reload drops every client and
// the credentials they hold.
func TestASaveWithNoChangeKeepsTheClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeKubeconfigAt(t, path, map[string]string{"alpha": "https://alpha.invalid"})
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Client("alpha")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	later := time.Now().Add(time.Minute)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, later, later)

	if changed, err := m.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if again, _ := m.Client("alpha"); again != first {
		t.Error("the client was rebuilt for a file whose contents did not change")
	}
}

// A file caught halfway through a save does not parse: the previous kubeconfig
// stays, and the finished save is read on the next call.
func TestAHalfWrittenFileKeepsThePreviousKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeKubeconfigAt(t, path, map[string]string{"alpha": "https://alpha.invalid"})
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ReloadIfChanged(); changed || err == nil {
		t.Fatalf("a file that does not parse: changed=%v err=%v", changed, err)
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("the previous kubeconfig was lost: %v", got)
	}

	writeKubeconfigAt(t, path, map[string]string{"alpha": "https://alpha.invalid", "beta": "https://beta.invalid"})
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("the finished save: changed=%v err=%v", changed, err)
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("contexts %v, want alpha and beta", got)
	}
}

// Every file in a `KUBECONFIG` list is watched, including one that does not
// exist yet when the console starts.
func TestWatchReadsAFileThatAppearsInTheList(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfigAt(t, filepath.Join(dir, "a"), map[string]string{"alpha": "https://alpha.invalid"})
	second := filepath.Join(dir, "later", "b")
	m, err := NewManager(first, second)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	done := make(chan struct{})
	var said []string
	go func() {
		m.Watch(ctx, 10*time.Millisecond, func(format string, args ...any) {
			said = append(said, fmt.Sprintf(format, args...))
		})
		close(done)
	}()

	writeKubeconfigAt(t, second, map[string]string{"beta": "https://beta.invalid"})
	deadline := time.Now().Add(5 * time.Second)
	for !m.Known("beta") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !m.Known("beta") {
		t.Fatalf("the context of a file created after start was never read; contexts %v", m.Contexts())
	}
	if len(said) != 1 {
		t.Errorf("logged %q, want one line for the one reload", said)
	}
}

// twoContexts writes alpha and beta, each on its own server, with current
// pointing at one of them.
func twoContexts(t *testing.T, path, current, betaServer string) {
	t.Helper()
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: %s
clusters:
- name: alpha
  cluster: {server: "https://alpha.invalid"}
- name: beta
  cluster: {server: %q}
contexts:
- name: alpha
  context: {cluster: alpha, user: alpha}
- name: beta
  context: {cluster: beta, user: beta}
users:
- name: alpha
  user: {token: TEST-TOKEN-alpha}
- name: beta
  user: {token: TEST-TOKEN-beta}
`, current, betaServer)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// `kubectl config use-context` and kubectx truncate the file, then write it.
// Read in between, the kubeconfig is empty: the clusters must not leave the
// console for that tick, and the write that follows is what is applied.
func TestATruncatedSaveKeepsThePreviousKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	twoContexts(t, path, "alpha", "https://beta.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now }

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("an emptied file was applied: changed=%v err=%v", changed, err)
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("the clusters left the console while the file was being rewritten: %v", got)
	}

	now = now.Add(time.Second)
	twoContexts(t, path, "beta", "https://beta.invalid")
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("the rewritten file: changed=%v err=%v", changed, err)
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("contexts %v after the rewrite", got)
	}
}

// A file really emptied stays empty, and is believed once it has lasted.
func TestAFileThatStaysEmptyIsAppliedOnceItHasLasted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	twoContexts(t, path, "alpha", "https://beta.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, step := range []time.Duration{0, 2 * time.Second} {
		now = now.Add(step)
		if changed, _ := m.ReloadIfChanged(); changed {
			t.Fatalf("applied %v after it was first seen", step)
		}
	}
	now = now.Add(2 * time.Second)
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("an empty file that lasted %v was never applied: changed=%v err=%v", settleFor+time.Second, changed, err)
	}
	if got := m.Contexts(); len(got) != 0 {
		t.Errorf("contexts %v, want none", got)
	}
}

// A file that cannot be read keeps the previous kubeconfig too.
func TestAnUnreadableFileKeepsThePreviousKubeconfig(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file modes do not refuse a read here")
	}
	path := filepath.Join(t.TempDir(), "config")
	twoContexts(t, path, "alpha", "https://beta.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if changed, err := m.ReloadIfChanged(); changed || err == nil {
		t.Fatalf("an unreadable file: changed=%v err=%v", changed, err)
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("the previous kubeconfig was lost: %v", got)
	}
}

// Switching `current-context`, which this manager never reads, keeps every
// connection; an edit to one context drops that context's clients alone.
func TestOnlyTheClientsOfAChangedContextAreRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	twoContexts(t, path, "alpha", "https://beta.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	alpha, _ := m.Client("alpha")
	beta, _ := m.Client("beta")

	twoContexts(t, path, "beta", "https://beta.invalid")
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if a, _ := m.Client("alpha"); a != alpha {
		t.Error("alpha was rebuilt for a change of current-context")
	}
	if b, _ := m.Client("beta"); b != beta {
		t.Error("beta was rebuilt for a change of current-context")
	}

	twoContexts(t, path, "beta", "https://beta-moved.invalid")
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if a, _ := m.Client("alpha"); a != alpha {
		t.Error("alpha was rebuilt for an edit to beta")
	}
	b, _ := m.Client("beta")
	if b == beta || b.Config.Host != "https://beta-moved.invalid" {
		t.Errorf("beta still answers from %s", b.Config.Host)
	}
}

// sameContext is what decides which clients survive a reload, and whether a
// client finished after its context changed is kept.
func TestSameContextTellsAMovedContextFromAnUnchangedOne(t *testing.T) {
	old := clientcmdapi.NewConfig()
	old.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://old.invalid"}
	old.AuthInfos["u"] = &clientcmdapi.AuthInfo{Token: "t"}
	old.Contexts["x"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}

	moved := clientcmdapi.NewConfig()
	moved.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://new.invalid"}
	moved.AuthInfos["u"] = &clientcmdapi.AuthInfo{Token: "t"}
	moved.Contexts["x"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}
	moved.CurrentContext = "x"

	if !sameContext(old, old, "x") || sameContext(old, moved, "x") || sameContext(old, moved, "absent") {
		t.Fatal("sameContext does not tell a moved context from an unchanged one")
	}
}
