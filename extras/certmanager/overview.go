package certmanager

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// listLimit is the most objects of one kind a read holds. Past it the answer
// says the read was cut (`complete: false`) rather than reading on: a page
// about expiry is still right about the 5 000 it read, and a cluster is not
// read to its end on every visit.
const listLimit = 5000

// maxRows is the most certificate rows an answer carries. The counts, the
// strip and the ring are over every certificate read; the rows are the alarms
// first, so a cut drops only certificates with nothing to say.
const maxRows = 2000

// maxSection bounds the issuers and ACME rows the same way.
const maxSection = 500

// now is the clock; tests move it.
var now = time.Now

// Section states, for the kinds read beside the certificates.
const (
	StateRead    = "read"
	StateAbsent  = "absent"  // the kind is not served: that part of cert-manager is not installed
	StateRefused = "refused" // the person may not list it (or it could not be asked)
	StateFailed  = "failed"  // the read failed
)

// Counts are the tiles.
type Counts struct {
	Total          int `json:"total"`
	Expired        int `json:"expired"`
	Week           int `json:"week"`  // expires within 7 days, not yet expired
	Month          int `json:"month"` // expires within 30 days, not yet expired (Week included)
	NotReady       int `json:"notReady"`
	RenewalOverdue int `json:"renewalOverdue"`
	ACMEFailing    int `json:"acmeFailing"`
	IssuersBroken  int `json:"issuersBroken"`
}

// Ring is the Ready states.
type Ring struct {
	Ready    int `json:"ready"`
	NotReady int `json:"notReady"`
	Unknown  int `json:"unknown"`
}

// IssuerSection is the Issuers and ClusterIssuers, read together.
type IssuerSection struct {
	State   string   `json:"state"`
	Message string   `json:"message,omitempty"`
	Items   []Issuer `json:"items"`
	Total   int      `json:"total"`
}

// ACMESection is the failing Orders and Challenges.
type ACMESection struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Items   []ACME `json:"items"`
	// InFlight counts the orders and challenges read, failing or not.
	InFlight int `json:"inFlight"`
}

// Overview is the page's one answer.
type Overview struct {
	// Installed is false when the cluster serves no Certificate kind: then
	// Message says why and nothing else is filled.
	Installed bool   `json:"installed"`
	Message   string `json:"message,omitempty"`
	Namespace string `json:"namespace"`
	Now       string `json:"now"`
	// Complete is false when a read was cut short or narrowed; Notes say
	// how.
	Complete     bool          `json:"complete"`
	Notes        []string      `json:"notes"`
	Counts       Counts        `json:"counts"`
	Ring         Ring          `json:"ring"`
	Strip        []int         `json:"strip"`
	Certificates []Certificate `json:"certificates"`
	// Shown is len(Certificates); Counts.Total is how many were read.
	Shown   int           `json:"shown"`
	Issuers IssuerSection `json:"issuers"`
	ACME    ACMESection   `json:"acme"`
}

var namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func overviewHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ns := r.URL.Query().Get("ns")
		if ns != "" && !namespacePattern.MatchString(ns) {
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace, "That is not a namespace name.", nil)
			return
		}
		answer, err := readOverview(r, h, cluster, ns, now())
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	})
}

// kinds resolves the qualified kinds through the cluster's catalog.
type kinds struct {
	r        *http.Request
	h        extension.Host
	cluster  string
	registry extension.CRDRegistry
}

func (k kinds) lookup(kind string) (extension.CRD, error) {
	return k.registry.Lookup(k.r.Context(), kind)
}

// absent is a kind the cluster does not serve: not installed, which is an
// answer about the cluster, never a failure.
func absent(err error) bool {
	return errors.Is(err, extension.ErrUnknownKind) || errors.Is(err, extension.ErrNotServed)
}

// list reads one kind, bounded. A kind that is not served is (zero, absent).
func (k kinds) list(kind, namespace string) (extension.Page, string, error) {
	crd, err := k.lookup(kind)
	if err != nil {
		if absent(err) {
			return extension.Page{}, StateAbsent, nil
		}
		return extension.Page{}, StateFailed, err
	}
	if !crd.Namespaced {
		namespace = ""
	}
	page, err := k.h.List(k.r, k.cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, Limit: listLimit})
	if err != nil {
		return extension.Page{}, sectionState(err), err
	}
	return page, StateRead, nil
}

