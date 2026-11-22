package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// Host.ConfiguredSecret and Host.ConfiguredService: what the console's
// configuration names, read with the console's own credentials, never a name
// the extension made up, recorded, the value nowhere but the return; and
// Host.Client, which answers discovery and nothing else. Against
// surfaceServer, whose Secret shop/db holds `password: hunter2`, and the
// fixtures, whose Configured names shop/db and the Service shop/api.

// consoleOnly answers the console's own reviews yes and every person's no:
// what a door reviewed for the person cannot read and a door read as the
// console can.
func consoleOnly(q accesstest.Question) (bool, error) { return q.Self, nil }

func configuredServer(t *testing.T, answer func(accesstest.Question) (bool, error), inCluster bool,
	sink *revealSink) (*Server, *surfaceServer, *accesstest.Reviews) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: answer}
	opts := access.Options{Reviews: reviews.For, InCluster: inCluster}
	if sink != nil {
		opts.Audit = audit.New(sink)
	}
	s, err := New(Options{Clusters: manager, InCluster: inCluster, Access: access.New(opts)})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake, reviews
}

func askedFor(fake *surfaceServer, part string) []string {
	var out []string
	for _, p := range fake.asked() {
		if strings.Contains(p, part) {
			out = append(out, p)
		}
	}
	return out
}

func TestAConfiguredSecretIsReadAsTheConsoleRecordedAndNeverSaid(t *testing.T) {
	logged := processLog(t)
	sink := &revealSink{}
	s, fake, reviews := configuredServer(t, consoleOnly, true, sink)

	// The person may not get the Secret (Reveal refuses them) ...
	if _, err := s.hostFor(fixtureKey).Reveal(asGrace(), "prod", "shop", "db", "password"); !errors.Is(err, extension.ErrRevealDenied) {
		t.Fatalf("Reveal answered %v; the person was meant to be refused", err)
	}
	// ... and the console reads it for them, through the configured door.
	value, err := s.hostFor(fixtureKey).ConfiguredSecret(asGrace(), "prod", "shop", "db", "password")
	if err != nil || value != "hunter2" {
		t.Fatalf("ConfiguredSecret answered %q, %v", value, err)
	}
	var self bool
	for _, q := range reviews.Asked() {
		if q.Attributes.Resource == "secrets" && q.Self {
			self = true
		}
	}
	if !self {
		t.Errorf("reviews %+v: the configured read was not reviewed as the console's own", reviews.Asked())
	}

	var configured []audit.Record
	for _, r := range sink.records(t) {
		if r.Via == audit.Console {
			configured = append(configured, r)
		}
	}
	if len(configured) != 2 || configured[0].Phase != audit.Attempt || configured[1].Phase != audit.Outcome {
		t.Fatalf("the configured read wrote %+v, want an attempt and its outcome as the console's", configured)
	}
	for _, r := range configured {
		if r.Act != audit.Reveal || r.Namespace != "shop" || r.Name != "db" || r.Key != "password" ||
			r.Extension != fixtureKey || r.OnBehalfOf != "grace@example.com" || r.Who != "" {
			t.Errorf("the %s record reads %+v", r.Phase, r)
		}
	}
	if configured[1].Outcome != audit.Applied {
		t.Errorf("the outcome record says %q, want %q", configured[1].Outcome, audit.Applied)
	}
	if !strings.Contains(logged.String(), "extension="+fixtureKey) || !strings.Contains(logged.String(), "via=console") {
		t.Errorf("the process log does not name the extension and the console:\n%s", logged.String())
	}
	noValue(t, "the audit log or the process log", sink.buf.String(), logged.String())
	if got := askedFor(fake, "/secrets/db"); len(got) != 1 {
		t.Errorf("the Secret was asked for %d times (%v), want once: by the configured door", len(got), got)
	}
}

