package boundary_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"boundary.kubetower.invalid"
	"github.com/thonicdev/kubetower/app"
	"golang.org/x/crypto/bcrypt"
)

// TestAnExtensionHandedToRunIsMountedBehindTheGate is the seam, exercised from
// the only side that proves anything about it.
//
// The rest of this module proves an extension can be *built* and *registered*
// from outside the core. That left the step a binary carrying extras actually
// needs unproved: nothing outside the core could start the console, because the
// start-up was a `package main`. So this starts the real one — the same
// `app.Run` `cmd/kubetower` calls, with the flags `task dev` passes — hands it
// the consumer, signs in through the real front door, and asks the real router
// for the consumer's route on two clusters: one where it is ticked, one where it
// is not.
//
// **What it proves, and what it does not.** That an extension handed to
// `app.Run` is registered, mounted under `/x/{cluster}/{key}/`, behind the
// session and behind the per-cluster gate, in a console started by code that
// cannot import `internal/`. The two gate contexts reach no cluster: they point
// at a port nothing listens on, and the route asked for is the one that needs
// none. The gate's own sentences, the settings grid and the router's refusals
// are `internal/web`'s tests, and none of that is repeated here.
//
// **And that a write through Host.Client never reaches a cluster**, on a third context that does answer: a recording fake
// whose access reviews deny everybody. The consumer tries a typed delete and a
// dynamic create through the clients the real console hands it; both must come
// back refused by the console, and the fake must have received no change at
// all. In this console and not in a stub, because the refusal is the cluster
// manager's guard on the config those clients were built from - a stub host
// would only prove the stub. Removing `access.Guard` from
// `internal/cluster/manager.go` makes the fake receive the delete and the
// create, and this test fail on both.
func TestAnExtensionHandedToRunIsMountedBehindTheGate(t *testing.T) {
	// The registry is process-wide, and TestRegisterAccepts… registers the same
	// consumer; handing it to Run a second time would be the duplicate refusal.
	app.Reset()
	t.Cleanup(app.Reset)

	// Not t.TempDir: the console opens its log file inside the state directory
	// and holds it for the life of the process, on purpose (logfile.go), and
	// Windows refuses to remove a directory holding an open file. A cleanup that
	// fails the test for that would be failing it for the behavior it wants, so
	// on Windows the directory and its log stay behind in the temp directory.
	state, err := os.MkdirTemp("", "kubetower-seam-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })

	// The one cluster here that answers, for the write assertion below.
	fake := newRecordingCluster(t)

	const on, off, writes = "on-cluster", "off-cluster", "writes-cluster"
	kubeconfig := filepath.Join(state, "kubeconfig")
	writeFile(t, kubeconfig, fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
- name: fake
  cluster:
    server: %s
users:
- name: nobody
  user:
    token: not-a-token
contexts:
- name: %s
  context: {cluster: nowhere, user: nobody}
- name: %s
  context: {cluster: nowhere, user: nobody}
- name: %s
  context: {cluster: fake, user: nobody}
current-context: %s
`, fake.URL, on, off, writes, on))
	// On on one cluster and turned off on the other, in the file the Settings
	// page writes, so the gate has both answers to give - and on on the fake, so
	// the write probe is served rather than refused by the gate. In the current
	// format, with the off written as a choice: on the desktop a cluster nobody
	// chose anything on has every extension on, so a file
	// that only ticked would leave off-cluster on.
	writeFile(t, filepath.Join(state, "extras.json"),
		fmt.Sprintf(`{"format":2,"on":{%q:{%q:true},%q:{%q:false},%q:{%q:true}}}`,
			on, boundary.Key, off, boundary.Key, writes, boundary.Key))

	const password = "the-seam-is-open"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	addr := freeAddress(t)

	// Whatever the machine running this has set must not decide the answer:
	// every KT_ variable in the environment is unset first, and the named ones
	// even when absent, so a host exporting a Valkey address, a replica count,
	// an identity provider or an ephemeral state directory gets the same console
	// as CI. t.Setenv records the old value and puts it back after the test;
	// the Unsetenv makes it absent rather than empty, which is what "not set"
	// means to every reader of these.
	unset := map[string]bool{}
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "KT_") {
			unset[name] = true
		}
	}
	for _, name := range []string{
		"KT_REPLICATED", "KT_VALKEY_ADDR", "KT_VALKEY_USERNAME", "KT_VALKEY_PASSWORD",
		"KT_STATE_PERSISTENT", "KT_OIDC_ISSUER", "KT_OIDC_CLIENT_ID", "KT_OIDC_CLIENT_SECRET",
		"KT_OIDC_DISCOVERY_URL", "KT_OIDC_REDIRECT_URL", "KT_OIDC_SCOPES", "KT_OIDC_USERNAME_CLAIM",
		"KT_OIDC_USERNAME_PREFIX", "KT_OIDC_GROUPS_CLAIM", "KT_OIDC_GROUPS_PREFIX", "KT_OIDC_SESSION_MAX", "KT_AUTH_LOCAL_ACCOUNT",
		"KT_USER", "KT_EXTRAS", "KT_READONLY_CLUSTERS", "KT_SESSION_TTL", "KT_SESSION_MAX",
	} {
		unset[name] = true
	}
	for name := range unset {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}

	// The fake listens on the loopback; inside a container the console would
	// otherwise move a loopback address to the host's (internal/cluster's
	// loopback.go), and the fake would never be asked anything.
	t.Setenv("KT_IN_CONTAINER", "0")
	t.Setenv("KT_STATE_DIR", state)
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("KT_LISTEN", addr)
	t.Setenv("KT_PASSWORD_HASH", string(hash))
	t.Setenv("KT_SESSION_SECRET", strings.Repeat("s", 32))

	// A front end of the caller's own, the shape a binary carrying extras
	// brings: the root of a Vite build, with an inline script the console has
	// to hash into its policy. Its own words, so an answer from the core's
	// embedded bundle cannot pass for it.
	const inline = "window.boundaryShell = 'the caller brought this';"
	const shellJS = "export const boundaryShell = true;"
	frontEnd := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><head><script>" + inline +
			"</script><script type=\"module\" src=\"/assets/shell-AAAABBBB.js\"></script></head>" +
			"<body>the boundary shell</body></html>")},
		"assets/shell-AAAABBBB.js": &fstest.MapFile{Data: []byte(shellJS)},
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			Args: []string{"--no-window", "--unattended"},
			UI:   frontEnd,
		}, boundary.Consumer{})
	}()

	base := "http://" + addr
	waitForConsole(t, base, done)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Before signing in: the extension's route is behind the front door like
	// every other, because the core mounts it inside the guard.
	if code, body := get(t, client, base+"/x/"+on+"/"+boundary.Key+"/socket"); code == http.StatusOK {
		t.Fatalf("the extension answered before anybody signed in: %d %q", code, body)
	}

	login, err := http.NewRequest(http.MethodPost, base+"/api/auth/login",
		strings.NewReader(fmt.Sprintf(`{"user":"admin","password":%q}`, password)))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-KubeTower-Request", "1")
	resp, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("signing in answered %d, want 204", resp.StatusCode)
	}

	// Ticked here: the consumer's own handler answers, under its own key.
	code, body := get(t, client, base+"/x/"+on+"/"+boundary.Key+"/socket")
	if code != http.StatusOK || !strings.HasPrefix(body, "token=") {
		t.Errorf("on %s, where it is ticked: %d %q, want 200 and the consumer's own answer", on, code, body)
	}

	// Not ticked here: the gate refuses, and the handler is never reached. The
	// refusal names the page by the label the consumer declared, which is what
	// tells the gate's 404 apart from an address nothing is mounted at.
	code, body = get(t, client, base+"/x/"+off+"/"+boundary.Key+"/socket")
	label := boundary.Consumer{}.Label()
	if code != http.StatusNotFound || strings.HasPrefix(body, "token=") || !strings.Contains(body, label) {
		t.Errorf("on %s, where it is not ticked: %d %q, want the gate's 404 naming %q", off, code, body, label)
	}

	// The setting the consumer declared, from outside the core, is listed with
	// its default and written through the console's own route, per cluster.
	if code, body := get(t, client, base+"/api/c/"+off+"/extensions"); code != http.StatusOK ||
		!strings.Contains(body, `"key":"`+boundary.SettingKey+`"`) || !strings.Contains(body, `"value":true`) {
		t.Errorf("the listing on %s answered %d %q, want the consumer's setting at its default", off, code, body)
	}
	put, err := http.NewRequest(http.MethodPut,
		base+"/api/c/"+off+"/extensions/"+boundary.Key+"/settings/"+boundary.SettingKey,
		strings.NewReader(`{"value":false}`))
	if err != nil {
		t.Fatal(err)
	}
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("X-KubeTower-Request", "1")
	if resp, err := client.Do(put); err != nil {
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("writing the consumer's setting answered %d, want 204", resp.StatusCode)
		}
	}
	if code, body := get(t, client, base+"/api/c/"+off+"/extensions"); code != http.StatusOK ||
		!strings.Contains(body, `"value":false`) {
		t.Errorf("after the write the listing on %s answered %d %q, want the setting false", off, code, body)
	}
	if _, body := get(t, client, base+"/api/c/"+on+"/extensions"); !strings.Contains(body, `"value":true`) {
		t.Errorf("a setting written on %s changed %s: %q", off, on, body)
	}

	// A write through Host.Client, from the consumer, on the cluster that
	// answers. Both refused by the console - in its own words, never as the
	// cluster's 403 - and nothing that changes anything received by the fake.
	code, body = get(t, client, base+"/x/"+writes+"/"+boundary.Key+"/?write=1")
	if code != http.StatusOK || strings.Count(body, "refused: ") != 2 ||
		strings.Count(body, "answer which kinds the cluster serves") != 2 {
		t.Errorf("a write through Host.Client on %s answered %d %q, want both the typed and the dynamic "+
			"write refused by the console", writes, code, body)
	}
	for _, request := range fake.requests() {
		if method, _, _ := strings.Cut(request, " "); method != http.MethodGet && !strings.HasSuffix(request, "accessreviews") {
			t.Errorf("the cluster received %q from a console whose extension may not write", request)
		}
	}

	// A read of an object through Host.Client: refused by the console, both
	// typed and dynamic, before anything is asked; discovery goes through.
	code, body = get(t, client, base+"/x/"+writes+"/"+boundary.Key+"/?read=1")
	typedRead, rest, _ := strings.Cut(body, "\n")
	dynamicRead, discovered, _ := strings.Cut(rest, "\n")
	if code != http.StatusOK || !strings.Contains(typedRead, "answer which kinds the cluster serves") ||
		!strings.Contains(dynamicRead, "answer which kinds the cluster serves") ||
		strings.Contains(discovered, "answer which kinds the cluster serves") {
		t.Errorf("a read through Host.Client on %s answered %d %q, want both object reads refused by the "+
			"console and discovery sent", writes, code, body)
	}
	askedDiscovery := false
	for _, request := range fake.requests() {
		if strings.Contains(request, "/configmaps") {
			t.Errorf("the cluster was asked %q: a read through Host.Client left the console", request)
		}
		askedDiscovery = askedDiscovery || request == "GET /api/v1"
	}
	if !askedDiscovery {
		t.Errorf("discovery through Host.Client never reached the cluster: %q", fake.requests())
	}

	// What the configuration names, read as the console: the cluster here
	// refuses the console's own review too, so nothing is read; and a name the
	// consumer's Configured does not hold is refused before any review.
	for _, door := range []struct{ path, want string }{
		{"/configured-secret", "configured=not permitted"},
		{"/configured-secret?name=another", "configured=not configured"},
		{"/configured-service", "service=not permitted"},
		{"/configured-service?name=another", "service=not configured"},
	} {
		code, body = get(t, client, base+"/x/"+writes+"/"+boundary.Key+door.path)
		if code != http.StatusOK || body != door.want {
			t.Errorf("%s on %s answered %d %q, want %q", door.path, writes, code, body, door.want)
		}
	}

	// A Secret's value through Host.Reveal, for a person the cluster refuses:
	// refused as not permitted - a sentinel the consumer
	// names from its side - and the Secret never asked for. And a list of
	// Secrets through Host.List, refused before anything is read.
	code, body = get(t, client, base+"/x/"+writes+"/"+boundary.Key+"/reveal")
	if code != http.StatusOK || body != "reveal=not permitted" {
		t.Errorf("a reveal on %s, where nobody may read a Secret, answered %d %q, want reveal=not permitted",
			writes, code, body)
	}
	code, body = get(t, client, base+"/x/"+writes+"/"+boundary.Key+"/list?resource=secrets")
	if code != http.StatusOK || body != "list=secrets refused" {
		t.Errorf("a list of Secrets on %s answered %d %q, want list=secrets refused", writes, code, body)
	}
	for _, request := range fake.requests() {
		if strings.Contains(request, "/secrets") {
			t.Errorf("the cluster was asked %q: a refused reveal or a refused list read a Secret", request)
		}
	}

	// A read refused for the person, through each door the contract documents
	// it on, classified from this side with errors.Is against
	// extension.ErrDenied: the cluster here answers every access review no.
	// Each is a refusal and not a failure, and nothing behind the review was
	// read.
	for _, door := range []struct{ path, want string }{
		{"/list?namespace=default", "list=denied"},
		{"/usage", "usage=denied"},
		{"/forward", "forward=denied"},
	} {
		code, body = get(t, client, base+"/x/"+writes+"/"+boundary.Key+door.path)
		if code != http.StatusOK || body != door.want {
			t.Errorf("%s on %s, where the cluster refuses every review, answered %d %q, want %q",
				door.path, writes, code, body, door.want)
		}
	}
	for _, request := range fake.requests() {
		if strings.Contains(request, "/configmaps") || strings.Contains(request, "/nodes") ||
			strings.Contains(request, "/services") || strings.Contains(request, "/pods") {
			t.Errorf("the cluster was asked %q: a refused read was made anyway", request)
		}
	}

	// The caller's front end, at the root and at a deep link the client router
	// owns, and a policy whose script-src names the hash of *its* inline script -
	// the one thing that goes wrong silently when the policy is hashed from
	// another bundle than the one served.
	sum := sha256.Sum256([]byte(inline))
	wantHash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for _, path := range []string{"/", "/c/" + on + "/pods"} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "the boundary shell") {
			t.Errorf("%s: %d %q, want 200 and the front end handed to app.Run", path, resp.StatusCode, raw)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, wantHash) {
			t.Errorf("%s: Content-Security-Policy %q does not name %s, the hash of the served shell's "+
				"inline script, so a browser would block it", path, csp, wantHash)
		}
	}
	code, body = get(t, client, base+"/assets/shell-AAAABBBB.js")
	if code != http.StatusOK || body != shellJS {
		t.Errorf("/assets/shell-AAAABBBB.js: %d %q, want the caller's asset", code, body)
	}

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("app.Run returned %v after its context was canceled, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("app.Run did not return within 15s of its context being canceled")
	}
}

// waitForConsole polls the sign-in methods, which answer before any session,
// until the console is up — or fails at once if Run has already returned.
func waitForConsole(t *testing.T, base string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("app.Run returned before the console answered: %v", err)
		default:
		}
		resp, err := http.Get(base + "/api/auth/methods")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the console did not answer %s/api/auth/methods within 60s", base)
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// freeAddress asks the kernel for a port and gives it back. Racy in principle —
// something else could take it between the two — and acceptable here, because
// the alternative is a listener parameter on app.Run that exists only for this
// test.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recordingCluster is an API server that records every request it receives
// and refuses everybody: an access review answers denied - the person the
// review would refuse - and anything else is a 404. What it records is the
// assertion, so it answers just enough for the console to keep asking.
type recordingCluster struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newRecordingCluster(t *testing.T) *recordingCluster {
	t.Helper()
	c := &recordingCluster{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		c.mu.Lock()
		c.seen = append(c.seen, r.Method+" "+r.URL.Path)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "accessreviews") {
			_, _ = io.WriteString(w, `{"kind":"SelfSubjectAccessReview","apiVersion":"authorization.k8s.io/v1",`+
				`"status":{"allowed":false,"reason":"this cluster refuses everybody"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *recordingCluster) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}
