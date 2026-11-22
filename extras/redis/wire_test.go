package redis

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The wire tests: what reaches a server's socket, recorded by the server.
//
// The prototype proved its read-only claim by grepping its own source for any
// send other than the one door. That stays (readonly_test.go), and this is the
// net the prototype did not have: a fake RESP server records every command it
// receives while every route runs, and the set must be inside the allowlist
// plus the client's own handshake - named here, each with why it is not a
// command this package sends.
var handshake = map[string]string{
	// valkey-go opens every connection with `HELLO 3` to agree RESP3, and puts
	// `AUTH default <password>` inside it when there is one. A Redis too old
	// for HELLO is then sent `HELLO 2`, which it also refuses.
	"HELLO": "the client's protocol negotiation, carrying the password when there is one",
	// The RESP2 handshake, on a server too old for HELLO: `AUTH <password>`.
	"AUTH": "the password, on a server too old to take it inside HELLO",
	// The database is chosen at dial time (options.database) and never
	// changed afterwards, so a SELECT on a shared connection cannot happen.
	"SELECT": "the database chosen at dial, when it is not 0",
}

type instanceBody struct {
	Instance struct {
		Why string `json:"why"`
	} `json:"instance"`
	Pod    string `json:"pod"`
	DB     int    `json:"db"`
	Server struct {
		Engine    string `json:"engine"`
		Version   string `json:"version"`
		Mode      string `json:"mode"`
		Databases int    `json:"databases"`
		Note      string `json:"note"`
		Role      string `json:"role"`
		Replicas  *int   `json:"replicas"`
	} `json:"server"`
	Credential *struct {
		Secret string `json:"secret"`
		Key    string `json:"key"`
		Source string `json:"source"`
		Pod    string `json:"pod"`
	} `json:"credential"`
	Overview *struct {
		Keys       int64  `json:"keys"`
		UsedMemory string `json:"usedMemory"`
		Keyspace   []struct {
			Index int   `json:"index"`
			Keys  int64 `json:"keys"`
		} `json:"keyspace"`
	} `json:"overview"`
	Scan *struct {
		Match string `json:"match"`
		Next  string `json:"next"`
		Done  bool   `json:"done"`
		Keys  []struct {
			Name   string `json:"name"`
			Raw    string `json:"raw"`
			Type   string `json:"type"`
			TTL    string `json:"ttl"`
			Memory *int64 `json:"memory"`
		} `json:"keys"`
	} `json:"scan"`
	Key *struct {
		Name     string  `json:"name"`
		Raw      string  `json:"raw"`
		Type     string  `json:"type"`
		Length   *int64  `json:"length"`
		Entries  []Entry `json:"entries"`
		Next     string  `json:"next"`
		PageSize int     `json:"pageSize"`
		Note     string  `json:"note"`
	} `json:"key"`
}

// stocked is a server holding a key of every type the viewer draws, and one
// it does not.
func stocked(t *testing.T, engine string) *fakeRedis {
	t.Helper()
	server := newFakeRedis(t, engine)
	server.set(0, "greeting", value{kind: "string", s: "hello", ttl: 90})
	server.set(0, "queue", value{kind: "list", items: []string{"a", "b"}})
	server.set(0, "user:1", value{kind: "hash", hash: map[string]string{"name": "ada", "lang": "en"}})
	server.set(0, "tags", value{kind: "set", items: []string{"x", "y"}})
	server.set(0, "board", value{kind: "zset", items: []string{"p1", "p2"}})
	server.set(0, "events", value{kind: "stream", stream: []streamItem{
		{id: "1-0", fields: []string{"user", "42", "action", "login"}},
		{id: "2-0", fields: []string{"user", "7"}},
		{id: "3-0", fields: []string{"user", "42", "action", "logout"}},
	}})
	server.set(0, "doc", value{kind: "ReJSON-RL"})
	server.set(1, "other-db", value{kind: "string", s: "in db1"})
	return server
}

func hostFor(t *testing.T, server *fakeRedis) *fakeHost {
	t.Helper()
	h := newHost()
	h.forwardTo = server.addr()
	h.redisService(t, "cache", "redis", corev1.ServicePort{Name: "redis", Port: 6379}, "redis-0")
	return h
}

