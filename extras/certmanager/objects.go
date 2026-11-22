package certmanager

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// The kinds the console's lists and panels name, as its address spells them.
const (
	KindCertificates = CertificatesKind
	KindSecrets      = "secrets"
	KindIngresses    = "ingresses"
)

var ingressesGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}

// Cell is one pill in a list's column, as the console's contract reads it.
type Cell struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	Hint string `json:"hint,omitempty"`
	Href string `json:"href,omitempty"`
}

// ObjectAnswer is the tab's and the shortcut's: the certificates that concern
// one object, soonest first.
type ObjectAnswer struct {
	Now          string        `json:"now"`
	Certificates []Certificate `json:"certificates"`
	// Chains is the issuance of each certificate, by its name: they are all
	// in the object's namespace.
	Chains map[string]Chain `json:"chains"`
}

// CodeNoCertificate: the object has no certificate cert-manager manages.
const CodeNoCertificate = "no_certificate"

// CodeBadKind: a kind the column and the tab are not drawn on.
const CodeBadKind = "bad_kind"

// certificatesBySecret reads every certificate in scope and indexes it by
// namespace/secretName. The second answer is false when cert-manager is not
// installed.
func certificatesBySecret(r *http.Request, h extension.Host, cluster, ns string, at time.Time) (map[string][]Certificate, bool, error) {
	k, err := catalogue(r, h, cluster)
	if err != nil {
		return nil, false, err
	}
	page, err := k.certificates(ns)
	var missing notInstalled
	if errors.As(err, &missing) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	out := make(map[string][]Certificate, len(page.Items))
	for i := range page.Items {
		c := readCertificate(&page.Items[i], at)
		if c.SecretName == "" {
			continue
		}
		key := c.Namespace + "/" + c.SecretName
		out[key] = append(out[key], c)
	}
	return out, true, nil
}

// soonest is the certificate that expires first: the one a column shows for
// an Ingress with several. A never-issued one comes first of all.
func soonest(cs []Certificate) Certificate {
	best := cs[0]
	for _, c := range cs[1:] {
		switch {
		case best.DaysLeft == nil:
		case c.DaysLeft == nil || c.NotAfter < best.NotAfter:
			best = c
		}
	}
	return best
}

// ingressSecrets is each TLS secret an Ingress names, as namespace/name.
func ingressSecrets(o *unstructured.Unstructured) []string {
	tls, _, _ := unstructured.NestedSlice(o.Object, "spec", "tls")
	var out []string
	for _, t := range tls {
		m, ok := t.(map[string]any)
		if !ok || str(m["secretName"]) == "" {
			continue
		}
		out = append(out, o.GetNamespace()+"/"+str(m["secretName"]))
	}
	return out
}

func pageHref(cluster string, c Certificate) string {
	return "/c/" + url.PathEscape(cluster) + "/x/" + Key + "?" + url.Values{"cert": {c.Namespace + "/" + c.Name}}.Encode()
}

func cellOf(cluster string, c Certificate) Cell {
	cell := Cell{Href: pageHref(cluster, c)}
	switch {
	case c.DaysLeft == nil:
		cell.Text, cell.Tone = "not issued", "bad"
	case c.Band == BandExpired:
		cell.Text, cell.Tone = "expired", "bad"
	default:
		cell.Text = fmt.Sprintf("%d d", *c.DaysLeft)
		cell.Tone = map[string]string{BandWeek: "bad", BandMonth: "warn", BandLater: "ok"}[c.Band]
	}
	cell.Hint = "Certificate " + c.Name
	if c.NotAfter != "" {
		cell.Hint += ", not after " + c.NotAfter
	}
	if c.Ready != "True" {
		cell.Hint += ", not ready"
		if c.Message != "" {
			cell.Hint += ": " + c.Message
		}
	}
	return cell
}

