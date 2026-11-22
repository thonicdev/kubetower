package certmanager

import (
	"sort"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The qualified kinds `Host.Definitions` resolves: bare plurals such as
// `certificates` or `orders` would be ambiguous as soon as a second group
// defines them.
const (
	CertificatesKind   = "certificates.cert-manager.io"
	IssuersKind        = "issuers.cert-manager.io"
	ClusterIssuersKind = "clusterissuers.cert-manager.io"
	OrdersKind         = "orders.acme.cert-manager.io"
	ChallengesKind     = "challenges.acme.cert-manager.io"
)

// The bands a certificate falls in, by days left. The thresholds are fixed:
// seven days is an emergency, thirty is cert-manager's own renewal point for
// a 90-day certificate.
const (
	BandExpired = "expired"
	BandWeek    = "week"  // 0 to 6 days left
	BandMonth   = "month" // 7 to 29 days left
	BandLater   = "later" // 30 days or more
	BandNone    = "none"  // never issued: no notAfter
)

const (
	weekDays  = 7
	monthDays = 30
	// StripDays is how far ahead the heat strip looks.
	StripDays = 90
	day       = 24 * time.Hour
)

// Certificate is one row of the page, as the wire carries it.
type Certificate struct {
	Namespace  string   `json:"namespace"`
	Name       string   `json:"name"`
	SecretName string   `json:"secretName"`
	DNSNames   []string `json:"dnsNames"`
	// MoreNames counts the DNS names past the ones carried.
	MoreNames   int    `json:"moreNames"`
	IssuerKind  string `json:"issuerKind"`
	IssuerName  string `json:"issuerName"`
	NotAfter    string `json:"notAfter"`
	RenewalTime string `json:"renewalTime"`
	// DaysLeft is null when the certificate has no notAfter: never issued,
	// which is not "expires today".
	DaysLeft *int   `json:"daysLeft"`
	Band     string `json:"band"`
	// Ready is the condition's status: True, False or Unknown (also when
	// absent).
	Ready   string `json:"ready"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// RenewalOverdue: renewalTime is past and the certificate was not
	// renewed - a renewal that is failing, seen before the expiry.
	RenewalOverdue bool `json:"renewalOverdue"`
	Attempts       int  `json:"failedAttempts"`
}

// Alert is whether the row belongs in one of the page's alarms.
func (c Certificate) Alert() bool {
	return c.Band == BandExpired || c.Band == BandWeek || c.Band == BandMonth || c.Ready != "True" || c.RenewalOverdue
}

// Issuer is an Issuer or a ClusterIssuer.
type Issuer struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Type is the issuer's mechanism: acme, ca, selfSigned, vault, venafi.
	Type    string `json:"type"`
	Ready   string `json:"ready"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Broken  bool   `json:"broken"`
}

// ACME is a failing Order or Challenge.
type ACME struct {
	Kind      string `json:"kind"` // Order or Challenge
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	// Type is the challenge's: HTTP-01 or DNS-01. Empty on an Order.
	Type string `json:"type"`
	// DNSName is the challenge's domain, or the order's first one.
	DNSName string `json:"dnsName"`
	// Certificate is the certificate the object was made for, when its
	// owners or annotations say.
	Certificate string `json:"certificate"`
	// order is a challenge's owning Order, through which its certificate is
	// found: cert-manager annotates the Order, not the Challenge.
	order string
}

// condition reads the condition of type kind: its status (Unknown when
// absent), reason and message.
func condition(o *unstructured.Unstructured, kind string) (status, reason, message string) {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || str(m["type"]) != kind {
			continue
		}
		status = str(m["status"])
		if status == "" {
			status = "Unknown"
		}
		return status, str(m["reason"]), str(m["message"])
	}
	return "Unknown", "", ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// maxNames is how many DNS names a row carries: a certificate for 100 names
// does not make a row of 100 names.
const maxNames = 3

// maxMessage bounds a condition's message on the wire.
const maxMessage = 600

func clip(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	cut := maxMessage
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// readCertificate classifies one Certificate at now.
func readCertificate(o *unstructured.Unstructured, now time.Time) Certificate {
	c := Certificate{Namespace: o.GetNamespace(), Name: o.GetName()}
	c.SecretName, _, _ = unstructured.NestedString(o.Object, "spec", "secretName")
	names, _, _ := unstructured.NestedStringSlice(o.Object, "spec", "dnsNames")
	if len(names) == 0 {
		if cn, _, _ := unstructured.NestedString(o.Object, "spec", "commonName"); cn != "" {
			names = []string{cn}
		}
	}
	if len(names) > maxNames {
		c.MoreNames = len(names) - maxNames
		names = names[:maxNames]
	}
	c.DNSNames = append([]string{}, names...)
	c.IssuerName, _, _ = unstructured.NestedString(o.Object, "spec", "issuerRef", "name")
	c.IssuerKind, _, _ = unstructured.NestedString(o.Object, "spec", "issuerRef", "kind")
	if c.IssuerKind == "" {
		c.IssuerKind = "Issuer"
	}
	c.Ready, c.Reason, c.Message = condition(o, "Ready")
	c.Message = clip(c.Message)
	if attempts, ok, _ := unstructured.NestedInt64(o.Object, "status", "failedIssuanceAttempts"); ok {
		c.Attempts = int(attempts)
	}
	c.Band = BandNone
	notAfter, _, _ := unstructured.NestedString(o.Object, "status", "notAfter")
	if t, err := time.Parse(time.RFC3339, notAfter); err == nil {
		c.NotAfter = t.UTC().Format(time.RFC3339)
		left := daysLeft(t, now)
		c.DaysLeft = &left
		c.Band = band(t, now)
	}
	renewal, _, _ := unstructured.NestedString(o.Object, "status", "renewalTime")
	if t, err := time.Parse(time.RFC3339, renewal); err == nil {
		c.RenewalTime = t.UTC().Format(time.RFC3339)
		// Overdue past a grace of an hour: cert-manager renews at
		// renewalTime, and a renewal in flight a few minutes late is not a
		// failing one.
		c.RenewalOverdue = c.Band != BandExpired && now.Sub(t) > time.Hour
	}
	return c
}

// daysLeft is whole days until t, rounded down; negative once it has passed.
func daysLeft(t, now time.Time) int {
	d := t.Sub(now)
	if d < 0 {
		return -int((-d + day - 1) / day)
	}
	return int(d / day)
}

func band(t, now time.Time) string {
	if !t.After(now) {
		return BandExpired
	}
	switch left := daysLeft(t, now); {
	case left < weekDays:
		return BandWeek
	case left < monthDays:
		return BandMonth
	}
	return BandLater
}

// issuerTypes are the mechanisms an issuer's spec names, in cert-manager's
// own spelling.
var issuerTypes = []string{"acme", "ca", "selfSigned", "vault", "venafi"}

func readIssuer(o *unstructured.Unstructured, kind string) Issuer {
	i := Issuer{Kind: kind, Namespace: o.GetNamespace(), Name: o.GetName()}
	spec, _, _ := unstructured.NestedMap(o.Object, "spec")
	for _, t := range issuerTypes {
		if _, ok := spec[t]; ok {
			i.Type = t
			break
		}
	}
	if i.Type == "" {
		// An external issuer is a different CRD; one here naming something
		// else is still an issuer, of a type this page does not know.
		for k := range spec {
			i.Type = k
			break
		}
	}
	i.Ready, i.Reason, i.Message = condition(o, "Ready")
	i.Message = clip(i.Message)
	i.Broken = i.Ready != "True"
	return i
}

// The states cert-manager gives an ACME object that has failed for good.
var failedStates = map[string]bool{"invalid": true, "errored": true, "expired": true}

// readOrder answers the order and whether it is failing: an errored or
// invalid order, which may have made no challenge at all (a domain refused
// by the ACME server's policy).
func readOrder(o *unstructured.Unstructured) (ACME, bool) {
	a := ACME{Kind: "Order", Namespace: o.GetNamespace(), Name: o.GetName()}
	a.State, _, _ = unstructured.NestedString(o.Object, "status", "state")
	a.Reason, _, _ = unstructured.NestedString(o.Object, "status", "reason")
	a.Reason = clip(a.Reason)
	if names, _, _ := unstructured.NestedStringSlice(o.Object, "spec", "dnsNames"); len(names) > 0 {
		a.DNSName = names[0]
	}
	a.Certificate = o.GetAnnotations()["cert-manager.io/certificate-name"]
	return a, failedStates[a.State]
}

// readChallenge answers the challenge and whether it is failing: a failed
// state, or any reason at all. A challenge whose self-check keeps failing
// stays `pending` for ever, its reason the only sign.
func readChallenge(o *unstructured.Unstructured) (ACME, bool) {
	a := ACME{Kind: "Challenge", Namespace: o.GetNamespace(), Name: o.GetName()}
	a.State, _, _ = unstructured.NestedString(o.Object, "status", "state")
	a.Reason, _, _ = unstructured.NestedString(o.Object, "status", "reason")
	a.Reason = clip(a.Reason)
	a.Type, _, _ = unstructured.NestedString(o.Object, "spec", "type")
	a.DNSName, _, _ = unstructured.NestedString(o.Object, "spec", "dnsName")
	a.Certificate = o.GetAnnotations()["cert-manager.io/certificate-name"]
	for _, owner := range o.GetOwnerReferences() {
		if owner.Kind == "Order" {
			a.order = owner.Name
		}
	}
	if a.State == "valid" {
		return a, false
	}
	return a, failedStates[a.State] || a.Reason != ""
}

// sortCertificates puts the alarms first, soonest expiry first, the never
// issued after the dated; then the rest by expiry. Ties by namespace and name
// so two answers to the same cluster draw the same order.
func sortCertificates(cs []Certificate) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Alert() != b.Alert() {
			return a.Alert()
		}
		if (a.DaysLeft == nil) != (b.DaysLeft == nil) {
			return a.DaysLeft != nil
		}
		if a.NotAfter != b.NotAfter {
			return a.NotAfter < b.NotAfter
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}