func TestAConfiguredSecretRefusesWhatTheConfigurationDoesNotName(t *testing.T) {
	sink := &revealSink{}
	s, fake, reviews := configuredServer(t, func(accesstest.Question) (bool, error) { return true, nil }, true, sink)
	h := s.hostFor(fixtureKey)
	for _, c := range []struct{ namespace, name string }{
		{"shop", "other"},   // another Secret beside it
		{"other", "db"},     // the same name elsewhere
		{"shop", "db "},     // a name that only looks like it
		{"Shop", "db"},      // case is a name too
		{"shop", "../db"},   // never a path
		{"kube-system", ""}, // nothing at all
	} {
		_, err := h.ConfiguredSecret(asGrace(), "prod", c.namespace, c.name, "password")
		if err == nil || (!errors.Is(err, extension.ErrNotConfigured) && c.name != "") {
			t.Errorf("ConfiguredSecret(%q, %q) answered %v, want ErrNotConfigured", c.namespace, c.name, err)
		}
		_, err = h.ConfiguredService(asGrace(), "prod", c.namespace, c.name)
		if err == nil || (!errors.Is(err, extension.ErrNotConfigured) && c.name != "") {
			t.Errorf("ConfiguredService(%q, %q) answered %v, want ErrNotConfigured", c.namespace, c.name, err)
		}
	}
	// The door with no extension behind it names nothing.
	if _, err := s.Host().ConfiguredSecret(asGrace(), "prod", "shop", "db", "password"); err == nil {
		t.Error("the console's own door read a configured Secret for no extension")
	}
	if got := askedFor(fake, "/secrets"); len(got) != 0 {
		t.Errorf("the cluster was asked %v for a Secret nothing configured", got)
	}
	if got := askedFor(fake, "/services"); len(got) != 0 {
		t.Errorf("the cluster was asked %v for a Service nothing configured", got)
	}
	if len(reviews.Asked()) != 0 {
		t.Errorf("reviews were asked %+v for names nothing configured", reviews.Asked())
	}
	if records := sink.records(t); len(records) != 0 {
		t.Errorf("a refused name wrote %+v; nothing was attempted", records)
	}
}

func TestAConfiguredSecretTheConsoleMayNotReadIsRefusedAndRecorded(t *testing.T) {
	sink := &revealSink{}
	s, fake, _ := configuredServer(t, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets", nil
	}, true, sink)
	_, err := s.hostFor(fixtureKey).ConfiguredSecret(asGrace(), "prod", "shop", "db", "password")
	if !errors.Is(err, extension.ErrRevealDenied) {
		t.Fatalf("err %v, want ErrRevealDenied", err)
	}
	records := sink.records(t)
	if len(records) != 2 || records[1].Outcome != audit.Refused || records[0].Decision != string(access.Denied) {
		t.Errorf("a refused configured read wrote %+v, want an attempt and a refused outcome", records)
	}
	if got := askedFor(fake, "/secrets"); len(got) != 0 {
		t.Errorf("the cluster was asked %v after the console's own review said no", got)
	}
}

func TestAConfiguredReadForNobodyIsRefusedUnderTheServerProfile(t *testing.T) {
	s, fake, _ := configuredServer(t, nil, true, &revealSink{})
	nobody := httptest.NewRequest(http.MethodGet, "/x/prod/alfa/", nil)
	if _, err := s.hostFor(fixtureKey).ConfiguredSecret(nobody, "prod", "shop", "db", "password"); !errors.Is(err, extension.ErrRevealUnreviewed) {
		t.Errorf("a configured Secret for nobody answered %v, want ErrRevealUnreviewed", err)
	}
	if _, err := s.hostFor(fixtureKey).ConfiguredService(nobody, "prod", "shop", "api"); !errors.Is(err, extension.ErrUnreviewable) {
		t.Errorf("a configured Service for nobody answered %v, want ErrUnreviewable", err)
	}
	if got := append(askedFor(fake, "/secrets"), askedFor(fake, "/services")...); len(got) != 0 {
		t.Errorf("the cluster was asked %v on nobody's behalf", got)
	}
}

func TestAConfiguredSecretSaysWhatIsAbsentWithoutTheValue(t *testing.T) {
	logged := processLog(t)
	sink := &revealSink{}
	s, _, _ := configuredServer(t, consoleOnly, true, sink)
	_, err := s.hostFor(fixtureKey).ConfiguredSecret(asGrace(), "prod", "shop", "db", "token")
	if !errors.Is(err, extension.ErrNoSuchKey) {
		t.Fatalf("err %v, want ErrNoSuchKey", err)
	}
	noValue(t, "an error, the audit log or the process log", err.Error(), sink.buf.String(), logged.String())
}

