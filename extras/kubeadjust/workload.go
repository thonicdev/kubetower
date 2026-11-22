package kubeadjust

import (
	"context"
	"errors"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The tab in a workload's own panel: one Deployment, StatefulSet, DaemonSet or
// CronJob,
// drawn as the page's namespace view draws its card - the same bars, the same
// findings with their confidence, the same kills remembered - and nothing else.
//
// It is the page's computation, not a second one. The route reads the
// workload's namespace exactly as `GET /?ns=` does (`read`), shapes it with the
// page's own `cards`, and keeps the one card that is this workload. Reading one
// namespace rather than the whole cluster is what keeps a panel cheap; reading
// less than the namespace would not be the same computation, because a
// workload's history lives in its ReplicaSets and ControllerRevisions and
// what sizes it lives in the namespace's autoscalers.

// CodeUnknownWorkload is the answer for a workload this cluster does not have,
// or a kind the tab is not drawn on.
const CodeUnknownWorkload = "unknown_workload"

// CodeForbidden is the answer for a workload in a namespace the person may not
// read it in.
const CodeForbidden = "forbidden"

// workloadKinds are the kinds the tab is drawn on, as the console's address
// spells them, and the kind the engine files their readings under.
var workloadKinds = map[string]struct {
	gvr  schema.GroupVersionResource
	kind string
}{
	"deployments":  {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment"},
	"statefulsets": {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet"},
	"daemonsets":   {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet"},
	// A CronJob's readings are its Jobs' pods, filed under the CronJob by the
	// two hops the page walks (pod → Job → CronJob).
	"cronjobs": {schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob"},
}

// workloadAnswer is what `GET /workload/{kind}/{namespace}/{name}` answers.
type workloadAnswer struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	// Kind is the engine's word: Deployment, StatefulSet, DaemonSet or CronJob.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Ready, Window and Notes are the page's, for this namespace: whether
	// anything could be measured, and the four lines saying from what.
	Ready  bool   `json:"ready"`
	Window string `json:"window"`
	Notes  []note `json:"notes"`
	// WindowReadAt is the page's, for this namespace.
	WindowReadAt string `json:"windowReadAt,omitempty"`
	// Partial is the page's sentence when the namespace's pods were narrowed;
	// empty when whole.
	Partial string `json:"partial"`
	// Card is this workload as the page draws it. Null when nothing could be
	// measured, or when no running pod of it reported usage - the two are
	// told apart by Ready.
	Card *workloadCard `json:"card"`
	// Commands are the card's kubectl lines, deduplicated, in the order its
	// findings are drawn.
	Commands []string `json:"commands"`
}

func (k KubeAdjust) workload(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		kind, namespace, name := r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name")
		target, known := workloadKinds[kind]
		if !known {
			writeProblem(w, http.StatusNotFound, CodeUnknownWorkload,
				"KubeAdjust has no tab for \""+kind+"\": only Deployments, StatefulSets, DaemonSets and CronJobs.", nil)
			return
		}
		if len(validation.IsDNS1123Label(namespace)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace, "\""+namespace+"\" is not a namespace name.", nil)
			return
		}
		if len(validation.IsDNS1123Subdomain(name)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeUnknownWorkload, "\""+name+"\" is not a "+target.kind+" name.", nil)
			return
		}

		// The workload itself first: a name that is not there is a 404, never a
		// card-less 200 that reads as a workload with no pods.
		page, err := h.List(r, cluster, extension.ListQuery{GVR: target.gvr, Namespace: namespace,
			FieldSelector: "metadata.name=" + name})
		if err != nil {
			writeWorkloadFailure(w, cluster, target.kind, err)
			return
		}
		found := false
		for i := range page.Items {
			if page.Items[i].GetName() == name && page.Items[i].GetNamespace() == namespace {
				found = true
			}
		}
		if !found {
			writeProblem(w, http.StatusNotFound, CodeUnknownWorkload,
				"No "+target.kind+" \""+name+"\" in namespace \""+namespace+"\".", nil)
			return
		}

		read, err := k.read(r, h, cluster, namespace, "")
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		mark := time.Now()
		answer := shapeWorkload(read, cluster, namespace, target.kind, name)
		read.timing.since("shape", mark)
		writeTimedJSON(w, http.StatusOK, answer, &read.timing)
	})
}

// shapeWorkload is the page's namespace view, kept to one card.
func shapeWorkload(read reading, cluster, namespace, kind, name string) workloadAnswer {
	report := read.report
	out := workloadAnswer{
		Cluster: cluster, Namespace: namespace, Kind: kind, Name: name,
		Ready: report.Usage.OK, Window: string(report.Window), Notes: notes(report, read.podsRead),
		WindowReadAt: readAtText(report.WindowReadAt), Commands: []string{},
	}
	if !report.Usage.OK {
		return out
	}
	if read.podsRead && !read.podsComplete {
		out.Partial = pagePartialText
	}
	for _, reading := range report.Readings {
		if reading.Kind != kind || reading.Name != name || reading.Namespace != namespace {
			continue
		}
		card := cards([]rightsizing.WorkloadReading{reading}, report.Storage.OK, report.Window == WindowPending)[0]
		out.Card = &card
		seen := map[string]bool{}
		for _, f := range card.Findings {
			if f.Kubectl != "" && !seen[f.Kubectl] {
				seen[f.Kubectl] = true
				out.Commands = append(out.Commands, f.Kubectl)
			}
		}
	}
	return out
}

// writeWorkloadFailure answers a workload that could not be looked up: 404 for
// a cluster not in the kubeconfig, 403 for a namespace the person may not read
// it in (list_forbidden when the access review refused the person), 503 when
// that could not be asked, 502 otherwise - never a 404, which would say the
// workload is not there.
func writeWorkloadFailure(w http.ResponseWriter, cluster, kind string, err error) {
	if refused, ok := extension.RefusedRead(err, "You may not read this "+kind+"."); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	switch {
	case errors.Is(err, extension.ErrUnknownCluster), errors.Is(err, context.Canceled):
		writeReadFailure(w, cluster, err)
	case apierrors.IsForbidden(err):
		writeProblem(w, http.StatusForbidden, CodeForbidden, "You may not read this "+kind+".", err)
	default:
		writeProblem(w, http.StatusBadGateway, CodeListFailed, "This "+kind+" could not be read.", err)
	}
}