// Every route, once against a server answering as Redis and once as Valkey,
// with and without a password, and against a Redis too old for HELLO: every
// command that reached the socket is a read on the allowlist or the client's
// named handshake. And each run reports the engine and version the server
// gave, never one guessed from a name.
func TestEveryCommandOnTheWireIsAReadOrTheClientsOwnHandshake(t *testing.T) {
	for _, c := range []struct {
		name, engine, version, password string
		noHello                         bool
	}{
		{"redis", "Redis", "7.2.4", "", false},
		{"valkey", "Valkey", "8.0.1", "", false},
		{"redis with a password", "Redis", "7.2.4", "s3cret-redis", false},
		{"valkey with a password", "Valkey", "8.0.1", "s3cret-valkey", false},
		{"redis 5, no HELLO, with a password", "Redis", "7.2.4", "s3cret-old", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := stocked(t, c.engine)
			server.password, server.noHello = c.password, c.noHello
			h := hostFor(t, server)
			if c.password != "" {
				h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis",
					Env: []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "redis-password")}}))
				h.secret("cache", "redis", map[string]string{"redis-password": c.password})
			}

			targets := []string{
				"/x/kind/redis/instances/cache/redis/6379",
				"/x/kind/redis/instances/cache/redis/6379?match=user:*",
				"/x/kind/redis/instances/cache/redis/6379?db=1",
				"/x/kind/redis/instances/cache/redis/6379/probe",
			}
			for _, key := range []string{"greeting", "queue", "user:1", "tags", "board", "events", "doc"} {
				targets = append(targets, "/x/kind/redis/instances/cache/redis/6379/key?key="+url.QueryEscape(key))
			}
			for _, target := range targets {
				w := serve(t, h, target)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: status %d: %s", target, w.Code, w.Body.String())
				}
				if c.password != "" && strings.Contains(w.Body.String(), c.password) {
					t.Fatalf("%s: the password is in the answer: %s", target, w.Body.String())
				}
				body := decode[instanceBody](t, w)
				if body.Server.Engine != c.engine || body.Server.Version != c.version {
					t.Fatalf("%s: engine %q %q, want %q %q, as the server reported itself",
						target, body.Server.Engine, body.Server.Version, c.engine, c.version)
				}
				if c.password != "" && (body.Credential == nil || body.Credential.Secret != "redis" ||
					body.Credential.Pod != "redis-0") {
					t.Fatalf("%s: the provenance is missing or wrong: %+v", target, body.Credential)
				}
			}

			seen := server.seen()
			if len(seen) < len(targets)*3 {
				t.Fatalf("the server recorded %d commands for %d requests; it is not seeing the traffic", len(seen), len(targets))
			}
			names := map[string]bool{}
			for _, cmd := range seen {
				name := strings.ToUpper(cmd[0])
				if handshake[name] != "" {
					names["handshake "+name] = true
					continue
				}
				if err := allowed(cmd); err != nil {
					t.Errorf("%v reached the server, and it is neither a read on the allowlist nor the "+
						"client's named handshake: %v", cmd, err)
				}
				names[name] = true
			}
			t.Logf("%d commands on the wire, of %d kinds: %v", len(seen), len(names), keysOf(names))
			if h.forwards != len(targets) || h.stopped != h.forwards {
				t.Fatalf("%d requests opened %d tunnel(s) and stopped %d: one each, each closed",
					len(targets), h.forwards, h.stopped)
			}
		})
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// **A write is refused before it reaches the socket**, not refused by the
// server. Each of these is handed to do() on a live connection to a server
// that records everything it receives, and none of them arrives.
func TestAWriteIsRefusedWithoutTouchingTheWire(t *testing.T) {
	server := stocked(t, "Redis")
	conn, err := open(t.Context(), server.addr(), options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	before := len(server.seen())

	for _, write := range [][]string{
		{"SET", "greeting", "bye"},
		{"DEL", "greeting"},
		{"FLUSHALL"},
		{"FLUSHDB", "ASYNC"},
		{"CONFIG", "SET", "maxmemory", "1"},
		{"EVAL", "return redis.call('del', KEYS[1])", "1", "greeting"},
		{"SCRIPT", "LOAD", "return 1"},
		{"KEYS", "*"},
		{"MONITOR"},
		{"SUBSCRIBE", "news"},
		{"MEMORY", "PURGE"},
		{"CLIENT", "KILL", "TYPE", "normal"},
		{"SELECT", "3"},
		{"EXPIRE", "greeting", "1"},
		{"SHUTDOWN", "NOSAVE"},
	} {
		if _, err := conn.do(t.Context(), write...); !errors.Is(err, errNotARead) {
			t.Errorf("%v was not refused by this package (err = %v)", write, err)
		}
	}

	for _, cmd := range server.seen()[before:] {
		// The client's keep-alive PING may land while the loop runs; it is a
		// read and the client's own.
		if strings.EqualFold(cmd[0], "PING") {
			continue
		}
		t.Errorf("%v reached the server after the guard refused it", cmd)
	}
	if v, _ := conn.text(t.Context(), "GET", "greeting"); v != "hello" {
		t.Fatalf("after every write was refused, GET greeting = %q, want the untouched value", v)
	}
}

// The password is read for the pod the tunnel reached and goes down that
// tunnel: one tunnel per request, the first dial without a password, the
// second with it, through the same address. The answer names where it came
// from, never what it is.
func TestAPasswordGoesOnlyDownTheTunnelItWasReadFor(t *testing.T) {
	server := stocked(t, "Redis")
	server.password = "pod-zero-only"
	h := hostFor(t, server)
	h.forwardPod = "redis-0"
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis",
		Env: []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis-0-secret", "password")}}))
	h.add(t, "pods", pod("cache", "redis-1", corev1.Container{Name: "redis",
		Env: []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis-1-secret", "password")}}))
	h.secret("cache", "redis-0-secret", map[string]string{"password": "pod-zero-only"})
	h.secret("cache", "redis-1-secret", map[string]string{"password": "pod-one-only"})

	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := decode[instanceBody](t, w)
	if body.Pod != "redis-0" || body.Credential == nil || body.Credential.Pod != "redis-0" ||
		body.Credential.Secret != "redis-0-secret" {
		t.Fatalf("pod %q, credential %+v: want the one read for redis-0, the pod the tunnel reached", body.Pod, body.Credential)
	}
	if strings.Contains(w.Body.String(), "pod-zero-only") {
		t.Fatal("the password is in the answer")
	}
	if h.forwards != 1 {
		t.Fatalf("%d tunnels opened; the password must go down the one it was read for", h.forwards)
	}
	if len(h.reveals) != 1 || h.reveals[0] != "cache/redis-0-secret/password" {
		t.Fatalf("revealed %v, want only the reached pod's own reference", h.reveals)
	}
	if server.sent("pod-one-only") {
		t.Fatal("a password read for another pod reached this one")
	}

	// The first HELLO carried no password: an unauthenticated try comes first.
	var hellos []string
	for _, cmd := range server.seen() {
		if strings.EqualFold(cmd[0], "HELLO") {
			hellos = append(hellos, strings.Join(cmd, " "))
		}
	}
	if len(hellos) < 2 || strings.Contains(hellos[0], "AUTH") || !strings.Contains(hellos[1], "AUTH default") {
		t.Fatalf("handshakes = %q, want one without a password and then one with it", hellos)
	}
}

// A tunnel that cannot say which pod it reached is sent no password, and no
// Secret is read for it.
func TestNoPasswordIsSentWhenTheTunnelCannotNameItsPod(t *testing.T) {
	server := stocked(t, "Redis")
	server.password = "unnamed-pod"
	h := hostFor(t, server)
	h.podUnknown = true
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis",
		Env: []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "password")}}))
	h.secret("cache", "redis", map[string]string{"password": "unnamed-pod"})

	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379")
	p := decode[problemBody](t, w)
	if w.Code != http.StatusConflict || p.Code != CodePodUnknown || p.Kind != kindRefusal {
		t.Fatalf("answered %d %+v, want 409 pod_unknown", w.Code, p)
	}
	if len(h.reveals) != 0 || server.sent("unnamed-pod") {
		t.Fatalf("revealed %v / sent the password to a pod nobody named", h.reveals)
	}
}

