package kubecron

import (
	"net/http"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// KubeCron on a Job or a Pod: the object panel's tab asks here which CronJob
// started it and which of the collector's runs it is.
//
// The collector records a run by pod name and CronJob only, so the way from a
// Pod to its run is the owner chain (Pod, Job, CronJob). Every object on the
// way is read through Host.List, for the person, so the tab reveals nothing
// they may not list.

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// ownerSearchPages bounds how far back a Job's runs are looked for: four pages
// of the collector's history, newest first, which is two hundred runs.
const ownerSearchPages = 4

// CronJobRef names a CronJob.
type CronJobRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type ownerAnswer struct {
	// CronJob is the one that started the object, absent when nothing did.
	CronJob *CronJobRef `json:"cronJob,omitempty"`
	// Job is the Job the object is or belongs to, when there is one.
	Job string `json:"job,omitempty"`
	// Note says why there is no CronJob or no run, in a lower-case sentence.
	Note      string         `json:"note,omitempty"`
	Collector collectorState `json:"collector"`
	// Runs are the collector's runs of this object, newest first: one for a
	// Pod, one per attempt for a Job.
	Runs     []runWire `json:"runs"`
	RunsNote string    `json:"runsNote,omitempty"`
}

// CodeObjectNotFound: no Job or Pod of that name that this console may read.
const CodeObjectNotFound = "object_not_found"

func (k KubeCron) owner(h extension.Host, kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		out := ownerAnswer{Runs: []runWire{}}

		gvr, what := jobsGVR, "Job"
		if kind == "pods" {
			gvr, what = podsGVR, "Pod"
		}
		obj, ok := readOne(w, r, h, cluster, gvr, namespace, name)
		if !ok {
			return
		}
		if obj == nil {
			writeProblem(w, http.StatusNotFound, CodeObjectNotFound,
				"There is no "+what+" "+namespace+"/"+name+" on this cluster that this console may read.", nil)
			return
		}

		job := obj
		if kind == "pods" {
			owner := ownerOf(*obj, "Job")
			if owner == "" {
				out.Note = "this Pod was not started by a Job, so no CronJob ran it"
				writeJSON(w, http.StatusOK, out)
				return
			}
			out.Job = owner
			job, ok = readOne(w, r, h, cluster, jobsGVR, namespace, owner)
			if !ok {
				return
			}
			if job == nil {
				out.Note = "its Job " + owner + " is gone or not one this console may read, so its CronJob cannot be told"
				writeJSON(w, http.StatusOK, out)
				return
			}
		} else {
			out.Job = name
		}

		cron := ownerOf(*job, "CronJob")
		if cron == "" {
			out.Note = "its Job was not started by a CronJob, so KubeCron has nothing about it"
			writeJSON(w, http.StatusOK, out)
			return
		}
		report, ok := k.judgeVisible(w, r, h, cluster, namespace, k.now(), cron)
		if !ok {
			return
		}
		if _, visible := findCronJob(report, namespace, cron); !visible {
			out.Note = "its CronJob " + cron + " is not one this console may read"
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.CronJob = &CronJobRef{Namespace: namespace, Name: cron}

		c, state, stop := k.openSession(r, h, cluster)
		defer stop()
		out.Collector = state
		if c == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}

		// The collector's `pod_name` is the pod's name on some installs and
		// the Job's on others, so a run is this object's when it names the
		// pod, the Job, or one of the Job's pods.
		matches := func(rec RunRecord) bool {
			if rec.Pod == out.Job {
				return true
			}
			if kind == "pods" {
				return rec.Pod == name
			}
			return strings.HasPrefix(rec.Pod, out.Job+"-")
		}
		created := job.GetCreationTimestamp().Time
		before := ""
		for page := 0; page < ownerSearchPages; page++ {
			runs, avail := c.Runs(r.Context(), namespace, cron, DefaultRunPage, before)
			if !avail.OK {
				out.RunsNote = avail.Detail
				break
			}
			older := false
			for _, rec := range runs.Runs {
				if matches(rec) {
					out.Runs = append(out.Runs, wireRunRecord(rec))
				}
				// A run that started before the Job existed cannot be one of its
				// attempts, and the pages only get older.
				if !created.IsZero() && rec.StartedAt.Before(created.Add(-1e9)) {
					older = true
				}
			}
			if (kind == "pods" && len(out.Runs) > 0) || older || runs.NextCursor == "" {
				break
			}
			before = runs.NextCursor
		}
		if len(out.Runs) == 0 && out.RunsNote == "" {
			out.RunsNote = "the collector holds no run of this " + strings.ToLower(what) + ": it may predate the collector, or have aged out of its history"
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// readOne reads one object by name through Host.List, for the person. A nil
// object with true is "no such object that this console may read"; false means
// the failure was already written.
func readOne(w http.ResponseWriter, r *http.Request, h extension.Host, cluster string, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, bool) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: gvr, Namespace: namespace, FieldSelector: "metadata.name=" + name})
	if err != nil {
		writeListFailure(w, cluster, err)
		return nil, false
	}
	for i := range page.Items {
		if page.Items[i].GetNamespace() == namespace && page.Items[i].GetName() == name {
			return &page.Items[i], true
		}
	}
	return nil, true
}

// ownerOf is the name of the object's controlling owner of that kind, or empty.
func ownerOf(obj unstructured.Unstructured, kind string) string {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == kind && ref.Controller != nil && *ref.Controller {
			return ref.Name
		}
	}
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == kind {
			return ref.Name
		}
	}
	return ""
}

// livePod says whether the run's pod is still in the cluster, for the person,
// and which pod that is: nil when that could not be asked, so the page offers
// neither answer.
//
// The run names a pod or, on some collectors, its Job (see owner's matcher),
// so the pod is looked for by name first and then by the `job-name` label
// Kubernetes puts on a Job's pods. The newest pod wins: a retried Job's last
// attempt is the one with a log to follow.
func livePod(r *http.Request, h extension.Host, cluster, namespace, named string) (*bool, string) {
	if named == "" {
		return nil, ""
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + named})
	if err != nil {
		return nil, ""
	}
	for i := range page.Items {
		if page.Items[i].GetName() == named {
			found := true
			return &found, named
		}
	}
	page, err = h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, LabelSelector: "job-name=" + named})
	if err != nil {
		return nil, ""
	}
	newest := ""
	var at time.Time
	for i := range page.Items {
		if c := page.Items[i].GetCreationTimestamp().Time; newest == "" || c.After(at) {
			newest, at = page.Items[i].GetName(), c
		}
	}
	found := newest != ""
	return &found, newest
}

// jobOf is the run's Job, while it exists. The collector's pod name may be
// the Job's, so a Job of that name is asked first, then a pod of that name
// for its job-name label. Empty when neither exists or could be read.
func jobOf(r *http.Request, h extension.Host, cluster, namespace, named string) string {
	if named == "" {
		return ""
	}
	if page, err := h.List(r, cluster, extension.ListQuery{GVR: jobsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + named}); err == nil {
		for i := range page.Items {
			if page.Items[i].GetName() == named {
				return named
			}
		}
	}
	if page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + named}); err == nil {
		for i := range page.Items {
			if page.Items[i].GetName() == named {
				return page.Items[i].GetLabels()["job-name"]
			}
		}
	}
	return ""
}
