package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// Host.Reveal is the console's reveal door handed to an extension:
// reviewed for the person by name, recorded as the
// extension's with the attempt before the read, refused when either cannot be
// done, and the value in no record, no log line and no error. Driven against
// surfaceServer, whose Secret `shop/db` holds `password: hunter2` and which
// records every path it is asked - the counting fake that says what was never
// read.

const revealExt = "alfa"

// hostRevealServer is a console over surfaceServer under the server profile,
// whose reviews answer the Secret question with secrets(q), and whose audit log
// writes to out.
func hostRevealServer(t *testing.T, secrets func(accesstest.Question) (bool, error), out io.Writer) (*Server, *surfaceServer) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Resource == "secrets" {
			return secrets(q)
		}
		return true, nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(out)})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func allowSecrets(accesstest.Question) (bool, error) { return true, nil }
func denySecrets(accesstest.Question) (bool, error)  { return false, nil }
func cannotAsk(accesstest.Question) (bool, error) {
	return false, errors.New("the authorizer did not answer")
}

// asGrace is a request from grace, signed in through the identity provider.
func asGrace() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x/prod/alfa/", nil).WithContext(grace())
}

// processLog captures the process log for the test.
func processLog(t *testing.T) *lockedBuffer {
	t.Helper()
	captured := &lockedBuffer{}
	previous := log.Writer()
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	return captured
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// noValue fails when the Secret's value, plain or as the API server sent it,
// is in any of what.
func noValue(t *testing.T, where string, what ...string) {
	t.Helper()
	for _, text := range what {
		if strings.Contains(text, "hunter2") || strings.Contains(text, "aHVudGVyMg") {
			t.Errorf("the Secret's value reached %s:\n%s", where, text)
		}
	}
}

func TestARevealThroughTheHostIsReviewedByNameAndRecordedAsTheExtensions(t *testing.T) {
	logged := processLog(t)
	sink := &revealSink{}
	s, _ := hostRevealServer(t, allowSecrets, sink)
	value, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", "db", "password")
	if err != nil || value != "hunter2" {
		t.Fatalf("the reveal answered %q, %v", value, err)
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Phase != audit.Attempt || got[1].Phase != audit.Outcome || got[0].ID != got[1].ID {
		t.Fatalf("one reveal wrote %+v, want an attempt and its outcome", got)
	}
	for _, r := range got {
		if r.Act != audit.Reveal || r.Verb != "get" || r.Resource != "secrets" || r.Namespace != "shop" ||
			r.Name != "db" || r.Key != "password" || r.Extension != revealExt || r.Who != "grace@example.com" ||
			r.Decision != string(access.Allowed) {
			t.Errorf("the %s record reads %+v", r.Phase, r)
		}
	}
	if got[1].Outcome != audit.Applied {
		t.Errorf("the outcome is %q, want %q", got[1].Outcome, audit.Applied)
	}
	line := logged.String()
	if !strings.Contains(line, "reveal: ") || !strings.Contains(line, "extension="+revealExt) ||
		!strings.Contains(line, "key=password") || !strings.Contains(line, "outcome=ok") {
		t.Errorf("the process line does not say the reveal, its key and its extension:\n%s", line)
	}
	noValue(t, "the audit record", sink.buf.String())
	noValue(t, "the process log", line)
}

// The reviewer asks about the person, by the Secret's name: a review by kind
// would over-grant a Role that names one Secret.
func TestARevealThroughTheHostIsAskedAboutThePersonByName(t *testing.T) {
	var asked []accesstest.Question
	var mu sync.Mutex
	s, _ := hostRevealServer(t, func(q accesstest.Question) (bool, error) {
		mu.Lock()
		asked = append(asked, q)
		mu.Unlock()
		return true, nil
	}, io.Discard)
	if _, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", "db", "password"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) == 0 {
		t.Fatal("the reveal asked no review")
	}
	for _, q := range asked {
		if q.User != "grace@example.com" || q.Self || q.Attributes.Verb != "get" || q.Attributes.Name != "db" ||
			q.Attributes.Namespace != "shop" {
			t.Errorf("the reveal was reviewed as %+v, want grace's get of secrets/db in shop", q)
		}
	}
}

func TestARevealThePersonMayNotMakeReadsNothing(t *testing.T) {
	logged := processLog(t)
	sink := &revealSink{}
	s, fake := hostRevealServer(t, denySecrets, sink)
	value, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", "db", "password")
	if !errors.Is(err, extension.ErrRevealDenied) || errors.Is(err, extension.ErrRevealUnreviewed) || value != "" {
		t.Fatalf("a denied reveal answered %q, %v; want ErrRevealDenied and nothing else", value, err)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read although the person may not: %v", fake.asked())
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Decision != string(access.Denied) || got[1].Outcome != audit.Refused ||
		got[1].Extension != revealExt || got[1].Key != "password" {
		t.Fatalf("a denied reveal wrote %+v, want an attempt and a refused outcome naming the extension", got)
	}
	if !strings.Contains(logged.String(), "outcome=refused") {
		t.Errorf("the process line does not say the refusal:\n%s", logged.String())
	}
}

func TestARevealWhoseReviewCannotBeMadeReadsNothing(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostRevealServer(t, cannotAsk, sink)
	_, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", "db", "password")
	if !errors.Is(err, extension.ErrRevealUnreviewed) || errors.Is(err, extension.ErrRevealDenied) {
		t.Fatalf("a reveal nobody could review answered %v; want ErrRevealUnreviewed and not a denial", err)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read although the review could not be made: %v", fake.asked())
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Decision != string(access.Unreviewable) || got[1].Outcome != audit.Refused {
		t.Fatalf("an unreviewable reveal wrote %+v, want an attempt and a refused outcome", got)
	}
}

// On the desktop a read whose review could not be made is let through,
// and a list would be. A reveal is not: it is RevealEnv's door, and only an
// allowed answer reads. This is the case only the first check refuses -
// Gate.Read, behind it, would let it through.
func TestARevealWhoseReviewCannotBeMadeOnTheDesktopReadsNothing(t *testing.T) {
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Resource == "secrets" {
			return cannotAsk(q)
		}
		return true, nil
	}}
	s, err := New(Options{Clusters: manager, Access: access.New(access.Options{Reviews: reviews.For})})
	if err != nil {
		t.Fatal(err)
	}
	ctx := httptest.NewRequest(http.MethodGet, "/x/prod/alfa/", nil)
	_, err = s.hostFor(revealExt).Reveal(ctx, "prod", "shop", "db", "password")
	if !errors.Is(err, extension.ErrRevealUnreviewed) {
		t.Fatalf("a desktop reveal nobody could review answered %v; want ErrRevealUnreviewed", err)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read on the desktop although the review could not be made: %v", fake.asked())
	}
}

// Denied and unrecordable at once, under the server profile: the denial wins.
// The person's own answer is the one to give them, and an unwritten record is
// not a reason to call a refusal an outage. Pins the first check, which Gate.Read
// would otherwise cover silently with the other sentinel.
func TestADeniedRevealWhoseRecordCannotBeWrittenIsADenial(t *testing.T) {
	sink := &revealSink{broken: true}
	s, fake := hostRevealServer(t, denySecrets, sink)
	_, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", "db", "password")
	if !errors.Is(err, extension.ErrRevealDenied) || errors.Is(err, extension.ErrRevealUnreviewed) {
		t.Fatalf("a denied, unrecordable reveal answered %v; want ErrRevealDenied and not ErrRevealUnreviewed", err)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read: %v", fake.asked())
	}
}

func TestARevealWhoseAttemptCannotBeRecordedReadsNothing(t *testing.T) {
	sink := &revealSink{broken: true}
	s, fake := hostRevealServer(t, allowSecrets, sink)
	value, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", "db", "password")
	if !errors.Is(err, extension.ErrRevealUnreviewed) || !errors.Is(err, access.ErrUnrecorded) || value != "" {
		t.Fatalf("an unrecorded reveal answered %q, %v; want ErrRevealUnreviewed for an unwritten record", value, err)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read although the reveal's record could not be written: %v", fake.asked())
	}
}

// An absence is not a refusal and not a failure: the person may read it, and
// there is nothing of that name. Each is recorded as a failed outcome with its
// word.
func TestARevealOfWhatIsNotThereSaysWhichIsNotThere(t *testing.T) {
	for _, c := range []struct {
		name, secret, key string
		want              error
		reason            string
	}{
		{"no such key", "db", "nope", extension.ErrNoSuchKey, noKey},
		{"no such Secret", "gone", "password", extension.ErrNoSuchSecret, "NotFound"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sink := &revealSink{}
			s, _ := hostRevealServer(t, allowSecrets, sink)
			_, err := s.hostFor(revealExt).Reveal(asGrace(), "prod", "shop", c.secret, c.key)
			if !errors.Is(err, c.want) || errors.Is(err, extension.ErrRevealDenied) || errors.Is(err, extension.ErrRevealUnreviewed) {
				t.Fatalf("answered %v, want %v", err, c.want)
			}
			got := sink.records(t)
			if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != c.reason {
				t.Fatalf("wrote %+v, want a failed outcome for %q", got, c.reason)
			}
			noValue(t, "the error", err.Error())
		})
	}
}