// The three credential answers stay three, and a refusal stays apart from a
// failure (R5 §5): the person may not read the Secret (a refusal, 403), the
// console could not review the read (a failure, 503), the pod names nothing (a
// refusal, 409), the password found was refused by the server (a refusal, 409,
// naming the Secret and never the value).
func TestTheCredentialAnswersAreToldApart(t *testing.T) {
	for _, c := range []struct {
		name      string
		setup     func(*testing.T, *fakeHost)
		status    int
		code      string
		kind      string
		mentions  string
		forbidden string
	}{
		{"denied", func(t *testing.T, h *fakeHost) {
			h.revealErr = fmt.Errorf("get secrets/redis: %w", extension.ErrRevealDenied)
		},
			http.StatusForbidden, CodeSecretDenied, kindRefusal, "may not read", ""},
		{"unreviewed", func(t *testing.T, h *fakeHost) { h.revealErr = extension.ErrRevealUnreviewed },
			http.StatusServiceUnavailable, CodeSecretUnreviewed, kindFailure, "could not review", ""},
		{"nothing named", func(t *testing.T, h *fakeHost) {
			h.objects["pods"] = nil
			h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis"}))
		},
			http.StatusConflict, CodeNoCredential, kindRefusal, "dock", ""},
		{"wrong password", func(t *testing.T, h *fakeHost) {
			h.secret("cache", "redis", map[string]string{"redis-password": "rotated-away"})
		},
			http.StatusConflict, CodePasswordRefused, kindRefusal, "Secret redis, key redis-password", "rotated-away"},
		{"unknown cluster", func(t *testing.T, h *fakeHost) { h.revealErr = extension.ErrUnknownCluster },
			http.StatusNotFound, CodeUnknownCluster, kindAbsent, "kubeconfig", ""},
		// Recognized by the sentinel alone, as Host.Forward wraps it
		// with it, and not by matching its text, which may change.
		{"unknown cluster, at the tunnel", func(t *testing.T, h *fakeHost) {
			h.forwardErr = fmt.Errorf("%w %q", extension.ErrUnknownCluster, "kind")
		},
			http.StatusNotFound, CodeUnknownCluster, kindAbsent, "kubeconfig", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := stocked(t, "Redis")
			server.password = "the-real-one"
			h := hostFor(t, server)
			h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis",
				Env: []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "redis-password")}}))
			c.setup(t, h)

			w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/probe")
			p := decode[problemBody](t, w)
			if w.Code != c.status || p.Code != c.code || p.Kind != c.kind {
				t.Fatalf("answered %d %+v, want %d %s (%s)", w.Code, p, c.status, c.code, c.kind)
			}
			if !strings.Contains(p.Message, c.mentions) {
				t.Fatalf("the sentence %q does not say %q", p.Message, c.mentions)
			}
			if c.forbidden != "" && strings.Contains(w.Body.String(), c.forbidden) {
				t.Fatalf("the refused password is in the answer: %s", w.Body.String())
			}
			if h.stopped != h.forwards {
				t.Fatalf("%d tunnels opened and %d stopped", h.forwards, h.stopped)
			}
		})
	}
}

