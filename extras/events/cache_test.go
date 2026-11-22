package events

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestTheCacheIsKeyedByPerson(t *testing.T) {
	h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
	e := newExt()

	for i := 0; i < 3; i++ {
		if rec := serve(t, e, h, "/?namespace=shop", "alice"); rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	if n := h.listsOf("events"); n != 2 { // the namespace's Events and the nodes' NodeNotReady, once
		t.Errorf("alice asked three times: %d event reads, want one build (2 reads)", n)
	}
	// Another person is never served what was read for alice.
	serve(t, e, h, "/?namespace=shop", "bob")
	if n := h.listsOf("events"); n != 4 {
		t.Errorf("bob was served alice's aggregate: %d event reads", n)
	}
	// Another namespace or window is another aggregate.
	serve(t, e, h, "/?namespace=shop&window=3h", "alice")
	serve(t, e, h, "/", "alice")
	if n := e.cache.size(); n != 4 {
		t.Errorf("cache holds %d, want 4 (alice x3, bob)", n)
	}
	// The page, the object and the line share one aggregate.
	before := h.listsOf("events")
	serve(t, e, h, "/object/deployments/shop/web", "alice")
	serve(t, e, h, "/line/deployments/shop/web", "alice")
	if h.listsOf("events") != before {
		t.Errorf("object and line did not reuse the aggregate: %d -> %d", before, h.listsOf("events"))
	}
}

func TestARequestWithNoSessionIsNeverCached(t *testing.T) {
	h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
	e := newExt()
	serve(t, e, h, "/", "")
	serve(t, e, h, "/", "")
	if e.cache.size() != 0 || h.listsOf("events") != 2 {
		t.Errorf("cache %d, event reads %d: with nobody to key on, each answer is read again", e.cache.size(), h.listsOf("events"))
	}
}

func TestTheCacheExpiresAfterFifteenSeconds(t *testing.T) {
	h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
	now := testNow
	e := newExt()
	e.now = func() time.Time { return now }
	serve(t, e, h, "/", "alice")
	now = now.Add(14 * time.Second)
	serve(t, e, h, "/", "alice")
	if n := h.listsOf("events"); n != 1 {
		t.Errorf("at 14 s: %d reads", n)
	}
	now = now.Add(2 * time.Second)
	serve(t, e, h, "/", "alice")
	if n := h.listsOf("events"); n != 2 {
		t.Errorf("at 16 s: %d reads, want a fresh one", n)
	}
}

func TestTheCacheHoldsThirtyTwoLeastRecentlyUsedOut(t *testing.T) {
	h := newHost(t)
	e := newExt()
	for i := 0; i < CacheEntries+10; i++ {
		serve(t, e, h, "/", fmt.Sprintf("person-%d", i))
	}
	if n := e.cache.size(); n != CacheEntries {
		t.Fatalf("cache holds %d, want %d", n, CacheEntries)
	}
	reads := h.listsOf("events")
	serve(t, e, h, "/", "person-0") // evicted: read again
	if h.listsOf("events") != reads+1 {
		t.Error("the oldest entry was not evicted")
	}
	reads = h.listsOf("events")
	serve(t, e, h, "/", "person-41") // the newest: still held
	if h.listsOf("events") != reads {
		t.Error("the newest entry was evicted")
	}
}

func TestAFailedBuildIsNotHeld(t *testing.T) {
	h := newHost(t)
	h.denied["events"] = true
	e := newExt()
	if rec := serve(t, e, h, "/", "alice"); rec.Code != http.StatusForbidden {
		t.Fatalf("%d", rec.Code)
	}
	if e.cache.size() != 0 {
		t.Error("a refusal was cached")
	}
}

func TestThePersonKeyIsAHashNeverTheCookie(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "s3cret-session"})
	k, ok := personKey(r)
	if !ok || k == "" || k == "s3cret-session" || len(k) != 32 {
		t.Errorf("key %q ok %v", k, ok)
	}
	r2, _ := http.NewRequest(http.MethodGet, "/", nil)
	if _, ok := personKey(r2); ok {
		t.Error("a request with no session got a key")
	}
}
