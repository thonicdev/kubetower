package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/health"
	"github.com/thonicdev/kubetower/internal/identity"
)

func signedIn(who identity.Who) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	return r.WithContext(identity.With(r.Context(), who))
}

var frank = identity.Who{Name: "frank@example.com", Method: identity.SSO, Groups: []string{"oidc:dev"}}

// Under the server profile `podExec` says only that this build can open a
// shell in a pod, and asks nobody: whether Shell is drawn on a pod is that
// pod's page's answer (shellFor). A single review of `create pods/exec` in
// every namespace would answer no to a person granted it in some namespaces
// only: alice bound to `edit` in `shop-staging` would get `podExec: false`
// while `ws/exec` there answers `decision=allowed`.
func TestTheServerProfileCapabilityAsksNothingAboutThePerson(t *testing.T) {
	s := serverFor(t, true)
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})

	if caps := (apiProfile{s}).Capabilities(signedIn(frank)); !caps.PodExec {
		t.Error("podExec is false on a build that serves the exec socket; Shell would be drawn on no pod")
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the capability asked the cluster %d questions; the pod's page asks", n)
	}
}

// A build with no terminal layer registers no exec socket, so a cluster that
// would allow the shell must not put the control on the page: it would open
// onto an address that answers 404. And it asks nobody, since no answer could
// make the control reachable.
func TestExecIsNotDrawnWhereTheChannelIsNotRegistered(t *testing.T) {
	s := serverFor(t, true)
	s.claude = nil
	if (apiChannels{s}).Offers(api.ChannelExec) {
		t.Fatal("precondition: a build with no terminal layer offers the exec channel")
	}
	reviews := &accesstest.Reviews{}
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})

	if caps := (apiProfile{s}).Capabilities(signedIn(frank)); caps.PodExec {
		t.Error("podExec is true on a build that serves no exec socket")
	}
	pod := api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	if offer := s.shellFor(signedIn(frank).Context(), pod); offer != nil {
		t.Errorf("a pod's page offers Shell %+v on a build that serves no exec socket", offer)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the review was asked %d times for a channel this build does not serve", n)
	}
}

// The desktop's capability is not asked: it covers every context of a
// kubeconfig at once, and one unreachable cluster would take the control off all
// of them. The exec itself is reviewed when it opens (internal/terminal).
func TestTheDesktopDoesNotAskBeforeDrawingExec(t *testing.T) {
	s := serverFor(t, false)
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	s.gate = reviews.Gate()

	if caps := (apiProfile{s}).Capabilities(nil); !caps.PodExec {
		t.Error("the desktop lost podExec")
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the desktop asked the cluster %d times for the capability", n)
	}
}

// The real review, against a server of this test's own: what reaches the API
// server for a person is a **SubjectAccessReview** naming them and their
// groups - never a self review, which would be the ServiceAccount's rights,
// and never an impersonation header - and for the shared password account a
// SelfSubjectAccessReview, the console's own rights. A person the API server
// allows is then asked about as the console too, as a self review.
//
// Through client-go and a kubeconfig rather than through the fake, because
// what the fake cannot show is the request's shape.
func TestTheReviewAsksTheAPIServerAboutThePerson(t *testing.T) {
	type seen struct {
		path        string
		impersonate string
		sar         authorizationv1.SubjectAccessReview
		self        authorizationv1.SelfSubjectAccessReview
	}
	var mu sync.Mutex
	var got []seen
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := seen{path: r.Method + " " + r.URL.Path, impersonate: r.Header.Get("Impersonate-User")}
		body, _ := io.ReadAll(r.Body)
		decoded, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
		var out any
		switch review := decoded.(type) {
		case *authorizationv1.SubjectAccessReview:
			entry.sar = *review
			review.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: false, Reason: "no RBAC policy matched"}
			if review.Spec.User == "grace@example.com" {
				review.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: true}
			}
			review.APIVersion, review.Kind = "authorization.k8s.io/v1", "SubjectAccessReview"
			out = review
		case *authorizationv1.SelfSubjectAccessReview:
			entry.self = *review
			review.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: true}
			review.APIVersion, review.Kind = "authorization.k8s.io/v1", "SelfSubjectAccessReview"
			out = review
		default:
			t.Errorf("an unexpected request reached the API server: %s (%v)", entry.path, err)
		}
		mu.Lock()
		got = append(got, entry)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(apiServer.Close)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: in-cluster
clusters:
- name: in-cluster
  cluster: {server: `+apiServer.URL+`}
contexts:
- name: in-cluster
  context: {cluster: in-cluster, user: sa}