// **A scan is a sample in progress.** A round may come back empty with the
// scan unfinished - "nothing matched in this slice", not "nothing here" - and
// the answer carries done and the next cursor so the page can say which.
func TestAScanIsASampleInProgressAndSaysSo(t *testing.T) {
	server := newFakeRedis(t, "Redis")
	for _, key := range []string{"a1", "a2", "b1", "b2"} {
		server.set(0, key, value{kind: "string", s: key})
	}
	h := hostFor(t, server)

	first := decode[instanceBody](t, serve(t, h, "/x/kind/redis/instances/cache/redis/6379?match=b*"))
	if first.Scan == nil || len(first.Scan.Keys) != 0 || first.Scan.Done || first.Scan.Next != "1" {
		t.Fatalf("first round = %+v, want no keys, not done, next cursor 1", first.Scan)
	}
	if first.Overview == nil || first.Overview.Keys != 4 {
		t.Fatalf("overview = %+v: an empty round must not read as an empty database", first.Overview)
	}
	second := decode[instanceBody](t, serve(t, h, "/x/kind/redis/instances/cache/redis/6379?match=b*&cursor=1"))
	if second.Scan == nil || len(second.Scan.Keys) != 2 || !second.Scan.Done || second.Scan.Next != "" {
		t.Fatalf("second round = %+v, want b1 and b2, done", second.Scan)
	}
	if second.Scan.Keys[0].Type != "string" || second.Scan.Keys[0].TTL != "no expiry" {
		t.Fatalf("row = %+v, want its type and \"no expiry\"", second.Scan.Keys[0])
	}
}

// TTL's two sentinels are words, and a key gone between the SCAN and the TYPE
// is a row saying so rather than a failed page.
func TestTheExpiryColumnSaysWordsForRedissSentinels(t *testing.T) {
	for seconds, want := range map[int64]string{-1: "no expiry", -2: "gone", 59: "59s", 90: "1m", 7200: "2h", 172800: "2d"} {
		if got := ttlWords(seconds); got != want {
			t.Errorf("ttlWords(%d) = %q, want %q", seconds, got, want)
		}
	}
	server := newFakeRedis(t, "Redis")
	conn, err := open(t.Context(), server.addr(), options{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := conn.typeOf(t.Context(), "deleted-meanwhile"); got != "gone" {
		t.Fatalf("a key deleted between SCAN and TYPE reads %q, want gone", got)
	}
}

// A key opens bounded and a page at a time, and every partial answer says
// what was cut.
func TestAKeyOpensBoundedAndSaysWhatWasCut(t *testing.T) {
	server := stocked(t, "Valkey")
	big := strings.Repeat("é", 40*1024) // 80 KiB of two-byte runes
	server.set(0, "big", value{kind: "string", s: big})
	long := make([]string, 250)
	for i := range long {
		long[i] = fmt.Sprintf("item-%03d", i)
	}
	server.set(0, "long", value{kind: "list", items: long})
	h := hostFor(t, server)

	open := func(key string) instanceBody {
		w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key="+url.QueryEscape(key))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", key, w.Code, w.Body.String())
		}
		return decode[instanceBody](t, w)
	}

	if k := open("big").Key; len(k.Entries) != 1 || len(k.Entries[0].Value) > maxValueBytes ||
		k.Entries[0].Binary || !strings.Contains(k.Note, "the first 64.0 KiB") || k.Next != "" {
		t.Fatalf("big string: %d bytes shown, binary %v, note %q, next %q", len(k.Entries[0].Value),
			k.Entries[0].Binary, k.Note, k.Next)
	}
	if k := open("long").Key; len(k.Entries) != pageSize || k.Note != "" || k.Next == "" ||
		k.PageSize != pageSize || k.Length == nil || *k.Length != 250 {
		t.Fatalf("long list: %d entries, note %q, next %q, page size %d, length %v, want one page of %d "+
			"with the next one's token and the whole length", len(k.Entries), k.Note, k.Next, k.PageSize,
			k.Length, pageSize)
	}
	if k := open("user:1").Key; len(k.Entries) != 2 || k.Entries[0].Field != "lang" || k.Note != "" || k.Next != "" {
		t.Fatalf("hash: %+v", k)
	}
	k := open("events").Key
	if len(k.Entries) != 3 || k.Next != "" || k.Length == nil || *k.Length != 3 {
		t.Fatalf("stream: %+v, want its three entries on one page", k)
	}
	if e := k.Entries[0]; e.Field != "1-0" || e.Value != `{"user":"42","action":"login"}` || e.Binary {
		t.Fatalf("a stream entry reads %+v, want its ID and its fields as a JSON object in order", e)
	}
	if k := open("doc").Key; !strings.Contains(k.Note, `"ReJSON-RL"`) {
		t.Fatalf("module type: %+v", k)
	}

	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key=expired-meanwhile")
	if p := decode[problemBody](t, w); w.Code != http.StatusNotFound || p.Code != CodeKeyGone || p.Kind != kindAbsent {
		t.Fatalf("a key that is not there answered %d %+v, want 404 key_gone", w.Code, p)
	}
}

