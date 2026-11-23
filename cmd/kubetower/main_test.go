package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/app"
	"github.com/thonicdev/kubetower/extras/argocd"
	"github.com/thonicdev/kubetower/web"
	"golang.org/x/crypto/bcrypt"
)

// runAsConsole is the environment variable that turns this test binary into
// the console binary itself: TestMain sees it and calls main. The end-to-end
// test below starts the program an operator runs, not a copy of its start-up
// assembled for the test.
const runAsConsole = "KT_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runAsConsole) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The list main hands over is the list the core accepts. This goes through the
// core's registry, so its refusals apply: an empty key, a duplicate key, an old
// key that collides with a live one.
func TestTheBinaryRegistersItsExtras(t *testing.T) {
	app.Reset()
	t.Cleanup(app.Reset)

	if err := app.Register(extras()...); err != nil {
		t.Fatalf("the core refused this binary's extras: %v", err)
	}
	if got := app.Keys(); !slices.Equal(got, []string{"argocd", "certmanager", "cnpg", "dns", "events", "kubeadjust", "kubecron", "netinspect", "pending", "redis", "runtime", "security"}) {
		t.Fatalf("registered keys = %v, want [argocd certmanager cnpg dns events kubeadjust kubecron netinspect pending redis runtime security]", got)
	}
	// The labels are what the menu draws, so they are asserted as the operator
	// reads them, in menu order.
	for i, want := range []string{"Cert Manager", "Delivery Center", "Event Intelligence", "DNS", "Runtime Inspector", "KubeAdjust", "KubeCron", "Network Inspector", "Pod Debugging", "Postgres (CNPG)", "Redis viewer", "Security Center"} {
		if got := app.Extensions()[i].Label(); got != want {
			t.Fatalf("label %d = %q, want %q", i, got, want)
		}
	}
}

// The binary starts, and the console it starts carries the extra behind its
// own gate:
//
//  1. off, which is every profile's default: a cluster nobody chose anything
//     on has no extra.
//     The route answers the console's refusal (404, plain text): its own
//     handler never runs, and the menu offers no Delivery Center.
//  2. turned on through the console's API: the menu carries Extra, Delivery
//     Center, and the route answers from the extra's handler, in JSON with the
//     extra's own problem code.
//  3. turned off again, and the console's refusal answers again.
//
// The cluster is a kubeconfig pointing at a port nothing listens on, so the
// handler's first question to the cluster fails. That is the point: the extra's
// `kind_unresolved` problem, in JSON, can only come from the extra's own code
// reached through the console's mount, and it needs no cluster to exist. The
// catalog is read lazily, so the failure surfaces at the lookup rather than
// at `Host.Definitions`. Against a real cluster the same route lists
// Applications.
func TestTheBinaryMountsTheExtraBehindTheGate(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the console as a child process")
	}
	base, client := startConsole(t)
	const route = "/x/nowhere/argocd/"

	// The extra's own handler, reached through the console's mount.
	answersFromTheExtra := func(when string) {
		t.Helper()
		status, contentType, body := get(t, client, base+route)
		var problem struct {
			Code string `json:"code"`
		}
		if !strings.HasPrefix(contentType, "application/json") || json.Unmarshal([]byte(body), &problem) != nil {
			t.Fatalf("%s: %s = %d %s %q, want JSON from the extra", when, route, status, contentType, body)
		}
		if status != http.StatusBadGateway || problem.Code != argocd.CodeKindUnresolved {
			t.Fatalf("%s: %s = %d code %q, want 502 %s from a cluster that is not there (body %q)",
				when, route, status, problem.Code, argocd.CodeKindUnresolved, body)
		}
		if !offersDeliveryCenter(t, client, base) {
			t.Fatalf("%s: the menu carries no Extra group with Delivery Center", when)
		}
	}
	turn := func(on bool) {
		t.Helper()
		put, err := http.NewRequest(http.MethodPut, base+"/api/c/nowhere/extensions/argocd",
			strings.NewReader(fmt.Sprintf(`{"enabled":%t}`, on)))
		if err != nil {
			t.Fatal(err)
		}
		put.Header.Set("Content-Type", "application/json")
		put.Header.Set("X-KubeTower-Request", "1")
		resp, err := client.Do(put)
		if err != nil {
			t.Fatalf("turn the extra %v: %v", on, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("turn the extra %v: %d, want 204", on, resp.StatusCode)
		}
	}

	refusedByTheConsole := func(when string) {
		t.Helper()
		status, contentType, body := get(t, client, base+route)
		if status != http.StatusNotFound || !strings.HasPrefix(contentType, "text/plain") {
			t.Fatalf("%s: %s = %d %s %q, want the console's 404 in plain text", when, route, status, contentType, body)
		}
		if offersDeliveryCenter(t, client, base) {
			t.Fatalf("%s: the menu offers Delivery Center", when)
		}
	}

	// 1. Off, with nothing chosen.
	refusedByTheConsole("by default")

	// 2. On.
	turn(true)
	answersFromTheExtra("turned on")

	// 3. Off again.
	turn(false)
	refusedByTheConsole("turned off again")
}

