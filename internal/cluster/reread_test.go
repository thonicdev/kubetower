package cluster

import (
	gocontext "context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/thonicdev/kubetower/internal/kindref"
)

// oneContext writes a kubeconfig holding one context, name, on server.
func oneContext(t *testing.T, path, name, server string) {
	t.Helper()
	writeKubeconfigAt(t, path, map[string]string{name: server})
}

// serverOf is the server a context's entry names in the kubeconfig in use.
func serverOf(m *Manager, name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx := m.raw.Contexts[name]
	if ctx == nil {
		return ""
	}
	if c := m.raw.Clusters[ctx.Cluster]; c != nil {
		return c.Server
	}
	return ""
}

// #153 item 1: with KUBECONFIG=a:b, a file truncated between the read of the
// files and their merge must not apply a merge without it, recorded under
// bytes that still held it: the next tick would see those bytes unchanged and
// never put its contexts back.
func TestAFileTruncatedBetweenTheReadAndTheMergeIsNotApplied(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	oneContext(t, a, "alpha", "https://alpha.invalid")
	oneContext(t, b, "beta", "https://beta.invalid")
	m, err := NewManager(a, b)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	aBytes, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}

	// b changes, and a is truncated while that change is being read.
	oneContext(t, b, "beta", "https://beta-2.invalid")
	once := sync.Once{}
	m.betweenReads = func() {
		once.Do(func() {
			if err := os.WriteFile(a, nil, 0o600); err != nil {
				t.Error(err)
			}
		})
	}
	if _, err := m.ReloadIfChanged(); err != nil {
		t.Fatal(err)
	}
	if got := m.Contexts(); !slices.Contains(got, "alpha") {
		t.Fatalf("a merge without a was applied: %v", got)
	}

	// a written back: the next tick reads both whole.
	m.betweenReads = nil
	if err := os.WriteFile(a, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := m.ReloadIfChanged(); err != nil {
		t.Fatal(err)
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("contexts %v once a is back", got)
	}
	if got := serverOf(m, "beta"); got != "https://beta-2.invalid" {
		t.Errorf("beta's server %q, want b's edit", got)
	}
}

// #153 item 2: an editor that renames the original away before writing the
// new one leaves the file absent for a moment. Its contexts, and their
// clients, stay until it has lasted settleFor.
func TestAFileGoneForAMomentKeepsItsClients(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	oneContext(t, a, "alpha", "https://alpha.invalid")
	oneContext(t, b, "beta", "https://beta.invalid")
	m, err := NewManager(a, b)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	before, err := m.Client("alpha")
	if err != nil {
		t.Fatal(err)
	}

	moved := a + ".bak"
	if err := os.Rename(a, moved); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("an absent file was applied at once: changed=%v err=%v", changed, err)
	}
	now = now.Add(time.Second)
	if err := os.Rename(moved, a); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("the same file back: changed=%v err=%v", changed, err)
	}
	after, err := m.Client("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("alpha's client was rebuilt across a file gone for a second")
	}

	// Gone for good: applied once it has lasted.
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	for _, step := range []time.Duration{0, 2 * time.Second, 2 * time.Second} {
		now = now.Add(step)
		_, _ = m.ReloadIfChanged()
	}
	if got := m.Contexts(); !slices.Equal(got, []string{"beta"}) {
		t.Errorf("contexts %v after a stayed gone past settleFor", got)
	}
}

// #153 item 3: the watcher's tick and a cluster's Retry re-read at once. The
// older read is never applied after the newer one.
func TestConcurrentReReadsEndOnTheNewerKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	oneContext(t, path, "alpha", "https://v0.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}

	// The first call reads v1 and is held just before applying it.
	oneContext(t, path, "alpha", "https://v1.invalid")
	held, release := make(chan struct{}), make(chan struct{})
	// Not a sync.Once: its Do makes a second caller wait for the first, which
	// would serialise the two calls here whatever ReloadIfChanged does.
	var first atomic.Bool
	m.beforeApply = func() {
		if first.CompareAndSwap(false, true) {
			close(held)
			<-release
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = m.ReloadIfChanged()
	}()
	<-held

	// v2 is saved, and a second call reads it while the first is held.
	oneContext(t, path, "alpha", "https://v2.invalid")
	second := make(chan struct{})
	go func() {
		defer close(second)
		_, _ = m.ReloadIfChanged()
	}()
	select {
	case <-second:
		// Not serialised: it applied v2 already, and the held v1 would follow.
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	<-second

	if got := serverOf(m, "alpha"); got != "https://v2.invalid" {
		t.Errorf("alpha on %q after both re-reads, want the newer v2", got)
	}
}

// #153 item 4: a context changed while its client is being built. That
// request keeps the client it built; the next one gets a client built from
// the new entry, not the stale one cached.
func TestAClientBuiltAcrossAnEditIsNotCached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	oneContext(t, path, "alpha", "https://old.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	once := sync.Once{}
	m.building = func() {
		once.Do(func() {
			oneContext(t, path, "alpha", "https://new.invalid")
			if err := m.Reload(); err != nil {
				t.Error(err)
			}
		})
	}
	during, err := m.Client("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if during.Config.Host != "https://old.invalid" {
		t.Fatalf("the request in flight got %q", during.Config.Host)
	}
	next, err := m.Client("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if next.Config.Host != "https://new.invalid" {
		t.Errorf("the next request got %q, the entry from before the edit", next.Config.Host)
	}
}

// #153 item 5: a context re-pointed at another server has its kinds
// discovered again: the mapper cache is keyed by the client, which a
// re-point rebuilds, and an edit elsewhere keeps.
func TestARePointedContextDiscoversItsKindsAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	twoContexts(t, path, "alpha", "https://beta.invalid")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	cache := kindref.NewCache()
	var built []string
	mapperOf := func(name string) {
		t.Helper()
		client, err := m.Client(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cache.Mapper(gocontext.Background(), client.MapperKey(), false, func(gocontext.Context) (meta.RESTMapper, error) {
			built = append(built, client.Config.Host)
			return meta.NewDefaultRESTMapper(nil), nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	mapperOf("beta")
	mapperOf("alpha")
	mapperOf("beta")
	if !slices.Equal(built, []string{"https://beta.invalid", "https://alpha.invalid"}) {
		t.Fatalf("discovered %v, want once per context", built)
	}

	// beta re-pointed; alpha untouched.
	twoContexts(t, path, "alpha", "https://beta-2.invalid")
	if changed, err := m.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	mapperOf("beta")
	mapperOf("alpha")
	want := []string{"https://beta.invalid", "https://alpha.invalid", "https://beta-2.invalid"}
	if !slices.Equal(built, want) {
		t.Errorf("discovered %v, want %v: beta from its new server, alpha from the cache", built, want)
	}
}