// A hash or a set is scanned a page at a time: a key of a thousand fields
// sends a page across the tunnel, never the thousand; and every element of a
// collection is cut to maxElementBytes with a mark saying so.
func TestCollectionsAreReadAPageAtATimeAndLongElementsClipped(t *testing.T) {
	server := stocked(t, "Redis")
	huge := strings.Repeat("x", 100*1024)
	fields := map[string]string{"a-huge": huge}
	members := []string{huge}
	for i := range 999 {
		fields[fmt.Sprintf("f-%04d", i)] = "v"
		members = append(members, fmt.Sprintf("m-%04d", i))
	}
	server.set(0, "wide", value{kind: "hash", hash: fields})
	server.set(0, "many", value{kind: "set", items: members})
	server.set(0, "fat", value{kind: "list", items: []string{"small", huge}})
	h := hostFor(t, server)

	type opened struct {
		Entries []Entry
		Note    string
		Next    string
	}
	open := func(key string) opened {
		t.Helper()
		w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key="+url.QueryEscape(key))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", key, w.Code, w.Body.String())
		}
		body := decode[instanceBody](t, w)
		if body.Key == nil {
			t.Fatalf("%s: no key in %s", key, w.Body.String())
		}
		return opened{Entries: body.Key.Entries, Note: body.Key.Note, Next: body.Key.Next}
	}
	clipped := func(t *testing.T, e Entry) {
		t.Helper()
		if !e.Clipped || e.Bytes != int64(len(huge)) || len(e.Value) > maxElementBytes {
			t.Errorf("a %d-byte element came back clipped=%v bytes=%d with %d bytes shown, want the mark, "+
				"its length and at most %d bytes", len(huge), e.Clipped, e.Bytes, len(e.Value), maxElementBytes)
		}
	}

	for _, key := range []string{"wide", "many"} {
		before := server.scannedSoFar()
		k := open(key)
		crossed := server.scannedSoFar() - before
		if len(k.Entries) != pageSize || k.Next == "" {
			t.Errorf("%s: %d entries, next %q, want a page of %d and the next one's token", key, len(k.Entries),
				k.Next, pageSize)
		}
		if crossed > pageSize {
			t.Errorf("%s: the scans carried %d elements to show %d; the read did not stop at a page", key, crossed, pageSize)
		}
		if want := "One element is longer than 8.0 KiB and only its start is shown."; k.Note != want {
			t.Errorf("%s: note %q, want %q and nothing about the page, which length and next say", key, k.Note, want)
		}
		var cut []Entry
		for _, e := range k.Entries {
			if e.Clipped {
				cut = append(cut, e)
			}
		}
		if len(cut) != 1 {
			t.Errorf("%s: %d elements are marked clipped, want the one long one", key, len(cut))
		} else {
			clipped(t, cut[0])
		}
	}

	k := open("fat")
	if len(k.Entries) != 2 || k.Entries[0].Clipped || k.Next != "" {
		t.Fatalf("fat list: %d entries, next %q", len(k.Entries), k.Next)
	}
	clipped(t, k.Entries[1])
	if !strings.Contains(k.Note, "One element is longer than 8.0 KiB") {
		t.Errorf("a list drawn whole with one element cut says %q, want the cut said", k.Note)
	}

	for _, cmd := range server.seen() {
		if cmd[0] == "HGETALL" || cmd[0] == "SMEMBERS" {
			t.Errorf("%v reached the server: a whole hash or set read", cmd)
		}
	}
}

// pageThrough opens a key and follows its next token to the end, and answers
// every page's entries, each page checked against the length it says.
func pageThrough(t *testing.T, h *fakeHost, key string) [][]Entry {
	t.Helper()
	var pages [][]Entry
	next := ""
	for {
		target := "/x/kind/redis/instances/cache/redis/6379/key?key=" + url.QueryEscape(key)
		if next != "" {
			target += "&page=" + url.QueryEscape(next)
		}
		w := serve(t, h, target)
		if w.Code != http.StatusOK {
			t.Fatalf("%s, page %q: status %d: %s", key, next, w.Code, w.Body.String())
		}
		k := decode[instanceBody](t, w).Key
		if k.PageSize != pageSize {
			t.Fatalf("%s: page size %d, want %d", key, k.PageSize, pageSize)
		}
		pages = append(pages, k.Entries)
		if k.Next == "" {
			return pages
		}
		if k.Next == next {
			t.Fatalf("%s: page %q hands back itself as the next", key, next)
		}
		if len(pages) > 50 {
			t.Fatalf("%s: more than 50 pages; the token does not move", key)
		}
		next = k.Next
	}
}

// Every type is read a page at a time to its end: each page holds at most a
// page, the token reads the next one, the last page has no token, and the
// pages together are the key - nothing lost, nothing read twice. A stream is
// read with XRANGE, and always with a COUNT.
func TestEveryTypeIsPagedToItsEndLosingAndRepeatingNothing(t *testing.T) {
	const n = 250
	server := newFakeRedis(t, "Redis")
	fields := map[string]string{}
	var items []string
	var stream []streamItem
	for i := range n {
		fields[fmt.Sprintf("f-%03d", i)] = fmt.Sprintf("v-%03d", i)
		items = append(items, fmt.Sprintf("e-%03d", i))
		// Several entries in one millisecond, so the next page's start has to
		// step the sequence, not the time.
		stream = append(stream, streamItem{id: fmt.Sprintf("%d-%d", 1730800000000+i/3, i%3),
			fields: []string{"n", strconv.Itoa(i)}})
	}
	server.set(0, "h", value{kind: "hash", hash: fields})
	server.set(0, "s", value{kind: "set", items: items})
	server.set(0, "l", value{kind: "list", items: items})
	server.set(0, "z", value{kind: "zset", items: items})
	server.set(0, "x", value{kind: "stream", stream: stream})
	h := hostFor(t, server)

	for _, key := range []string{"h", "s", "l", "z", "x"} {
		t.Run(key, func(t *testing.T) {
			pages := pageThrough(t, h, key)
			if len(pages) != 3 {
				t.Errorf("%d pages, want 3 of at most %d for %d elements", len(pages), pageSize, n)
			}
			seen := map[string]int{}
			for i, page := range pages {
				if len(page) > pageSize {
					t.Errorf("page %d holds %d, more than a page of %d", i, len(page), pageSize)
				}
				for _, e := range page {
					seen[e.Field+"="+e.Value]++
				}
			}
			if len(seen) != n {
				t.Errorf("the pages hold %d distinct elements, want all %d", len(seen), n)
			}
			for element, times := range seen {
				if times != 1 {
					t.Errorf("%s was read %d times", element, times)
				}
			}
		})
	}

	ranges := 0
	for _, cmd := range server.seen() {
		if strings.EqualFold(cmd[0], "XRANGE") {
			ranges++
			if len(cmd) != 6 || !strings.EqualFold(cmd[4], "COUNT") || cmd[5] != strconv.Itoa(pageSize) {
				t.Errorf("%v reached the server: a stream read without a COUNT of a page", cmd)
			}
		}
	}
	if ranges != 3 {
		t.Errorf("%d XRANGE reads for three pages of a stream, want 3", ranges)
	}
}

