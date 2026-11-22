package certmanager

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

func TestBandsAndDaysLeft(t *testing.T) {
	for _, tc := range []struct {
		in       time.Duration
		band     string
		daysLeft int
	}{
		{-49 * time.Hour, BandExpired, -3},
		{-time.Minute, BandExpired, -1},
		{time.Hour, BandWeek, 0},
		{6*day + 23*time.Hour, BandWeek, 6},
		{7 * day, BandMonth, 7},
		{29*day + time.Hour, BandMonth, 29},
		{30 * day, BandLater, 30},
		{200 * day, BandLater, 200},
	} {
		o := certificate(t, "shop", "web", testNow.Add(tc.in), "True")
		c := readCertificate(&o, testNow)
		if c.Band != tc.band || c.DaysLeft == nil || *c.DaysLeft != tc.daysLeft {
			t.Errorf("%v: band %q days %v, want %q %d", tc.in, c.Band, c.DaysLeft, tc.band, tc.daysLeft)
		}
	}
}

// A certificate never issued has no date: it is not "expires today".
func TestNeverIssuedHasNoDate(t *testing.T) {
	o := certificate(t, "shop", "web", time.Time{}, "False")
	c := readCertificate(&o, testNow)
	if c.DaysLeft != nil || c.Band != BandNone || c.Ready != "False" || !c.Alert() {
		t.Fatalf("got %+v", c)
	}
}

func TestRenewalOverdue(t *testing.T) {
	// Expires in 20 days, renewal was due 10 days ago: failing to renew.
	o := certificate(t, "shop", "web", testNow.Add(20*day), "True")
	if c := readCertificate(&o, testNow); !c.RenewalOverdue {
		t.Fatal("a renewal ten days late is overdue")
	}
	// Expires in 60 days, renewal in 30: not yet.
	o = certificate(t, "shop", "web", testNow.Add(60*day), "True")
	if c := readCertificate(&o, testNow); c.RenewalOverdue {
		t.Fatal("a renewal still ahead is not overdue")
	}
}

func TestACMEFailing(t *testing.T) {
	for _, tc := range []struct {
		state, reason string
		failing       bool
	}{
		{"pending", "Waiting for HTTP-01 challenge propagation: failed to perform self check", true},
		{"pending", "", false},
		{"invalid", "", true},
		{"errored", "", true},
		{"valid", "", false},
	} {
		o := challenge(t, "shop", "web", tc.state, tc.reason)
		if _, failing := readChallenge(&o); failing != tc.failing {
			t.Errorf("challenge %s %q: failing %v", tc.state, tc.reason, failing)
		}
	}
	o := order(t, "shop", "web", "errored", "rejectedIdentifier")
	if _, failing := readOrder(&o); !failing {
		t.Error("an errored order is failing, even with no challenge")
	}
	o = order(t, "shop", "web", "pending", "")
	if _, failing := readOrder(&o); failing {
		t.Error("a pending order is in flight")
	}
}

