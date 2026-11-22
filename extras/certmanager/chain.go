package certmanager

import (
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// CertificateRequestsKind is the request cert-manager makes for each issuance.
const CertificateRequestsKind = "certificaterequests.cert-manager.io"

// The state of one link of the chain. Four of them are answers about the
// cluster and stay four: an object that is there, one that is not there, one
// the person may not read, one whose read failed. A kind the cluster does not
// serve is its own answer, and so is a read cut at its limit.
const (
	LinkFound    = "found"    // read: the object is there
	LinkNone     = "none"     // the kind was read to its end and no such object exists
	LinkRefused  = "refused"  // the person may not read it (or it could not be asked)
	LinkFailed   = "failed"   // the read failed
	LinkUnserved = "unserved" // the cluster serves no such kind
	LinkCut      = "cut"      // the read stopped at its limit before the object was seen
)

// The tones a link is drawn in, the console's own.
const (
	ToneOK      = "ok"
	ToneWarn    = "warn"
	ToneBad     = "bad"
	ToneNeutral = "neutral"
)

const (
	// chainLimit bounds each of the three reads (requests, orders,
	// challenges) of one namespace.
	chainLimit = 500
	// maxChallenges bounds the challenges drawn under one order.
	maxChallenges = 20
)

// Cond is one condition of a request, as cert-manager wrote it.
type Cond struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Link is one step of the issuance chain.
type Link struct {
	Kind      string `json:"kind"` // CertificateRequest, Order, Challenge, Issuer, ClusterIssuer
	State     string `json:"state"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Tone      string `json:"tone"`
	// Phase is the one word the step is in: approved, denied, issued, failed,
	// pending, valid, errored ... in cert-manager's own spelling where it has one.
	Phase   string `json:"phase,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Type is a challenge's (HTTP-01, DNS-01) or an issuer's mechanism.
	Type    string `json:"type,omitempty"`
	DNSName string `json:"dnsName,omitempty"`
	Created string `json:"created,omitempty"`
	// Changed is when the step last changed condition, when it says.
	Changed    string `json:"changed,omitempty"`
	Conditions []Cond `json:"conditions,omitempty"`
}

// Chain is the issuance of one certificate: its latest request, the order
// that request led to, the order's challenges, and the issuer asked.
type Chain struct {
	Request    Link   `json:"request"`
	Order      Link   `json:"order"`
	Challenges []Link `json:"challenges"`
	// MoreChallenges counts the challenges past the ones carried.
	MoreChallenges int  `json:"moreChallenges"`
	Issuer         Link `json:"issuer"`
	// Secret is the name of the Secret the certificate is stored in. It is
	// named, never opened.
	Secret string `json:"secret"`
	// Stuck is the kind of the first link that is not ok, in chain order;
	// empty when every link read is ok.
	Stuck string `json:"stuck,omitempty"`
}

// ownerOf is the name of an owner of kind kind, if any.
func ownerOf(o *unstructured.Unstructured, kind string) string {
	for _, owner := range o.GetOwnerReferences() {
		if owner.Kind == kind {
			return owner.Name
		}
	}
	return ""
}

func stamp(o *unstructured.Unstructured) string {
	t := o.GetCreationTimestamp()
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// changed is the latest transition of any condition of o.
func changed(o *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	var latest time.Time
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, err := time.Parse(time.RFC3339, str(m["lastTransitionTime"])); err == nil && t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339)
}

// readRequest classifies a CertificateRequest: denied and failed are bad, a
// request still waiting for approval or for its issuer is pending.
func readRequest(o *unstructured.Unstructured) Link {
	l := Link{Kind: "CertificateRequest", State: LinkFound, Namespace: o.GetNamespace(), Name: o.GetName(),
		Created: stamp(o), Changed: changed(o)}
	for _, kind := range []string{"Approved", "Denied", "Ready", "InvalidRequest"} {
		if !hasCondition(o, kind) {
			continue
		}
		status, reason, message := condition(o, kind)
		l.Conditions = append(l.Conditions, Cond{Type: kind, Status: status, Reason: reason, Message: clip(message)})
	}
	approved, _, _ := condition(o, "Approved")
	denied, deniedReason, deniedMsg := condition(o, "Denied")
	ready, readyReason, readyMsg := condition(o, "Ready")
	invalid, invalidReason, invalidMsg := condition(o, "InvalidRequest")
	switch {
	case denied == "True":
		l.Phase, l.Tone, l.Reason, l.Message = "denied", ToneBad, deniedReason, clip(deniedMsg)
	case invalid == "True":
		l.Phase, l.Tone, l.Reason, l.Message = "invalid", ToneBad, invalidReason, clip(invalidMsg)
	case ready == "True":
		l.Phase, l.Tone, l.Reason, l.Message = "issued", ToneOK, readyReason, clip(readyMsg)
	case ready == "False" && readyReason == "Failed":
		l.Phase, l.Tone, l.Reason, l.Message = "failed", ToneBad, readyReason, clip(readyMsg)
	case approved == "True":
		l.Phase, l.Tone, l.Reason, l.Message = "approved", ToneWarn, readyReason, clip(readyMsg)
	default:
		l.Phase, l.Tone, l.Reason, l.Message = "pending", ToneWarn, readyReason, clip(readyMsg)
	}
	return l
}

func hasCondition(o *unstructured.Unstructured, kind string) bool {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && str(m["type"]) == kind {
			return true
		}
	}
	return false
}