// cellsHandler is the column: one read for the whole list, the answers keyed
// namespace/name. No Secret is read: a Secret's cell is the certificate that
// names it.
func cellsHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, kind := r.PathValue("cluster"), r.URL.Query().Get("kind")
		at := now()
		cells, err := readCells(r, h, cluster, kind, at)
		if errors.Is(err, errBadKind) {
			writeProblem(w, http.StatusBadRequest, CodeBadKind, "The column is drawn on Certificates, Secrets and Ingresses only.", nil)
			return
		}
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		writeJSON(w, http.StatusOK, cells)
	})
}

var errBadKind = errors.New("not a kind this extra draws on")

func readCells(r *http.Request, h extension.Host, cluster, kind string, at time.Time) (map[string]Cell, error) {
	if kind != KindCertificates && kind != KindSecrets && kind != KindIngresses {
		return nil, errBadKind
	}
	bySecret, installed, err := certificatesBySecret(r, h, cluster, "", at)
	if err != nil {
		return nil, err
	}
	cells := map[string]Cell{}
	if !installed {
		return cells, nil
	}
	switch kind {
	case KindCertificates:
		for _, cs := range bySecret {
			for _, c := range cs {
				cells[c.Namespace+"/"+c.Name] = cellOf(cluster, c)
			}
		}
	case KindSecrets:
		for key, cs := range bySecret {
			cells[key] = cellOf(cluster, soonest(cs))
		}
	case KindIngresses:
		if len(bySecret) == 0 {
			return cells, nil
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: ingressesGVR, Limit: listLimit})
		if err != nil {
			return nil, &failure{step: stepIngresses, err: err}
		}
		for i := range page.Items {
			var found []Certificate
			for _, secret := range ingressSecrets(&page.Items[i]) {
				found = append(found, bySecret[secret]...)
			}
			if len(found) > 0 {
				cells[page.Items[i].GetNamespace()+"/"+page.Items[i].GetName()] = cellOf(cluster, soonest(found))
			}
		}
	}
	return cells, nil
}

// objectHandler is the tab and the shortcut: the certificates of one
// Certificate, Secret or Ingress. None is a 404 with its own code, which the
// tab reads as "not drawn here".
func objectHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, kind := r.PathValue("cluster"), r.PathValue("kind")
		namespace, name := r.PathValue("namespace"), r.PathValue("name")
		if !namespacePattern.MatchString(namespace) {
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace, "That is not a namespace name.", nil)
			return
		}
		at := now()
		found, err := readObject(r, h, cluster, kind, namespace, name, at)
		if errors.Is(err, errBadKind) {
			writeProblem(w, http.StatusBadRequest, CodeBadKind, "The tab is drawn on Certificates, Secrets and Ingresses only.", nil)
			return
		}
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		if len(found) == 0 {
			writeProblem(w, http.StatusNotFound, CodeNoCertificate, "No certificate cert-manager manages is behind this object.", nil)
			return
		}
		sortCertificates(found)
		writeJSON(w, http.StatusOK, ObjectAnswer{Now: at.UTC().Format(time.RFC3339), Certificates: found,
			Chains: readChains(r, h, cluster, namespace, found)})
	})
}

// readObject reads only the object's namespace: the certificates there, and
// for an Ingress the Ingress itself.
func readObject(r *http.Request, h extension.Host, cluster, kind, namespace, name string, at time.Time) ([]Certificate, error) {
	if kind != KindCertificates && kind != KindSecrets && kind != KindIngresses {
		return nil, errBadKind
	}
	bySecret, installed, err := certificatesBySecret(r, h, cluster, namespace, at)
	if err != nil || !installed {
		return nil, err
	}
	switch kind {
	case KindCertificates:
		for _, cs := range bySecret {
			for _, c := range cs {
				if c.Name == name {
					return []Certificate{c}, nil
				}
			}
		}
		return nil, nil
	case KindSecrets:
		return bySecret[namespace+"/"+name], nil
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: ingressesGVR, Namespace: namespace,
		FieldSelector: "metadata.name=" + name, Limit: 1})
	if err != nil {
		return nil, &failure{step: stepIngresses, err: err}
	}
	var found []Certificate
	for i := range page.Items {
		for _, secret := range ingressSecrets(&page.Items[i]) {
			found = append(found, bySecret[secret]...)
		}
	}
	return found, nil
}