func TestOverviewCountsAndOrder(t *testing.T) {
	at(t)
	h := newHost()
	h.add("certificates",
		certificate(t, "shop", "later", testNow.Add(80*day), "True"),
		certificate(t, "shop", "week", testNow.Add(3*day), "True"),
		certificate(t, "shop", "month", testNow.Add(15*day), "True"),
		certificate(t, "shop", "expired", testNow.Add(-2*day), "False"),
		certificate(t, "pay", "never", time.Time{}, "False"),
		certificate(t, "pay", "far", testNow.Add(300*day), "True"),
	)
	h.add("issuers", issuer(t, "Issuer", "shop", "ca", "False", "Error getting keypair"))
	h.add("clusterissuers", issuer(t, "ClusterIssuer", "", "letsencrypt", "True", "registered"))
	h.add("challenges", challenge(t, "shop", "stuck", "pending", "self check failed"), challenge(t, "shop", "fine", "pending", ""))
	h.add("orders", order(t, "pay", "refused", "errored", "forbidden by policy"))

	rec := serve(t, h, "/x/prod/certmanager/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	o := decode[Overview](t, rec)
	want := Counts{Total: 6, Expired: 1, Week: 1, Month: 2, NotReady: 2, RenewalOverdue: 2, ACMEFailing: 2, IssuersBroken: 1}
	if o.Counts != want {
		t.Errorf("counts %+v, want %+v", o.Counts, want)
	}
	if o.Ring != (Ring{Ready: 4, NotReady: 2}) {
		t.Errorf("ring %+v", o.Ring)
	}
	if len(o.Strip) != StripDays || o.Strip[3] != 1 || o.Strip[15] != 1 || o.Strip[80] != 1 {
		t.Errorf("strip %v", o.Strip)
	}
	var names []string
	for _, c := range o.Certificates {
		names = append(names, c.Name)
	}
	// Alarms first by date, the never issued after them; then the rest.
	if got := fmt.Sprint(names); got != "[expired week month never later far]" {
		t.Errorf("order %s", got)
	}
	if !o.Installed || !o.Complete || o.Shown != 6 {
		t.Errorf("installed %v complete %v shown %d", o.Installed, o.Complete, o.Shown)
	}
	if o.Issuers.State != StateRead || o.Issuers.Total != 2 || !o.Issuers.Items[0].Broken {
		t.Errorf("issuers %+v", o.Issuers)
	}
	if o.ACME.State != StateRead || o.ACME.InFlight != 3 || len(o.ACME.Items) != 2 {
		t.Errorf("acme %+v", o.ACME)
	}
}

func TestNamespaceScope(t *testing.T) {
	at(t)
	h := newHost()
	h.add("certificates", certificate(t, "shop", "a", testNow.Add(3*day), "True"), certificate(t, "pay", "b", testNow.Add(3*day), "True"))
	h.add("clusterissuers", issuer(t, "ClusterIssuer", "", "le", "True", ""))
	o := decode[Overview](t, serve(t, h, "/x/prod/certmanager/?ns=shop"))
	if o.Counts.Total != 1 || o.Namespace != "shop" {
		t.Fatalf("got %+v", o.Counts)
	}
	// A ClusterIssuer serves every namespace: it is read whatever the scope.
	if o.Issuers.Total != 1 {
		t.Errorf("cluster issuers in a namespace scope: %+v", o.Issuers)
	}
	for _, q := range h.listed {
		if q.GVR.Resource == "clusterissuers" && q.Namespace != "" {
			t.Errorf("a cluster-scoped kind was asked in a namespace: %+v", q)
		}
		if q.Limit != listLimit {
			t.Errorf("a list without its limit: %+v", q)
		}
	}
	if rec := serve(t, h, "/x/prod/certmanager/?ns=Not_A_Namespace"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad namespace answered %d", rec.Code)
	}
}

// Not installed, refused and empty are three answers.
func TestNotInstalledRefusedEmpty(t *testing.T) {
	at(t)

	h := newHost()
	h.registry = fakeRegistry{served: map[string]extension.CRD{}}
	rec := serve(t, h, "/x/prod/certmanager/")
	o := decode[Overview](t, rec)
	if rec.Code != http.StatusOK || o.Installed || o.Message == "" {
		t.Fatalf("not installed: %d %+v", rec.Code, o)
	}

	h = newHost()
	h.registry.lookErr = map[string]error{CertificatesKind: extension.ErrNotServed}
	if o := decode[Overview](t, serve(t, h, "/x/prod/certmanager/")); o.Installed {
		t.Fatal("a kind served by no version is not installed")
	}

	h = newHost()
	h.listErr["certificates"] = fmt.Errorf("list: %w", extension.ErrDenied)
	rec = serve(t, h, "/x/prod/certmanager/")
	if rec.Code != http.StatusForbidden || decode[problem](t, rec).Code != CodeListForbidden {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}

	h = newHost()
	h.listErr["certificates"] = fmt.Errorf("list: %w", extension.ErrUnreviewable)
	if rec := serve(t, h, "/x/prod/certmanager/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreviewable: %d", rec.Code)
	}

	h = newHost()
	o = decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if !o.Installed || o.Counts.Total != 0 || o.Certificates == nil || len(o.Strip) != StripDays {
		t.Fatalf("empty: %+v", o)
	}
}

func TestFailures(t *testing.T) {
	at(t)
	h := newHost()
	h.defErr = fmt.Errorf("context: %w", extension.ErrUnknownCluster)
	if rec := serve(t, h, "/x/gone/certmanager/"); rec.Code != http.StatusNotFound || decode[problem](t, rec).Code != CodeUnknownCluster {
		t.Fatalf("unknown cluster: %d %s", rec.Code, rec.Body)
	}
	h = newHost()
	h.defErr = errors.New("discovery timed out")
	if rec := serve(t, h, "/x/prod/certmanager/"); rec.Code != http.StatusBadGateway || decode[problem](t, rec).Code != CodeCatalogueUnreadable {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body)
	}
	h = newHost()
	h.listErr["certificates"] = extension.NotGranted(apierrors.NewForbidden(
		schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "", errors.New("no")))
	if rec := serve(t, h, "/x/prod/certmanager/"); rec.Code != http.StatusServiceUnavailable || decode[problem](t, rec).Code != extension.CodeConsoleNotGranted {
		t.Fatalf("console not granted: %d %s", rec.Code, rec.Body)
	}
	h = newHost()
	h.listErr["certificates"] = errors.New("connection refused")
	if rec := serve(t, h, "/x/prod/certmanager/"); rec.Code != http.StatusBadGateway || decode[problem](t, rec).Code != CodeListFailed {
		t.Fatalf("list failed: %d %s", rec.Code, rec.Body)
	}
}