func TestARevealOnAClusterNotInTheKubeconfigIsUnknown(t *testing.T) {
	s, fake := hostRevealServer(t, allowSecrets, io.Discard)
	_, err := s.hostFor(revealExt).Reveal(asGrace(), "nope", "shop", "db", "password")
	if !errors.Is(err, extension.ErrUnknownCluster) {
		t.Fatalf("answered %v, want ErrUnknownCluster", err)
	}
	if readTheSecret(fake) {
		t.Error("a reveal on an unknown cluster read a Secret")
	}
}

// A door handed to no extension has nobody to record the reveal as, so it
// reveals nothing.
func TestARevealThroughAHostWithNoExtensionIsRefused(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostRevealServer(t, allowSecrets, sink)
	if _, err := s.Host().Reveal(asGrace(), "prod", "shop", "db", "password"); err == nil {
		t.Fatal("a host handed to no extension revealed a Secret")
	}
	if readTheSecret(fake) || len(sink.records(t)) != 0 {
		t.Errorf("a refused anonymous reveal read or recorded something: %v", fake.asked())
	}
}

// Every extension is handed its own door by the mount, and it carries its key.
func TestEveryExtensionIsHandedADoorCarryingItsKey(t *testing.T) {
	s, _ := hostRevealServer(t, allowSecrets, io.Discard)
	if h, ok := s.hostFor("bravo").(host); !ok || h.ext != "bravo" {
		t.Fatalf("hostFor(bravo) = %#v", s.hostFor("bravo"))
	}
}