func TestAConfiguredServiceIsReadAsTheConsole(t *testing.T) {
	s, _, reviews := configuredServer(t, consoleOnly, true, nil)
	svc, err := s.hostFor(fixtureKey).ConfiguredService(asGrace(), "prod", "shop", "api")
	if err != nil || svc.Name != "api" || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 443 {
		t.Fatalf("ConfiguredService answered %+v, %v", svc, err)
	}
	for _, q := range reviews.Asked() {
		if q.Attributes.Resource == "services" && !q.Self {
			t.Errorf("the configured Service was reviewed for the person: %+v", q)
		}
	}
	// What the configuration names and the cluster lacks is its own answer.
	refused, _, _ := configuredServer(t, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "services", nil
	}, true, nil)
	if _, err := refused.hostFor(fixtureKey).ConfiguredService(asGrace(), "prod", "shop", "api"); !errors.Is(err, extension.ErrDenied) {
		t.Errorf("a Service the console may not read answered %v, want ErrDenied", err)
	}
}

// On the desktop nothing changes: an unreviewable console read goes through,
// as every other read there does.
func TestOnTheDesktopAnUnreviewableConfiguredReadGoesThrough(t *testing.T) {
	s, _, _ := configuredServer(t, func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorizer did not answer")
	}, false, nil)
	value, err := s.hostFor(fixtureKey).ConfiguredSecret(asGrace(), "prod", "shop", "db", "password")
	if err != nil || value != "hunter2" {
		t.Errorf("ConfiguredSecret on the desktop answered %q, %v", value, err)
	}
	if _, err := s.hostFor(fixtureKey).ConfiguredService(asGrace(), "prod", "shop", "api"); err != nil {
		t.Errorf("ConfiguredService on the desktop answered %v", err)
	}
}

// Host.Client answers discovery, and an object read or a change through either
// client is refused before it leaves the console, on both profiles.
func TestHostClientAnswersDiscoveryAndNothingElse(t *testing.T) {
	for _, inCluster := range []bool{false, true} {
		t.Run(fmt.Sprintf("incluster=%t", inCluster), func(t *testing.T) {
			s, fake, _ := configuredServer(t, nil, inCluster, nil)
			clients, err := s.hostFor(fixtureKey).Client(asGrace(), "prod")
			if err != nil {
				t.Fatal(err)
			}
			ctx := grace()
			list, err := clients.Typed.Discovery().ServerResourcesForGroupVersion("v1")
			if err != nil || len(list.APIResources) == 0 {
				t.Fatalf("discovery answered %v, %v", list, err)
			}
			pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			for name, read := range map[string]func() error{
				"typed get": func() error {
					_, err := clients.Typed.CoreV1().Pods("shop").Get(ctx, "api-1", metav1.GetOptions{})
					return err
				},
				"typed list": func() error { _, err := clients.Typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{}); return err },
				"secret get": func() error {
					_, err := clients.Typed.CoreV1().Secrets("shop").Get(ctx, "db", metav1.GetOptions{})
					return err
				},
				"dynamic list": func() error { _, err := clients.Dynamic.Resource(pods).List(ctx, metav1.ListOptions{}); return err },
				"watch": func() error {
					_, err := clients.Typed.CoreV1().Pods("shop").Watch(ctx, metav1.ListOptions{})
					return err
				},
				"raw get": func() error {
					return clients.Typed.CoreV1().RESTClient().Get().AbsPath("/api/v1/namespaces/shop/pods").Do(ctx).Error()
				},
				"delete": func() error { return clients.Typed.CoreV1().Pods("shop").Delete(ctx, "api-1", metav1.DeleteOptions{}) },
			} {
				err := read()
				if !errors.Is(err, access.ErrNotDiscovery) {
					t.Errorf("%s through Host.Client answered %v, want a refusal by the console", name, err)
				}
				var status interface{ Status() metav1.Status }
				if errors.As(err, &status) && status.Status().Code == http.StatusForbidden {
					t.Errorf("%s was refused as a 403: %v", name, err)
				}
			}
			for _, p := range fake.asked() {
				if strings.Contains(p, "/pods") || strings.Contains(p, "/secrets") {
					t.Errorf("the cluster was asked %q through Host.Client", p)
				}
			}
		})
	}
}

// The console's own lists are untouched by the narrowing of what an extension
// is handed: the manager's clients still read.
func TestTheConsolesOwnClientsStillRead(t *testing.T) {
	s, _, _ := configuredServer(t, nil, false, nil)
	client, err := s.clusters.Client("prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Clients.CoreV1().Pods("shop").Get(context.Background(), "api-1", metav1.GetOptions{}); err != nil {
		t.Errorf("the console's own client could not read: %v", err)
	}
}