// A scan whose rounds come back short still makes a page, keeping every
// element a round returned even past a page - the cursor has moved past them -
// and a scan that keeps answering very short rounds is stopped after
// maxPageRounds with the cursor handed back, so the next page goes on from
// there and nothing is lost.
func TestAScanOfShortRoundsKeepsEverythingAndStaysBounded(t *testing.T) {
	const n = 250
	for _, c := range []struct {
		step      int
		firstPage int
	}{
		// Rounds of 7: the fifteenth round passes a page, and all of it is kept.
		{7, 105},
		// Rounds of 1: the round bound stops the page short of a page.
		{1, maxPageRounds},
	} {
		t.Run(fmt.Sprintf("rounds of %d", c.step), func(t *testing.T) {
			server := newFakeRedis(t, "Redis")
			server.scanStep = c.step
			fields := map[string]string{}
			for i := range n {
				fields[fmt.Sprintf("f-%03d", i)] = "v"
			}
			server.set(0, "h", value{kind: "hash", hash: fields})
			h := hostFor(t, server)

			pages := pageThrough(t, h, "h")
			if len(pages[0]) != c.firstPage {
				t.Errorf("the first page holds %d, want %d", len(pages[0]), c.firstPage)
			}
			seen := map[string]bool{}
			for _, page := range pages {
				for _, e := range page {
					if seen[e.Field] {
						t.Errorf("%s was read twice", e.Field)
					}
					seen[e.Field] = true
				}
			}
			if len(seen) != n {
				t.Errorf("the pages hold %d fields, want all %d", len(seen), n)
			}
			scans := 0
			for _, cmd := range server.seen() {
				if strings.EqualFold(cmd[0], "HSCAN") {
					scans++
				}
			}
			if most := len(pages) * maxPageRounds; scans > most {
				t.Errorf("%d HSCAN rounds for %d pages, more than %d a page", scans, len(pages), maxPageRounds)
			}
		})
	}
}

// A page token that is not one this server hands out is a bad request, said
// before anything is dialled; one handed out for another kind of key is a bad
// request too, said once the key's type is known.
func TestABadPageTokenIsRefused(t *testing.T) {
	h := newHost()
	for _, page := range []string{"zz", "h:", "h:abc", "h:0", "o:0", "o:-1", "o:+5", "x:1", "x:1-a", "q:5",
		"o:" + strings.Repeat("9", 70)} {
		w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key=k&page="+url.QueryEscape(page))
		if w.Code != http.StatusBadRequest || decode[problemBody](t, w).Code != CodeBadRequest {
			t.Errorf("page %q answered %d %s, want 400 bad_request", page, w.Code, w.Body.String())
		}
	}
	if h.forwards != 0 {
		t.Fatalf("%d tunnel(s) opened for malformed page tokens", h.forwards)
	}

	server := stocked(t, "Redis")
	h = hostFor(t, server)
	for _, c := range []struct{ key, page string }{
		{"queue", "h:5"}, {"user:1", "o:100"}, {"tags", "h:5"}, {"events", "o:100"}, {"greeting", "o:100"},
		{"doc", "x:1-0"},
	} {
		w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key="+url.QueryEscape(c.key)+
			"&page="+url.QueryEscape(c.page))
		p := decode[problemBody](t, w)
		if w.Code != http.StatusBadRequest || p.Code != CodeBadRequest || !strings.Contains(p.Message, "first page") {
			t.Errorf("%s with page %q answered %d %+v, want 400 saying to open it from its first page",
				c.key, c.page, w.Code, p)
		}
	}
}