// acmeLink classifies an Order or Challenge by its state.
func acmeLink(a ACME, o *unstructured.Unstructured, failing bool) Link {
	l := Link{Kind: a.Kind, State: LinkFound, Namespace: a.Namespace, Name: a.Name, Phase: a.State,
		Reason: a.Reason, Type: a.Type, DNSName: a.DNSName, Created: stamp(o)}
	switch {
	case failing:
		l.Tone = ToneBad
	case a.State == "valid":
		l.Tone = ToneOK
	default:
		l.Tone = ToneWarn
	}
	return l
}

// missing is a link whose object could not be shown, for the reason state.
func missing(kind, state, name string) Link {
	return Link{Kind: kind, State: state, Name: name, Tone: ToneNeutral}
}

// chainReads holds what one namespace says about issuance: each kind read
// once, bounded, so a Secret or an Ingress with several certificates costs
// three reads and not three per certificate.
type chainReads struct {
	requests, orders, challenges chainKind
}

// chainKind is one kind's read: its objects, or why there are none to show.
type chainKind struct {
	state    string // LinkFound when read, else the link state a missing object takes
	complete bool
	items    []unstructured.Unstructured
}

func readChainKind(k kinds, kind, namespace string) chainKind {
	crd, err := k.lookup(kind)
	if err != nil {
		if absent(err) {
			return chainKind{state: LinkUnserved}
		}
		return chainKind{state: LinkFailed}
	}
	page, err := k.h.List(k.r, k.cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, Limit: chainLimit})
	if err != nil {
		if sectionState(err) == StateRefused {
			return chainKind{state: LinkRefused}
		}
		return chainKind{state: LinkFailed}
	}
	return chainKind{state: LinkFound, complete: page.Complete, items: page.Items}
}

// absentLink is the link for an object a read of its kind did not find: none
// when the read reached the end, cut when it did not.
func (c chainKind) absentLink(kind string) Link {
	if c.state != LinkFound {
		return missing(kind, c.state, "")
	}
	if !c.complete {
		return missing(kind, LinkCut, "")
	}
	return missing(kind, LinkNone, "")
}

// readChains builds the chain of each certificate of one namespace. A read
// that fails leaves its links drawn as what they are; it never fails the
// answer.
func readChains(r *http.Request, h extension.Host, cluster, namespace string, certs []Certificate) map[string]Chain {
	out := make(map[string]Chain, len(certs))
	if len(certs) == 0 {
		return out
	}
	k, err := catalogue(r, h, cluster)
	if err != nil {
		for _, c := range certs {
			out[c.Name] = unreadChain(c, LinkFailed)
		}
		return out
	}
	reads := chainReads{
		requests:   readChainKind(k, CertificateRequestsKind, namespace),
		orders:     readChainKind(k, OrdersKind, namespace),
		challenges: readChainKind(k, ChallengesKind, namespace),
	}
	issuers := map[string]Link{}
	for _, c := range certs {
		key := c.IssuerKind + "/" + c.IssuerName
		l, ok := issuers[key]
		if !ok {
			l = readIssuerLink(k, namespace, c)
			issuers[key] = l
		}
		out[c.Name] = buildChain(c, reads, l)
	}
	return out
}

func unreadChain(c Certificate, state string) Chain {
	ch := Chain{Secret: c.SecretName, Request: missing("CertificateRequest", state, ""), Order: missing("Order", state, ""),
		Issuer: missing(c.IssuerKind, state, c.IssuerName), Challenges: []Link{}}
	return ch
}

