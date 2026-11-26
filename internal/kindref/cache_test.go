package kindref

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// justPods is the mapper a cluster had before somebody installed an operator.
func justPods() meta.RESTMapper {
	core := schema.GroupVersion{Version: "v1"}
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{core})
	m.Add(core.WithKind("Pod"), meta.RESTScopeNamespace)
	return m
}

// The reason this cache is one cache: a caller that does not rebuild the mapper
// when a kind fails to resolve refuses a CRD installed in the last ten minutes,
// while the drawer beside it, which does rebuild, opens it.
func TestAKindThatDoesNotResolveCostsOneRebuildAndThenResolves(t *testing.T) {
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		if builds == 1 {
			// The mapper held from before the CRD was installed.
			return justPods(), nil
		}
		return mapperWith(t), nil
	}

	c := NewCache()
	if _, _, err := c.Mapping(t.Context(), "prod", "pods", build); err != nil {
		t.Fatalf("pods did not resolve: %v", err)
	}
	if builds != 1 {
		t.Fatalf("built the mapper %d times for the first call, want 1", builds)
	}

	mapping, err := func() (*meta.RESTMapping, error) {
		_, mapping, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build)
		return mapping, err
	}()
	if err != nil {
		t.Fatalf("a CRD installed since the mapper was built was refused: %v\n"+
			"Without the rebuild the Changes panel says "+
			"\"is not a kind this cluster serves\" about an object the drawer beside it "+
			"has just rendered, for as long as the TTL", err)
	}
	if mapping.Resource.Resource != "applicationsets" {
		t.Errorf("resolved to %q, want applicationsets", mapping.Resource.Resource)
	}
	if builds != 2 {
		t.Errorf("built the mapper %d times, want exactly 2 — one rebuild, not one per call", builds)
	}

	// And the rebuilt mapper is the one now held, so the next caller pays
	// nothing.
	if _, _, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build); err != nil {
		t.Fatalf("the second ask failed: %v", err)
	}
	if builds != 2 {
		t.Errorf("built the mapper %d times, want 2 — the rebuild was not kept", builds)
	}
}

// A kind that genuinely does not exist costs exactly one rebuild and then says
// so, rather than rebuilding on every call for a typo.
func TestAKindThatDoesNotExistIsRefusedAfterOneRebuild(t *testing.T) {
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		return justPods(), nil
	}

	c := NewCache()
	_, _, err := c.Mapping(t.Context(), "prod", "wibbles.example.com", build)
	if err == nil {
		t.Fatal("a kind nothing serves resolved")
	}
	if !strings.Contains(err.Error(), "not a kind this cluster serves") {
		t.Errorf("error = %q, want the caller's own sentence", err)
	}
	if builds != 2 {
		t.Errorf("built the mapper %d times, want 2", builds)
	}
}

// The cache is a cache: one build per cluster inside the TTL, however many
// panels ask.
func TestTheMapperIsBuiltOncePerCluster(t *testing.T) {
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		return mapperWith(t), nil
	}

	c := NewCache()
	for range 5 {
		if _, err := c.Mapper(t.Context(), "prod", false, build); err != nil {
			t.Fatalf("Mapper: %v", err)
		}
	}
	if builds != 1 {
		t.Errorf("built %d times for one cluster, want 1 — this is dozens of round trips each", builds)
	}

	// A second cluster is a second mapper. Sharing one would answer a kind
	// question about the wrong cluster, which is the worst possible failure here.
	if _, err := c.Mapper(t.Context(), "staging", false, build); err != nil {
		t.Fatalf("Mapper: %v", err)
	}
	if builds != 2 {
		t.Errorf("built %d times for two clusters, want 2", builds)
	}
}