// List does not read Secrets, in any spelling, and asks the cluster nothing.
// Nor anything whose names are not one path segment each.
func TestHostListRefusesSecretsBeforeReadingAnything(t *testing.T) {
	s, fake := hostRevealServer(t, allowSecrets, io.Discard)
	for _, resource := range []string{"secrets", "Secrets", " secrets"} {
		before := len(fake.asked())
		page, err := s.hostFor(revealExt).List(asGrace(), "prod", extension.ListQuery{
			GVR: schema.GroupVersionResource{Version: "v1", Resource: resource}, Namespace: "shop"})
		if !errors.Is(err, extension.ErrSecretsNotListed) || len(page.Items) != 0 {
			t.Errorf("List of %q answered %d items, %v; want ErrSecretsNotListed", resource, len(page.Items), err)
		}
		if after := fake.asked(); len(after) != before {
			t.Errorf("List of %q asked the cluster %v", resource, after[before:])
		}
	}
	// Spellings client-go's path.Join cleans into the Secrets URL, and a
	// version or namespace that climbs: refused as not one path segment, and
	// the cluster asked nothing (none may go out as
	// GET .../secrets).
	for _, q := range []extension.ListQuery{
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets/"}, Namespace: "shop"},
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "./secrets"}, Namespace: "shop"},
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "x/../secrets"}, Namespace: "shop"},
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: ".."}, Namespace: "shop"},
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: ""}, Namespace: "shop"},
		{GVR: schema.GroupVersionResource{Group: "x", Version: "../../api/v1", Resource: "configmaps"}},
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Namespace: "shop/secrets/x/.."},
	} {
		before := len(fake.asked())
		page, err := s.hostFor(revealExt).List(asGrace(), "prod", q)
		if err == nil || len(page.Items) != 0 {
			t.Errorf("List of %+v answered %d items, %v; want a refusal", q, len(page.Items), err)
		}
		if after := fake.asked(); len(after) != before {
			t.Errorf("List of %+v asked the cluster %v", q, after[before:])
		}
	}
	// An ordinary list still reads.
	if _, err := s.hostFor(revealExt).List(asGrace(), "prod", extension.ListQuery{
		GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespace: "shop"}); err != nil {
		t.Errorf("an ordinary list of pods was refused: %v", err)
	}
	// A kind of the same name in another group is not core Secrets.
	if isSecrets("example.com", "secrets") {
		t.Error("secrets.example.com was taken for core Secrets")
	}
}

// namedTunnels digs tunnels that say which pod they reached.
type namedTunnels struct{ pod string }

func (n namedTunnels) Open(context.Context, string, string, string, int) (metrics.Tunnel, error) {
	return namedTunnel{fakeTunnel: &fakeTunnel{addr: "127.0.0.1:40001", done: make(chan struct{})}, pod: n.pod}, nil
}

type namedTunnel struct {
	*fakeTunnel
	pod string
}

func (n namedTunnel) Pod() (string, bool) { return n.pod, true }

// Forward says which pod the tunnel reached, through the
// extensions' cap, and a tunneller that cannot say is reported as such - never
// as a pod with an empty name.
func TestForwardSaysWhichPodTheTunnelReached(t *testing.T) {
	ctx := context.Background()

	named := host{s: &Server{tunnels: countTunnels(namedTunnels{pod: "redis-master-0"}, maxExtensionTunnels)}}
	tunnel, err := named.Forward(ctx, "prod", "cache", "redis", 6379)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Stop()
	if !tunnel.PodKnown || tunnel.Pod != "redis-master-0" || tunnel.Addr != "127.0.0.1:40001" {
		t.Errorf("a tunnel onto redis-master-0 answered %+v", tunnel)
	}

	silent := host{s: &Server{tunnels: countTunnels(&fakeTunnels{}, maxExtensionTunnels)}}
	tunnel, err = silent.Forward(ctx, "prod", "cache", "redis", 6379)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Stop()
	if tunnel.PodKnown || tunnel.Pod != "" {
		t.Errorf("a tunnel that cannot name its pod answered %+v, want PodKnown false", tunnel)
	}

	empty := host{s: &Server{tunnels: countTunnels(namedTunnels{pod: ""}, maxExtensionTunnels)}}
	tunnel, err = empty.Forward(ctx, "prod", "cache", "redis", 6379)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Stop()
	if tunnel.PodKnown {
		t.Errorf("an empty pod name said to be known was reported as a pod: %+v", tunnel)
	}
}