// startConsole starts this test binary as the console, on a free port, with a
// scratch state directory and a kubeconfig naming one cluster, `nowhere`, whose
// server is a port nothing listens on. It returns the base URL and a client
// signed in.
//
// The password is the test's own, handed over as KT_PASSWORD_HASH. Without one
// a loopback desktop start asks for none, and the console would then be the launcher's rather than a session's; a
// configured hash is what the smoke suite and the development loop use for the
// same reason.
func startConsole(t *testing.T) (string, *http.Client) {
	t.Helper()
	state := t.TempDir()
	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeFile(t, kubeconfig, `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: nowhere
  context:
    cluster: nowhere
    user: nowhere
current-context: nowhere
users:
- name: nowhere
  user:
    token: not-a-token
`)
	addr := freeAddress(t)
	base := "http://" + addr
	password := rand.Text()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "--no-window", "--unattended")
	cmd.Env = append(withoutConsoleSettings(os.Environ()),
		runAsConsole+"=1",
		"KUBECONFIG="+kubeconfig,
		"KT_STATE_DIR="+state,
		"KT_LISTEN="+addr,
		"KT_PASSWORD_HASH="+string(hash),
	)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the console: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("console output:\n%s", output.String())
		}
	})
	return base, signedIn(t, base, password)
}

// The console this binary serves carries the extras' pages: the bundle
// embedded in it holds the Delivery Center's chunk, KubeCron's, KubeAdjust's,
// the Redis viewer's and the JVM Inspector's, and the console serves each from `/assets/`.
// `web/app/main.tsx` hands every extension to `createApp`, and a bundle without them would be a
// menu entry that lands on the console's "no extension answers here" page.
//
// It needs the bundle built (`task build:web`, or `pnpm -C web build`).
// Without it the test skips and says so: `web/dist` is committed holding only
// its placeholder (docs/DEFINITION-OF-DONE.md §14), so a backend contributor
// can still run `go test ./...` with no Node. **KT_REQUIRE_BUNDLE=1 turns that
// skip into a failure**, and smoke.yml sets it right after it builds the
// bundle, so in CI this test runs rather than skipping where it can.
func TestTheBundleCarriesTheExtrasPages(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the console as a child process")
	}
	ui, err := fs.Sub(web.Assets, "dist/ui")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		if os.Getenv("KT_REQUIRE_BUNDLE") == "1" {
			t.Fatalf("no bundle in web/dist/ui (%v), and KT_REQUIRE_BUNDLE=1", err)
		}
		t.Skipf("no bundle in web/dist/ui (%v): run `task build:web` first", err)
	}
	var chunks []string
	for _, page := range []string{"DeliveryCenter", "KubeCron", "KubeAdjust", "RedisViewer", "JVMInspector", "PendingPage"} {
		found, err := fs.Glob(ui, "assets/"+page+"-*.js")
		if err != nil || len(found) != 1 {
			t.Fatalf("the bundle holds %d %s chunks (%v), want exactly 1", len(found), page, err)
		}
		chunks = append(chunks, found[0])
	}

	base, client := startConsole(t)
	for _, chunk := range chunks {
		status, contentType, _ := get(t, client, base+"/"+chunk)
		if status != http.StatusOK || !strings.Contains(contentType, "javascript") {
			t.Fatalf("GET /%s = %d %s, want the extra's chunk", chunk, status, contentType)
		}
	}
}

// signedIn waits for the console to answer, then signs in with password.
func signedIn(t *testing.T, base, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := client.Get(base + "/api/capabilities")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the console did not answer within a minute: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	body, _ := json.Marshal(map[string]string{"user": "admin", "password": password})
	req, err := http.NewRequest(http.MethodPost, base+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KubeTower-Request", "1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sign in: %d, want 204", resp.StatusCode)
	}
	return client
}

// offersDeliveryCenter reports whether the menu of `nowhere` carries Delivery
// Center in its Extra group. Another extra keeps the group there when this one
// is off, so the question is the entry, not the group.
func offersDeliveryCenter(t *testing.T, client *http.Client, base string) bool {
	t.Helper()
	status, _, body := get(t, client, base+"/api/c/nowhere/nav")
	if status != http.StatusOK {
		t.Fatalf("nav: %d %q", status, body)
	}
	var nav struct {
		Data struct {
			Groups []struct {
				Key   string `json:"key"`
				Items []struct {
					Text string `json:"text"`
				} `json:"items"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &nav); err != nil {
		t.Fatalf("nav is not the documented envelope: %v (%q)", err, body)
	}
	for _, g := range nav.Data.Groups {
		if g.Key != "extra" {
			continue
		}
		for _, item := range g.Items {
			if item.Text == "Delivery Center" {
				return true
			}
		}
	}
	return false
}

func get(t *testing.T, client *http.Client, url string) (int, string, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

// freeAddress asks the kernel for a port and gives it back. The console binds
// it a moment later, so another process could take it in between. On a test
// machine that race is theoretical, and losing it fails loudly.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// withoutConsoleSettings drops every KT_* variable and every kubeconfig
// override from the inherited environment. An operator's own KT_EXTRAS or
// KT_PASSWORD_HASH would otherwise decide what this test observes.
func withoutConsoleSettings(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(name), "KT_") || strings.EqualFold(name, "KUBECONFIG") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The three renamed extras keep the keys their URLs, their stored switches and
// KT_EXTRAS use; only the labels changed.
func TestTheRenamedExtrasKeepTheirKeys(t *testing.T) {
	want := map[string]string{"KubeCron": "kubecron", "KubeAdjust": "kubeadjust", "Pod Debugging": "pending"}
	for _, e := range extras() {
		if key, ok := want[e.Label()]; ok {
			if e.Key() != key {
				t.Errorf("%s has key %q, want %q", e.Label(), e.Key(), key)
			}
			delete(want, e.Label())
		}
	}
	if len(want) != 0 {
		t.Errorf("not registered under their new label: %v", want)
	}
}