func sectionState(err error) string {
	if errors.Is(err, extension.ErrDenied) || errors.Is(err, extension.ErrUnreviewable) {
		return StateRefused
	}
	return StateFailed
}

// notInstalled carries the sentence for a cluster without the
// Certificate kind; readOverview turns it into an answer, not a failure.
type notInstalled struct{ message string }

func (n notInstalled) Error() string { return n.message }

func catalogue(r *http.Request, h extension.Host, cluster string) (kinds, error) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return kinds{}, &failure{step: stepCatalogue, err: err}
	}
	return kinds{r: r, h: h, cluster: cluster, registry: registry}, nil
}

// certificates reads the certificates in scope: a refusal or a failure is
// the request's, and a kind that is not served is notInstalled.
func (k kinds) certificates(ns string) (extension.Page, error) {
	crd, err := k.lookup(CertificatesKind)
	switch {
	case errors.Is(err, extension.ErrUnknownKind):
		return extension.Page{}, notInstalled{"cert-manager is not installed in this cluster: it serves no Certificate kind (certificates.cert-manager.io)."}
	case errors.Is(err, extension.ErrNotServed):
		return extension.Page{}, notInstalled{"cert-manager's Certificate kind is defined in this cluster, but no version of it is served."}
	case err != nil:
		return extension.Page{}, &failure{step: stepCatalogue, err: err}
	}
	page, err := k.h.List(k.r, k.cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: ns, Limit: listLimit})
	if err != nil {
		return extension.Page{}, &failure{step: stepCertificates, err: err}
	}
	return page, nil
}

func readOverview(r *http.Request, h extension.Host, cluster, ns string, at time.Time) (Overview, error) {
	out := Overview{Namespace: ns, Now: at.UTC().Format(time.RFC3339), Notes: []string{}, Strip: make([]int, StripDays),
		Certificates: []Certificate{}, Issuers: IssuerSection{Items: []Issuer{}}, ACME: ACMESection{Items: []ACME{}}}
	k, err := catalogue(r, h, cluster)
	if err != nil {
		return Overview{}, err
	}
	page, err := k.certificates(ns)
	var missing notInstalled
	if errors.As(err, &missing) {
		out.Message = missing.message
		out.Complete = true
		return out, nil
	}
	if err != nil {
		return Overview{}, err
	}
	out.Installed = true
	out.Complete = page.Complete
	if !page.Complete {
		out.Notes = append(out.Notes, "Certificates: "+noteOr(page.Note))
	}

	for i := range page.Items {
		c := readCertificate(&page.Items[i], at)
		tally(&out, c)
		out.Certificates = append(out.Certificates, c)
	}
	sortCertificates(out.Certificates)
	if len(out.Certificates) > maxRows {
		out.Certificates = out.Certificates[:maxRows]
		out.Notes = append(out.Notes, "Only the first certificates are listed, the alarms first; the counts are over every one read.")
	}
	out.Shown = len(out.Certificates)

	out.Issuers = readIssuers(k, ns, &out)
	out.Counts.IssuersBroken = brokenIssuers(out.Issuers)
	out.ACME = readACME(k, ns, &out)
	out.Counts.ACMEFailing = len(out.ACME.Items)
	if len(out.ACME.Items) > maxSection {
		out.ACME.Items = out.ACME.Items[:maxSection]
	}
	return out, nil
}

func noteOr(note string) string {
	if note == "" {
		return "the read stopped before the end."
	}
	return note
}

// tally counts one certificate into the tiles, the ring and the strip.
func tally(out *Overview, c Certificate) {
	out.Counts.Total++
	switch c.Band {
	case BandExpired:
		out.Counts.Expired++
	case BandWeek:
		out.Counts.Week++
		out.Counts.Month++
	case BandMonth:
		out.Counts.Month++
	}
	if c.RenewalOverdue {
		out.Counts.RenewalOverdue++
	}
	switch c.Ready {
	case "True":
		out.Ring.Ready++
	case "False":
		out.Ring.NotReady++
		out.Counts.NotReady++
	default:
		out.Ring.Unknown++
		out.Counts.NotReady++
	}
	if c.DaysLeft != nil && *c.DaysLeft >= 0 && *c.DaysLeft < StripDays && c.Band != BandExpired {
		out.Strip[*c.DaysLeft]++
	}
}