// A binary element carries its bytes - as cut to the element cap - in
// standard base64, beside the U+FFFD rendering: the rendering cannot be turned
// back into them, and the browser needs them to inflate or draw in hex. A text
// element carries none, and neither does a stream entry, which is many values.
func TestABinaryElementCarriesItsBytes(t *testing.T) {
	server := newFakeRedis(t, "Redis")
	gzip := "\x1f\x8b\x08\x00\xff\xfe payload"
	long := "\xff" + strings.Repeat("y", 20*1024)
	server.set(0, "h", value{kind: "hash", hash: map[string]string{"blob": gzip, "text": "plain"}})
	server.set(0, "l", value{kind: "list", items: []string{long}})
	server.set(0, "str", value{kind: "string", s: gzip})
	server.set(0, "x", value{kind: "stream", stream: []streamItem{
		{id: "5-0", fields: []string{"a", "1", "b", "<&>", "a", "2"}},
		{id: "5-1", fields: []string{"bin", "\xff"}},
	}})
	h := hostFor(t, server)

	open := func(key string) []Entry {
		t.Helper()
		w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key="+key)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", key, w.Code, w.Body.String())
		}
		return decode[instanceBody](t, w).Key.Entries
	}
	bytesOf := func(t *testing.T, e Entry, want string) {
		t.Helper()
		got, err := base64.StdEncoding.DecodeString(e.Raw)
		if !e.Binary || err != nil || string(got) != want {
			t.Errorf("a binary element came back binary=%v raw=%q (%v), want the %d bytes it holds",
				e.Binary, e.Raw, err, len(want))
		}
		if !utf8.ValidString(e.Value) || !strings.Contains(e.Value, "�") {
			t.Errorf("value %q, want the U+FFFD rendering kept", e.Value)
		}
	}

	hash := open("h")
	if len(hash) != 2 || hash[0].Field != "blob" {
		t.Fatalf("hash: %+v", hash)
	}
	bytesOf(t, hash[0], gzip)
	if hash[1].Raw != "" || hash[1].Binary {
		t.Errorf("a text element carries raw %q, binary %v; want neither", hash[1].Raw, hash[1].Binary)
	}
	bytesOf(t, open("str")[0], gzip)
	list := open("l")
	bytesOf(t, list[0], long[:maxElementBytes])

	stream := open("x")
	if len(stream) != 2 || stream[0].Value != `{"a":"2","b":"<&>"}` || stream[0].Binary {
		t.Errorf("a stream entry with a repeated field reads %+v, want the field once with its last value", stream)
	}
	if len(stream) == 2 && (!stream[1].Binary || stream[1].Raw != "" || stream[1].Value != `{"bin":"�"}`) {
		t.Errorf("a stream entry with a binary value reads %+v, want binary, rendered, no raw", stream[1])
	}
}