// The TTL a caller actually gets, as opposed to the one a test sets on itself.
//
// `mapperTTL = 1 << 62` — an effectively infinite TTL, which is the tempting
// simplification since a kind the mapper cannot resolve forces a rebuild
// anyway — would pass every other test here, because TestAnAgedMapperIsRebuilt
// below assigns `c.ttl` directly and so never reads the constant. The retry
// only recovers kinds that were *added*; a mapper held
// forever still answers with a version the cluster stopped serving after a chart
// upgrade, and nothing would ever ask again.
func TestTheDefaultTTLIsFiniteAndShortEnoughToMatter(t *testing.T) {
	c := NewCache()
	if c.ttl <= 0 {
		t.Fatalf("the default TTL is %v, so nothing is cached at all — this is dozens of "+
			"round trips per drawer open", c.ttl)
	}
	if c.ttl > time.Hour {
		t.Errorf("the default TTL is %v: a mapper held that long survives an operator "+
			"upgrade that moves a CRD to a version this one does not know, and the retry "+
			"cannot recover a kind that resolves to the wrong version", c.ttl)
	}
}

func TestAnAgedMapperIsRebuilt(t *testing.T) {
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		return mapperWith(t), nil
	}

	c := NewCache()
	if _, err := c.Mapper(t.Context(), "prod", false, build); err != nil {
		t.Fatalf("Mapper: %v", err)
	}
	c.ttl = -time.Second // everything held is now older than the TTL
	if _, err := c.Mapper(t.Context(), "prod", false, build); err != nil {
		t.Fatalf("Mapper: %v", err)
	}
	if builds != 2 {
		t.Errorf("built %d times, want 2 — the TTL did not expire anything", builds)
	}
}

// A cluster that could not be asked is not a cluster that answered "no such
// kind". The callers phrase the two differently, so the difference has to survive
// the trip through here.
func TestDiscoveryFailureIsDistinguishableFromAnUnknownKind(t *testing.T) {
	refused := errors.New("the server has asked for the client to provide credentials")
	c := NewCache()

	_, _, err := c.Mapping(t.Context(), "prod", "pods", func(context.Context) (meta.RESTMapper, error) { return nil, refused })
	if err == nil {
		t.Fatal("a cluster that could not be asked answered")
	}
	var discovery *DiscoveryError
	if !errors.As(err, &discovery) {
		t.Fatalf("error is %T, want a *DiscoveryError so the caller can say the cluster could "+
			"not be asked rather than that it does not serve the kind", err)
	}
	if !errors.Is(err, refused) {
		t.Error("the underlying reason was swallowed")
	}

	// And a failed build is not cached: the next call tries again rather than
	// holding a nil mapper for ten minutes.
	if _, err := c.Mapper(t.Context(), "prod", false, func(context.Context) (meta.RESTMapper, error) { return mapperWith(t), nil }); err != nil {
		t.Fatalf("the retry after a failed build failed too: %v", err)
	}
}

// --- the negative cache, and the context ---------------------------------------

// `kind` comes off a URL path, so a kind the cluster does not serve is a
// request an operator — or a crawler, or a typo in a loop — can make as often as
// it likes. Without a negative cache each one is a full `GetAPIGroupResources`
// walk, dozens of round trips, *and* it replaces the mapper every other panel in
// the console is using.
//
// Counted in builds rather than timed: a duration in a test is noise, and the
// walk is what costs.
func TestAKindTheClusterDoesNotServeIsNotRewalkedOnEveryRequest(t *testing.T) {
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		return justPods(), nil
	}

	c := NewCache()
	for range 20 {
		if _, _, err := c.Mapping(t.Context(), "prod", "wibbles.example.com", build); err == nil {
			t.Fatal("a kind nothing serves resolved")
		}
	}
	if builds != 2 {
		t.Errorf("twenty requests for a kind this cluster does not serve built the mapper %d times, "+
			"want 2 — one held mapper and one rebuild to be sure. Every extra one is a full "+
			"discovery walk, and it also throws away the mapper every other panel is using", builds)
	}
}