// buildChain walks Certificate to request to order to challenges through
// ownerReferences, in the namespace read and nowhere else.
func buildChain(c Certificate, reads chainReads, issuer Link) Chain {
	ch := Chain{Secret: c.SecretName, Challenges: []Link{}, Issuer: issuer}

	var latest *unstructured.Unstructured
	for i := range reads.requests.items {
		o := &reads.requests.items[i]
		if ownerOf(o, "Certificate") != c.Name {
			continue
		}
		if latest == nil || newer(o, latest) {
			latest = o
		}
	}
	if latest == nil {
		ch.Request = reads.requests.absentLink("CertificateRequest")
		ch.Order = missing("Order", LinkNone, "")
		if reads.requests.state != LinkFound {
			ch.Order = missing("Order", reads.requests.state, "")
		}
		finish(&ch)
		return ch
	}
	ch.Request = readRequest(latest)

	var ord *unstructured.Unstructured
	for i := range reads.orders.items {
		o := &reads.orders.items[i]
		if ownerOf(o, "CertificateRequest") == latest.GetName() {
			ord = o
			break
		}
	}
	if ord == nil {
		ch.Order = reads.orders.absentLink("Order")
		finish(&ch)
		return ch
	}
	a, failing := readOrder(ord)
	ch.Order = acmeLink(a, ord, failing)

	var found []Link
	for i := range reads.challenges.items {
		o := &reads.challenges.items[i]
		if ownerOf(o, "Order") != ord.GetName() {
			continue
		}
		ca, cfail := readChallenge(o)
		found = append(found, acmeLink(ca, o, cfail))
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	if len(found) > maxChallenges {
		ch.MoreChallenges = len(found) - maxChallenges
		found = found[:maxChallenges]
	}
	if len(found) > 0 {
		ch.Challenges = found
	} else if reads.challenges.state != LinkFound {
		// Say that the challenges could not be read, once, as one link.
		ch.Challenges = []Link{missing("Challenge", reads.challenges.state, "")}
	} else if !reads.challenges.complete {
		ch.Challenges = []Link{missing("Challenge", LinkCut, "")}
	}
	finish(&ch)
	return ch
}

// newer is whether a was created after b, ties by name so the answer is stable.
func newer(a, b *unstructured.Unstructured) bool {
	ta, tb := a.GetCreationTimestamp(), b.GetCreationTimestamp()
	if !ta.Equal(&tb) {
		return tb.Before(&ta)
	}
	return a.GetName() > b.GetName()
}

// finish names the first link that is not ok.
func finish(ch *Chain) {
	links := append([]Link{ch.Request, ch.Order}, ch.Challenges...)
	links = append(links, ch.Issuer)
	for _, l := range links {
		if l.State == LinkFound && (l.Tone == ToneBad || l.Tone == ToneWarn) {
			ch.Stuck = l.Kind
			return
		}
	}
}

var issuerGVRs = map[string]string{"Issuer": IssuersKind, "ClusterIssuer": ClusterIssuersKind}

// readIssuerLink reads the one issuer the certificate names, by name.
func readIssuerLink(k kinds, namespace string, c Certificate) Link {
	slug, ok := issuerGVRs[c.IssuerKind]
	if !ok {
		// An external issuer is a kind of another group, which this extra
		// does not read.
		return missing(c.IssuerKind, LinkUnserved, c.IssuerName)
	}
	crd, err := k.lookup(slug)
	if err != nil {
		if absent(err) {
			return missing(c.IssuerKind, LinkUnserved, c.IssuerName)
		}
		return missing(c.IssuerKind, LinkFailed, c.IssuerName)
	}
	ns := namespace
	if !crd.Namespaced {
		ns = ""
	}
	page, err := k.h.List(k.r, k.cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: ns,
		FieldSelector: "metadata.name=" + c.IssuerName, Limit: 1})
	if err != nil {
		if sectionState(err) == StateRefused {
			return missing(c.IssuerKind, LinkRefused, c.IssuerName)
		}
		return missing(c.IssuerKind, LinkFailed, c.IssuerName)
	}
	for i := range page.Items {
		if page.Items[i].GetName() != c.IssuerName {
			continue
		}
		is := readIssuer(&page.Items[i], c.IssuerKind)
		l := Link{Kind: is.Kind, State: LinkFound, Namespace: is.Namespace, Name: is.Name, Type: is.Type,
			Phase: "ready", Tone: ToneOK}
		if is.Broken {
			l.Phase, l.Tone, l.Reason, l.Message = "not ready", ToneBad, is.Reason, is.Message
		}
		return l
	}
	return missing(c.IssuerKind, LinkNone, c.IssuerName)
}