// **Keys are bytes.** A key that is not UTF-8 is listed with its raw form, and
// the key route opens it from that form - otherwise it would render as U+FFFD
// and could never be opened again.
func TestAKeyThatIsNotTextCanStillBeOpened(t *testing.T) {
	server := newFakeRedis(t, "Redis")
	binary := "bin:\xff\xfe"
	server.set(0, binary, value{kind: "string", s: "\x00\x01payload"})
	h := hostFor(t, server)

	list := decode[instanceBody](t, serve(t, h, "/x/kind/redis/instances/cache/redis/6379"))
	if list.Scan == nil || len(list.Scan.Keys) != 1 || list.Scan.Keys[0].Raw == "" {
		t.Fatalf("scan = %+v, want the key with its raw form", list.Scan)
	}
	raw := list.Scan.Keys[0].Raw
	if decoded, _ := base64.RawURLEncoding.DecodeString(raw); string(decoded) != binary {
		t.Fatalf("raw %q does not decode to the key's bytes", raw)
	}
	opened := decode[instanceBody](t, serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?raw="+raw))
	if opened.Key == nil || opened.Key.Raw != raw || opened.Key.Type != "string" {
		t.Fatalf("opened = %+v, want the same key back", opened.Key)
	}
}

// A Cluster node is browsed and said to show its own slots only; a Sentinel is
// named and not browsed: this viewer reads single nodes.
func TestClusterAndSentinelAreNamedAndNotHandled(t *testing.T) {
	cluster := stocked(t, "Redis")
	cluster.mode = "cluster"
	body := decode[instanceBody](t, serve(t, hostFor(t, cluster), "/x/kind/redis/instances/cache/redis/6379"))
	if body.Server.Mode != "cluster" || !strings.Contains(body.Server.Note, "own slots") || body.Scan == nil {
		t.Fatalf("a Cluster node answered %+v, want it browsed and said to show its own slots", body.Server)
	}

	sentinel := newFakeRedis(t, "Redis")
	sentinel.mode = "sentinel"
	h := hostFor(t, sentinel)
	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379")
	body = decode[instanceBody](t, w)
	if w.Code != http.StatusOK || body.Server.Mode != "sentinel" || body.Scan != nil || !strings.Contains(body.Server.Note, "Sentinel") {
		t.Fatalf("a Sentinel answered %d %+v, want it named and not scanned", w.Code, body)
	}
	for _, cmd := range sentinel.seen() {
		if strings.EqualFold(cmd[0], "SCAN") || strings.EqualFold(cmd[0], "DBSIZE") {
			t.Fatalf("a Sentinel was sent %v", cmd)
		}
	}
}

// A string above maxStringRead is not read at all: GET has no range, so it
// would cross the tunnel into the console whole to show 64 KiB of it. The note
// says so, and no GET reaches the server.
func TestAStringTooLargeToReadIsNotRead(t *testing.T) {
	server := newFakeRedis(t, "Redis")
	server.set(0, "huge", value{kind: "string", s: "x", strlen: 512 << 20})
	h := hostFor(t, server)

	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key=huge")
	body := decode[instanceBody](t, w)
	if w.Code != http.StatusOK || body.Key == nil || len(body.Key.Entries) != 0 ||
		!strings.Contains(body.Key.Note, "none of it is shown") || body.Key.Length == nil || *body.Key.Length != 512<<20 {
		t.Fatalf("a 512 MiB string answered %d %+v, want its size and no value", w.Code, body.Key)
	}
	for _, cmd := range server.seen() {
		if strings.EqualFold(cmd[0], "GET") {
			t.Fatalf("%v was sent for a string larger than this console reads", cmd)
		}
	}
}

// A database the server does not have fails the dial's own SELECT, and is said
// as that - an absence - rather than as a server that did not answer like a
// Redis. A Cluster node has database 0 only.
func TestADatabaseTheServerDoesNotHaveIsSaidAsThat(t *testing.T) {
	server := stocked(t, "Redis")
	h := hostFor(t, server)
	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379?db=20")
	if p := decode[problemBody](t, w); w.Code != http.StatusNotFound || p.Code != CodeNoSuchDatabase || p.Kind != kindAbsent ||
		!strings.Contains(p.Message, "no database 20") {
		t.Fatalf("db=20 answered %d %+v, want 404 no_such_database", w.Code, p)
	}

	cluster := stocked(t, "Redis")
	cluster.mode = "cluster"
	w = serve(t, hostFor(t, cluster), "/x/kind/redis/instances/cache/redis/6379/probe?db=1")
	if p := decode[problemBody](t, w); w.Code != http.StatusNotFound || p.Code != CodeNoSuchDatabase {
		t.Fatalf("db=1 on a Cluster node answered %d %+v, want no_such_database", w.Code, p)
	}
}

// The password is withheld from an error judged whole, before the error is
// shortened: a message long enough to be cut, with the password past the cut
// or across it, must not leave a prefix of it behind.
func TestThePasswordIsScrubbedBeforeTheErrorIsShortened(t *testing.T) {
	password := "correct-horse-battery-staple"
	long := strings.Repeat("a long sentence that fills the first line ", 6) + "and then " + password
	for _, err := range []error{
		errors.New(long),
		errors.New(strings.Repeat("x", 230) + password),
		errors.New("first line\n" + password),
	} {
		if got := scrubbed(err, password); strings.Contains(got, "correct-") {
			t.Errorf("scrubbed(%q) = %q, which still carries the password", err, got)
		}
	}
	if got := scrubbed(errors.New("ERR nothing secret here"), password); got != "ERR nothing secret here" {
		t.Errorf("a harmless error was changed: %q", got)
	}
}

// A probe says what the server itself says it is to its replicas - the role
// the inventory puts in place of the one the labels guessed
// - in this console's words, and how many replicas a primary has attached.
func TestAProbeSaysTheRoleTheServerReports(t *testing.T) {
	for _, c := range []struct {
		said     string
		want     string
		replicas *int
	}{
		{"master", rolePrimary, func() *int { n := 2; return &n }()},
		{"slave", roleReplica, nil},
		{"", "", nil},
	} {
		server := newFakeRedis(t, "Redis")
		server.role = c.said
		h := hostFor(t, server)
		body := decode[instanceBody](t, serve(t, h, "/x/kind/redis/instances/cache/redis/6379/probe"))
		if body.Server.Role != c.want {
			t.Errorf("INFO replication role:%q read as %q, want %q", c.said, body.Server.Role, c.want)
		}
		if (body.Server.Replicas == nil) != (c.replicas == nil) ||
			(c.replicas != nil && *body.Server.Replicas != *c.replicas) {
			t.Errorf("role:%q: replicas = %v, want %v", c.said, body.Server.Replicas, c.replicas)
		}
	}
}

// Each key of a scan round carries what it costs, read
// with MEMORY USAGE - a read on the allowlist - and the round's keys are read
// several at a time without one escaping the allowlist.
func TestAScanRoundSaysWhatEachKeyCosts(t *testing.T) {
	server := stocked(t, "Redis")
	for i := range 40 {
		server.set(0, fmt.Sprintf("bulk:%02d", i), value{kind: "string", s: "v"})
	}
	h := hostFor(t, server)
	body := decode[instanceBody](t, serve(t, h, "/x/kind/redis/instances/cache/redis/6379"))
	if body.Scan == nil || len(body.Scan.Keys) == 0 {
		t.Fatalf("scan = %+v", body.Scan)
	}
	for _, k := range body.Scan.Keys {
		if k.Memory == nil || *k.Memory != 72 {
			t.Fatalf("%s: memory = %v, want the 72 bytes the server said", k.Name, k.Memory)
		}
		if k.Type == "" || k.TTL == "" {
			t.Fatalf("%s: a row lost its type or expiry to the concurrent reads: %+v", k.Name, k)
		}
	}
	for _, cmd := range server.seen() {
		if _, ok := handshake[strings.ToUpper(cmd[0])]; ok {
			continue
		}
		if err := allowed(cmd); err != nil {
			t.Fatalf("%q reached the server: %v", cmd, err)
		}
	}
}

// A sorted set opens with its scores, in either shape the protocol has.
func TestASortedSetOpensWithItsScores(t *testing.T) {
	for _, nested := range []bool{false, true} {
		server := stocked(t, "Redis")
		server.nestedScores = nested
		h := hostFor(t, server)
		w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/key?key=board")
		if w.Code != http.StatusOK {
			t.Fatalf("nested %v: status %d: %s", nested, w.Code, w.Body.String())
		}
		k := decode[instanceBody](t, w).Key
		if len(k.Entries) != 2 || k.Entries[0].Value != "p1" || k.Entries[0].Score != "1.5" || k.Entries[1].Score != "2.5" {
			t.Fatalf("nested %v: entries = %+v, want p1 1.5 and p2 2.5", nested, k.Entries)
		}
	}
}
