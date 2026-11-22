package argocd

import (
	"net/http"
	"net/url"

	"github.com/thonicdev/kubetower/extension"
)

// The Argo CD column of the console's lists. One read of the Applications, for
// the person, answers the whole list from their status.resources; nothing is
// read per row, and a row Argo CD does not manage has no answer (an empty
// cell).

// ColumnCell is one row's answer, in the core's ExtensionCell shape.
type ColumnCell struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	Hint string `json:"hint,omitempty"`
	Href string `json:"href,omitempty"`
}

func (d Delivery) column(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		segment := r.URL.Query().Get("kind")
		if segment == "" {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "The address names no kind.", nil)
			return
		}
		t, err := kindOf(r, h, cluster, segment)
		if kindProblem(w, segment, err) || unknownCluster(w, cluster, err) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeKindUnresolved, "The cluster's kinds could not be read.", err)
			return
		}

		crd, ok := resolve(w, r, h, cluster, ApplicationKind, "Argo CD")
		if !ok {
			return
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR()})
		if writeRefused(w, err, func(who string) string {
			return capital(who) + " may not list Argo CD's Applications."
		}) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeListFailed, "The Applications could not be read.", err)
			return
		}

		out := map[string]ColumnCell{}
		for i := range page.Items {
			app := applicationDetail(page.Items[i])
			for _, res := range app.Resources {
				if res.Group != t.gvr.Group || res.Kind != t.kind {
					continue
				}
				key := res.Name
				if res.Namespace != "" {
					key = res.Namespace + "/" + res.Name
				}
				cell := ColumnCell{Text: res.Status, Hint: "Application " + app.Namespace + "/" + app.Name}
				switch res.Status {
				case "Synced":
					cell.Tone = "ok"
				case "OutOfSync":
					cell.Tone = "warn"
				case "":
					cell.Text = "managed"
				}
				if res.RequiresPruning {
					cell.Text, cell.Tone = "prune", "warn"
					cell.Hint += ": no longer in git, Argo CD would delete it"
				}
				cell.Href = objectHref(cluster, segment, res.Namespace, res.Name) + "?tab=x.argocd.sync"
				// When two Applications name one object, the OutOfSync one wins.
				if prev, seen := out[key]; !seen || prev.Tone != "warn" {
					out[key] = cell
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// objectHref is the object's page in the console.
func objectHref(cluster, segment, namespace, name string) string {
	path := "/c/" + url.PathEscape(cluster) + "/" + url.PathEscape(segment) + "/"
	if namespace != "" {
		path += url.PathEscape(namespace) + "/"
	}
	return path + url.PathEscape(name)
}