// readIssuers reads the Issuers in scope and every ClusterIssuer (they serve
// every namespace). The section reads as long as one of the two kinds did: a
// person who may list the Issuers of their namespaces but not the
// ClusterIssuers counts the broken ones they can see, and the page says what
// was left out as a note. Only when neither kind could be read does the
// section say Refused or Failed, and a failure outranks a refusal.
func readIssuers(k kinds, ns string, out *Overview) IssuerSection {
	s := IssuerSection{State: StateRead, Items: []Issuer{}}
	read := 0
	var left []string
	var failed, refused *sectionError
	for _, kind := range []struct{ slug, name string }{{IssuersKind, "Issuer"}, {ClusterIssuersKind, "ClusterIssuer"}} {
		page, state, err := k.list(kind.slug, ns)
		if err != nil {
			e := &sectionError{state: state, message: sectionMessage(kind.name+"s", state, err)}
			left = append(left, e.message)
			if state == StateRefused && refused == nil {
				refused = e
			} else if state != StateRefused && failed == nil {
				failed = e
			}
			continue
		}
		if state == StateAbsent {
			continue
		}
		read++
		if !page.Complete {
			out.Complete = false
			out.Notes = append(out.Notes, kind.name+"s: "+noteOr(page.Note))
		}
		for i := range page.Items {
			s.Items = append(s.Items, readIssuer(&page.Items[i], kind.name))
		}
	}
	switch {
	case read == 0 && failed != nil:
		s.State, s.Message = failed.state, failed.message
	case read == 0 && refused != nil:
		s.State, s.Message = refused.state, refused.message
	case len(left) > 0:
		out.Complete = false
		out.Notes = append(out.Notes, left...)
	}
	s.Total = len(s.Items)
	// The broken first, then by kind, namespace and name.
	sortIssuers(s.Items)
	if len(s.Items) > maxSection {
		s.Items = s.Items[:maxSection]
	}
	return s
}

// sectionError is a kind of a section that could not be read.
type sectionError struct{ state, message string }

func brokenIssuers(s IssuerSection) int {
	n := 0
	for _, i := range s.Items {
		if i.Broken {
			n++
		}
	}
	return n
}

func readACME(k kinds, ns string, out *Overview) ACMESection {
	s := ACMESection{State: StateRead, Items: []ACME{}}
	absentBoth := true
	// Every order's certificate, failing or not, so a challenge names its own.
	orderCert := map[string]string{}
	for _, kind := range []struct {
		slug, name string
		read       func(*unstructured.Unstructured) (ACME, bool)
	}{{OrdersKind, "Order", readOrder}, {ChallengesKind, "Challenge", readChallenge}} {
		page, state, err := k.list(kind.slug, ns)
		if err != nil {
			s.State = state
			s.Message = sectionMessage(kind.name+"s", state, err)
			absentBoth = false
			continue
		}
		if state == StateAbsent {
			continue
		}
		absentBoth = false
		if !page.Complete {
			out.Complete = false
			out.Notes = append(out.Notes, kind.name+"s: "+noteOr(page.Note))
		}
		for i := range page.Items {
			s.InFlight++
			a, failing := kind.read(&page.Items[i])
			if a.Kind == "Order" && a.Certificate != "" {
				orderCert[a.Namespace+"/"+a.Name] = a.Certificate
			}
			if failing {
				s.Items = append(s.Items, a)
			}
		}
	}
	if absentBoth {
		s.State = StateAbsent
		s.Message = "cert-manager's ACME kinds (Orders, Challenges) are not served in this cluster."
	}
	for i := range s.Items {
		if a := &s.Items[i]; a.Certificate == "" && a.order != "" {
			a.Certificate = orderCert[a.Namespace+"/"+a.order]
		}
	}
	sortACME(s.Items)
	return s
}

func sectionMessage(what, state string, err error) string {
	if state == StateRefused {
		if errors.Is(err, extension.ErrUnreviewable) {
			return extension.UnreviewedText
		}
		return "The cluster says you may not list the " + what + " here, so they are not shown."
	}
	return "The " + what + " could not be read: " + shortError(err)
}