users:
- name: sa
  user: {token: sa-token}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Clusters: manager, Terminal: shellsPresent{}, InCluster: true})
	if err != nil {
		t.Fatal(err)
	}

	// Two pods, so the console's held answer about the first is not what
	// grace's second question is answered from.
	api1 := api.ObjectRef{Cluster: "in-cluster", Kind: "pods", Namespace: "shop", Name: "api-1"}
	api2 := api.ObjectRef{Cluster: "in-cluster", Kind: "pods", Namespace: "shop", Name: "api-2"}
	if offer := s.shellFor(signedIn(frank).Context(), api1); offer != nil {
		t.Errorf("Shell is offered %+v over a review that answered allowed: false", offer)
	}
	admin := identity.Who{Name: "admin", Method: identity.Password}
	if offer := s.shellFor(signedIn(admin).Context(), api1); offer == nil || !offer.Permitted {
		t.Errorf("the shared account's self review answered allowed and Shell is %+v", offer)
	}
	grace := identity.Who{Name: "grace@example.com", Method: identity.SSO, Groups: []string{"oidc:dev"}}
	if offer := s.shellFor(signedIn(grace).Context(), api2); offer == nil || !offer.Permitted {
		t.Errorf("grace and the console may both exec and Shell is %+v", offer)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("the API server was asked %d times, want 4: %+v", len(got), got)
	}
	person, shared, graces, console := got[0], got[1], got[2], got[3]
	if person.path != "POST /apis/authorization.k8s.io/v1/subjectaccessreviews" {
		t.Errorf("the person's review went to %q", person.path)
	}
	for _, one := range got {
		if one.impersonate != "" {
			t.Errorf("a review carried an impersonation header: %s", one.path)
		}
	}
	spec := person.sar.Spec
	if spec.User != "frank@example.com" {
		t.Errorf("the review named %q", spec.User)
	}
	if want := []string{"oidc:dev", "system:authenticated"}; strings.Join(spec.Groups, ",") != strings.Join(want, ",") {
		t.Errorf("the review asserted groups %v, want %v", spec.Groups, want)
	}
	attrs := spec.ResourceAttributes
	if attrs == nil || attrs.Verb != "create" || attrs.Resource != "pods" || attrs.Subresource != "exec" ||
		attrs.Group != "" || attrs.Namespace != "shop" || attrs.Name != "api-1" {
		t.Errorf("the review asked about %+v, want create pods/exec on shop/api-1", attrs)
	}
	if shared.path != "POST /apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
		t.Errorf("the shared account's review went to %q, want a self review", shared.path)
	}
	if graces.path != "POST /apis/authorization.k8s.io/v1/subjectaccessreviews" || graces.sar.Spec.User != "grace@example.com" {
		t.Errorf("grace's review was %s naming %q", graces.path, graces.sar.Spec.User)
	}
	if console.path != "POST /apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
		t.Errorf("the console's own review went to %q, want a self review", console.path)
	}
	if a := console.self.Spec.ResourceAttributes; a == nil || a.Subresource != "exec" || a.Namespace != "shop" || a.Name != "api-2" {
		t.Errorf("the console's own review asked about %+v, want create pods/exec on shop/api-2", a)
	}
}

// Under the profile the page draws the `kubectl port-forward` command, and
// `portForward: false` is what tells it to - whether or not a forward set was
// handed in, because the three operations are not registered there.
func TestTheServerProfileSaysShowTheCommandForPortForward(t *testing.T) {
	s := serverFor(t, true)
	s.forwards = forwardsFixture{}
	if caps := (apiProfile{s}).Capabilities(nil); caps.PortForward {
		t.Error("portForward is true under the server profile, so the page offers a forward " +
			"whose 127.0.0.1 is the viewer's own machine")
	}

	desktop := serverFor(t, false)
	desktop.forwards = forwardsFixture{}
	if caps := (apiProfile{desktop}).Capabilities(nil); !caps.PortForward {
		t.Error("the desktop with a forward set says portForward false")
	}
}

type forwardsFixture struct{ Forwards }

// The AWS SSO row describes a person's laptop. A pod has no operator and no
// ~/.aws/sso, so under the profile the row and its notice are not in the
// response; on the desktop both are exactly as they were.
func TestTheServerProfileReportsNoAWSSSOSession(t *testing.T) {
	rows := healthFixtureRows()
	// Checked and absent is what a pod measured: `sso | absent | AWS SSO session`.
	// The zero value's notice is the third arm, and it goes too.
	absent := probeFixture{rows: rows, session: health.Session{State: health.SSOAbsent, Checked: true}}
	unknown := probeFixture{rows: rows}

	server := serverFor(t, true)
	for _, probe := range []probeFixture{absent, unknown} {
		report := healthReport(server, probe, rows, 0, len(rows), false)
		for _, check := range report.Checks {
			if check.Key == "sso" {
				t.Errorf("the server profile reports an SSO check: %+v", check)
			}
		}
		for _, notice := range report.Notices {
			if notice.Key == "sso" {
				t.Errorf("the server profile reports an SSO notice: %+v", notice)
			}
		}
	}

	desktop := healthReport(serverFor(t, false), absent, rows, 0, len(rows), false)
	if !hasCheck(desktop.Checks, "sso") {
		t.Error("the desktop lost its SSO row")
	}
	if notices := healthReport(serverFor(t, false), unknown, rows, 0, len(rows), false).Notices; !hasNotice(notices, "sso") {
		t.Error("the desktop lost the notice for an SSO cache it could not read")
	}
}

func hasCheck(checks []api.HealthCheck, key string) bool {
	for _, check := range checks {
		if check.Key == key {
			return true
		}
	}
	return false
}

func hasNotice(notices []api.HealthNotice, key string) bool {
	for _, notice := range notices {
		if notice.Key == key {
			return true
		}
	}
	return false
}