// The trap in a negative cache, and the reason this one expires: it is a cache
// of an **absence**, and an absence ends the moment somebody applies a CRD.
// Nothing tells this package that happened, so the lifetime is the only
// invalidation there is.
func TestANegativeAnswerExpiresSoANewlyInstalledCRDBecomesVisible(t *testing.T) {
	knows := false
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		if knows {
			return mapperWith(t), nil
		}
		return justPods(), nil
	}

	c := NewCache()
	if _, _, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build); err == nil {
		t.Fatal("a kind nothing served resolved")
	}
	if builds != 2 {
		t.Fatalf("built %d times, want 2", builds)
	}

	// Somebody installs Argo CD, and the negative entry ages out. Backdated
	// rather than slept: the property is that it expires, not how fast a machine
	// is.
	knows = true
	c.mu.Lock()
	for k := range c.unknown {
		c.unknown[k] = time.Now().Add(-2 * c.unknownTTL)
	}
	c.mu.Unlock()

	_, mapping, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build)
	if err != nil {
		t.Fatalf("a CRD installed after the negative entry was written is still invisible: %v\n"+
			"A negative cache that does not expire turns a working cluster into one where a newly "+
			"installed operator's kind cannot be opened until the process restarts", err)
	}
	if mapping.Resource.Resource != "applicationsets" {
		t.Errorf("resolved to %q, want applicationsets", mapping.Resource.Resource)
	}
}

// The lifetime a caller actually gets, as opposed to the one a test sets on
// itself — the same hole `TestTheDefaultTTLIsFiniteAndShortEnoughToMatter`
// covers. `unknownTTL = 1 << 62` is the tempting simplification, and every
// other test here either sets the field or backdates the map, so nothing else
// reads the constant.
func TestTheNegativeTTLIsFiniteAndShort(t *testing.T) {
	c := NewCache()
	if c.unknownTTL <= 0 {
		t.Fatalf("the negative TTL is %v, so nothing is remembered: a kind off a URL costs a full "+
			"discovery walk per request again", c.unknownTTL)
	}
	if c.unknownTTL > 2*time.Minute {
		t.Errorf("the negative TTL is %v. That is how long a kind installed a moment ago stays "+
			"invisible with no way for an operator to clear it, and nothing in this package is told "+
			"when a CRD is applied — the lifetime is the whole of the invalidation", c.unknownTTL)
	}
	if c.unknownTTL >= c.ttl {
		t.Errorf("the negative TTL (%v) is not shorter than the mapper TTL (%v): the negative entry "+
			"would outlive the mapper it was recorded against, which is the one thing that frees a "+
			"kind without waiting", c.unknownTTL, c.ttl)
	}
}

// What the negative entry gates is the **rebuild**, never the answer. A mapper
// that has aged out and been rebuilt resolves the kind straight away, even while
// the negative entry is still young — so no entry here can hide a kind the
// cluster serves.
func TestANegativeEntryNeverHidesAKindTheHeldMapperKnows(t *testing.T) {
	knows := false
	build := func(context.Context) (meta.RESTMapper, error) {
		if knows {
			return mapperWith(t), nil
		}
		return justPods(), nil
	}

	c := NewCache()
	if _, _, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build); err == nil {
		t.Fatal("a kind nothing served resolved")
	}

	// The mapper's own TTL rolls over — ten minutes in production — and the
	// cluster has learned the kind meanwhile. The negative entry is untouched and
	// still well inside its window.
	knows = true
	c.ttl = -time.Second
	_, mapping, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build)
	if err != nil {
		t.Fatalf("a kind the rebuilt mapper knows was refused from the negative cache: %v", err)
	}
	if mapping.Resource.Resource != "applicationsets" {
		t.Errorf("resolved to %q, want applicationsets", mapping.Resource.Resource)
	}
}

