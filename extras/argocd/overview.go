package argocd

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd/report"
	"github.com/thonicdev/kubetower/textutil"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GET /report answers the Delivery Center's whole page. The model and its
// judgments are in `report/`; this file only reads its inputs through
// `Host`.

var servicesGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}

func (d Delivery) report(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")

		registry, err := h.Definitions(r, cluster)
		if unknownCluster(w, cluster, err) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeCatalogueUnreadable, "The custom resource catalog could not be read, so whether this cluster serves Argo CD is unknown.", err)
			return
		}

		read := func(slug, plural string) (report.Kind, bool) {
			k := report.Kind{Plural: plural}
			crd, err := registry.Lookup(r.Context(), slug)
			switch {
			case isKindAnswer(err):
				return k, true
			case err != nil:
				writeProblem(w, http.StatusBadGateway, CodeKindUnresolved, "Resolving "+slug+" failed.", err)
				return k, false
			}
			k.Served = true
			page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR()})
			if err != nil {
				// One kind failing is that area's note, not the page's failure.
				k.Unavailable = textutil.FirstLineN(err.Error(), 200)
				// Refused Applications are the page's answer: who was refused
				// and how to grant it.
				if slug == ApplicationKind && writeRefused(w, err, func(who string) string {
					return "Argo CD is installed on this cluster, and " + who + " may not list its Applications."
				}) {
					return k, false
				}
				return k, true
			}
			k.Note = page.Note
			for i := range page.Items {
				raw, err := json.Marshal(page.Items[i].Object)
				if err != nil {
					continue
				}
				k.Items = append(k.Items, raw)
			}
			return k, true
		}

		apps, ok := read(ApplicationKind, "applications")
		if !ok {
			return
		}
		sets, ok := read(report.ApplicationSetKind, "applicationsets")
		if !ok {
			return
		}
		projects, ok := read(report.AppProjectKind, "appprojects")
		if !ok {
			return
		}
		if !apps.Served && !sets.Served && !projects.Served {
			writeProblem(w, http.StatusNotFound, CodeKindUnknown, "This cluster serves none of Argo CD's custom resource kinds, so Argo CD is not installed on it.", nil)
			return
		}

		install := report.ProbeInstall(func(selector string) ([]string, error) {
			page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, LabelSelector: selector})
			if err != nil {
				return nil, err
			}
			namespaces := make([]string, 0, len(page.Items))
			for i := range page.Items {
				namespaces = append(namespaces, page.Items[i].GetNamespace())
			}
			return namespaces, nil
		})

		now := time.Now()
		writeJSON(w, http.StatusOK, report.ViewOf(report.Build(cluster, now, apps, sets, projects, install), now))
	})
}