// A section refused alone says so, and the certificates stay drawn.
func TestSectionRefusedAlone(t *testing.T) {
	at(t)
	h := newHost()
	h.add("certificates", certificate(t, "shop", "a", testNow.Add(3*day), "True"))
	h.listErr["clusterissuers"] = fmt.Errorf("list: %w", extension.ErrDenied)
	h.listErr["challenges"] = errors.New("timeout")
	o := decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if o.Counts.Total != 1 {
		t.Fatalf("certificates not drawn: %+v", o.Counts)
	}
	// The Issuers of the person's namespaces were read, so the section reads
	// and says what it left out.
	if o.Issuers.State != StateRead || o.Complete || !hasNote(o.Notes, "ClusterIssuers") {
		t.Errorf("issuers %+v complete %v notes %v", o.Issuers, o.Complete, o.Notes)
	}
	if o.ACME.State != StateFailed || o.ACME.Message == "" {
		t.Errorf("acme %+v", o.ACME)
	}
	// Without ACME kinds served, the section says absent, not empty.
	h = newHost()
	served := map[string]extension.CRD{}
	for k, v := range crds {
		if k != OrdersKind && k != ChallengesKind {
			served[k] = v
		}
	}
	h.registry = fakeRegistry{served: served}
	o = decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if o.ACME.State != StateAbsent {
		t.Errorf("acme without its kinds: %+v", o.ACME)
	}
}

// A read cut at the limit says so.
func TestCutReadIsIncomplete(t *testing.T) {
	at(t)
	h := newHost()
	for i := 0; i < listLimit+3; i++ {
		h.add("certificates", certificate(t, "shop", fmt.Sprintf("c%05d", i), testNow.Add(time.Duration(i%400)*day), "True"))
	}
	o := decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if o.Complete || len(o.Notes) == 0 || o.Counts.Total != listLimit || o.Shown != maxRows {
		t.Fatalf("complete %v notes %v total %d shown %d", o.Complete, o.Notes, o.Counts.Total, o.Shown)
	}
}

func TestSuggest(t *testing.T) {
	h := newHost()
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	got, err := New().Suggest(r, h, "prod")
	if err != nil || got != "cert-manager is installed" {
		t.Fatalf("%q %v", got, err)
	}
	h.add("certificates", certificate(t, "shop", "a", testNow, "True"), certificate(t, "shop", "b", testNow, "True"))
	if got, _ := New().Suggest(r, h, "prod"); got != "cert-manager is installed, with 2 certificates" {
		t.Fatalf("%q", got)
	}
	h.registry = fakeRegistry{served: map[string]extension.CRD{}}
	if got, err := New().Suggest(r, h, "prod"); got != "" || err != nil {
		t.Fatalf("not installed suggests %q %v", got, err)
	}
	h = newHost()
	h.listErr["certificates"] = extension.ErrDenied
	if _, err := New().Suggest(r, h, "prod"); err == nil {
		t.Fatal("a refused read is not a suggestion")
	}
}

func TestComponentNamesWhatSuggestLooksFor(t *testing.T) {
	var s extension.Suggester = New()
	named, ok := s.(extension.Component)
	if !ok || named.Component() != "cert-manager" {
		t.Fatalf("component = %v, %v", named, ok)
	}
}

func TestClip(t *testing.T) {
	long := ""
	for len(long) < 2*maxMessage {
		long += "é"
	}
	got := clip(long)
	if len(got) > maxMessage+len("…") || got[len(got)-len("…"):] != "…" {
		t.Fatalf("clip: %d bytes", len(got))
	}
}

// cert-manager annotates the Order with its certificate, not the Challenge:
// a failing challenge names its certificate through the order that owns it.
func TestChallengeNamesItsCertificateThroughItsOrder(t *testing.T) {
	at(t)
	h := newHost()
	o := order(t, "shop", "web-1-42", "pending", "")
	o.SetAnnotations(map[string]string{"cert-manager.io/certificate-name": "web"})
	ch := challenge(t, "shop", "web-1-42-0", "pending", "self check failed")
	ch.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "acme.cert-manager.io/v1", Kind: "Order", Name: "web-1-42", UID: "u"}})
	h.add("orders", o)
	h.add("challenges", ch)
	got := decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if len(got.ACME.Items) != 1 || got.ACME.Items[0].Certificate != "web" {
		t.Fatalf("acme %+v", got.ACME.Items)
	}
}