// A cluster that could not be asked is not a cluster that answered "no such
// kind". Remembering a failed rebuild would give that confusion a lifetime.
func TestADiscoveryFailureIsNotRememberedAsAnUnknownKind(t *testing.T) {
	refused := errors.New("the server has asked for the client to provide credentials")
	var builds int
	build := func(context.Context) (meta.RESTMapper, error) {
		builds++
		if builds == 1 {
			return justPods(), nil
		}
		return nil, refused
	}

	c := NewCache()
	for range 2 {
		if _, _, err := c.Mapping(t.Context(), "prod", "applicationsets.argoproj.io", build); err == nil {
			t.Fatal("a kind nothing served resolved")
		}
	}
	if builds != 3 {
		t.Errorf("built %d times, want 3 — a rebuild that never reached the cluster was remembered "+
			"as the cluster saying no, so the second request did not try again", builds)
	}
}

// The negative cache is keyed on something that comes off a URL, so it is
// attacker-shaped: it must not grow without bound.
func TestTheNegativeCacheIsBounded(t *testing.T) {
	build := func(context.Context) (meta.RESTMapper, error) { return justPods(), nil }

	c := NewCache()
	for i := range maxUnknown + 50 {
		kind := "wibble" + strconv.Itoa(i) + ".example.com"
		if _, _, err := c.Mapping(t.Context(), "prod", kind, build); err == nil {
			t.Fatalf("%s resolved", kind)
		}
	}
	c.mu.Lock()
	held := len(c.unknown)
	c.mu.Unlock()
	if held > maxUnknown {
		t.Errorf("the negative cache holds %d entries with a cap of %d: a loop with a typo in it is "+
			"a memory leak", held, maxUnknown)
	}
}

type buildContextKey struct{}

// The negative cache's other half: the walk is bounded by the caller's context,
// so a page load htmx has already abandoned does not keep asking the cluster
// for every API group it serves, to the end.
func TestTheBuildIsGivenTheCallersContext(t *testing.T) {
	ctx := context.WithValue(t.Context(), buildContextKey{}, "the caller's")

	var seen any
	c := NewCache()
	if _, err := c.Mapper(ctx, "prod", false, func(ctx context.Context) (meta.RESTMapper, error) {
		seen = ctx.Value(buildContextKey{})
		return mapperWith(t), nil
	}); err != nil {
		t.Fatalf("Mapper: %v", err)
	}
	if seen != "the caller's" {
		t.Errorf("the build was given a context carrying %v, want the caller's own — a build that "+
			"cannot see the request cannot stop when the request goes away", seen)
	}
}

// And the context is usable, not decorative: a build that watches it returns
// when the request is canceled, rather than walking every API group for nobody.
func TestACancelledRequestStopsTheDiscoveryWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	started := make(chan struct{})
	c := NewCache()
	done := make(chan error, 1)
	go func() {
		_, err := c.Mapper(ctx, "prod", false, func(ctx context.Context) (meta.RESTMapper, error) {
			close(started)
			<-ctx.Done() // stands in for the walk's next round trip
			return nil, ctx.Err()
		})
		done <- err
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the build ended with %v, want something wrapping context.Canceled", err)
		}
		var discovery *DiscoveryError
		if !errors.As(err, &discovery) {
			t.Errorf("error is %T, want a *DiscoveryError: a walk that was abandoned is a cluster "+
				"that could not be asked, not a kind it does not serve", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the build never saw the cancellation — it has no context to see it with")
	}
}

// A key is a client's, and a re-pointed context's old key is never asked
// again: it goes once expired, when the next mapper is built (#153).
func TestAnExpiredMapperGoesWhenTheNextIsBuilt(t *testing.T) {
	c := NewCache()
	c.ttl = time.Hour
	build := func(context.Context) (meta.RESTMapper, error) { return meta.NewDefaultRESTMapper(nil), nil }
	if _, err := c.Mapper(context.Background(), "prod\x001", false, build); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	old := c.entries["prod\x001"]
	old.built = time.Now().Add(-2 * time.Hour)
	c.entries["prod\x001"] = old
	c.mu.Unlock()

	if _, err := c.Mapper(context.Background(), "prod\x002", false, build); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, kept := c.entries["prod\x001"]; kept {
		t.Error("the expired mapper of a client no longer in use is still held")
	}
	if _, ok := c.entries["prod\x002"]; !ok {
		t.Error("the new mapper was not kept")
	}
}
