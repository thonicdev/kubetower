package pending

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// workloadKinds are the kinds Pending adds a column and a tab to, by the
// plural the console's address spells them with, and the Kind a pod's owner
// reference (or a FailedCreate event) names for each.
var workloadKinds = map[string]string{
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"daemonsets":   "DaemonSet",
	"jobs":         "Job",
}

const templateHashLabel = "pod-template-hash"

// workloadOf is the workload a pod, or the object named by an event, belongs
// to. A Deployment never owns a pod: its ReplicaSet does, and that name is the
// Deployment's followed by the pod template's hash, which the pods carry as a
// label. An event names the ReplicaSet only, so there the last dash-separated
// part is taken as the hash; a name that does not end that way stays the
// ReplicaSet's own and matches no Deployment.
func workloadOf(kind, name string, labels map[string]string, fromEvent bool) (string, string) {
	if kind != "ReplicaSet" {
		return kind, name
	}
	if fromEvent {
		i := strings.LastIndexByte(name, '-')
		if i <= 0 || i == len(name)-1 {
			return kind, name
		}
		return "Deployment", name[:i]
	}
	if hash := labels[templateHashLabel]; hash != "" && strings.HasSuffix(name, "-"+hash) {
		return "Deployment", strings.TrimSuffix(name, "-"+hash)
	}
	return kind, name
}

// workloadState is what Pending says of one workload: its pending pods and,
// when its controller was refused a pod, the refusal.
type workloadState struct {
	rows    []Row
	failure *Failure
}

func workloadKey(kind, namespace, name string) string { return kind + "/" + namespace + "/" + name }

// staleRefusal is a refusal the quota would no longer make: its figures, read
// now, leave room for what was asked. The event outlives the problem by an
// hour or so, and a cell must not call a healthy workload stuck.
func staleRefusal(f Failure) bool {
	return f.Quota != nil && f.Quota.Room != nil && *f.Quota.Room
}

// groupWorkloads files the pending pods and the refusals by workload, from
// reads already made: nothing is listed per workload.
func groupWorkloads(pods []pendingPod, failures []Failure) map[string]*workloadState {
	out := map[string]*workloadState{}
	at := func(key string) *workloadState {
		if out[key] == nil {
			out[key] = &workloadState{}
		}
		return out[key]
	}
	for _, p := range pods {
		o := p.row.Owner
		if o == nil {
			continue
		}
		kind, name := workloadOf(o.Kind, o.Name, p.pod.Labels, false)
		s := at(workloadKey(kind, p.row.Namespace, name))
		s.rows = append(s.rows, p.row)
	}
	for i := range failures {
		f := failures[i]
		if staleRefusal(f) {
			continue
		}
		kind, name := workloadOf(f.Kind, f.Name, nil, true)
		s := at(workloadKey(kind, f.Namespace, name))
		if s.failure == nil || f.Last.After(s.failure.Last) {
			s.failure = &f
		}
	}
	return out
}

// cellOf is the Pending column's cell for one workload.
func cellOf(cluster, plural, namespace, name string, s *workloadState) CellAnswer {
	cell := CellAnswer{Href: "/c/" + url.PathEscape(cluster) + "/" + plural + "/" + url.PathEscape(namespace) + "/" + url.PathEscape(name) + "?tab=x." + Key + ".workload"}
	switch {
	case s.failure != nil:
		cell.Text, cell.Tone, cell.Hint = "Cannot create", "bad", failureHint(*s.failure)
		if n := len(s.rows); n > 0 {
			cell.Hint += " · " + strconv.Itoa(n) + " pending"
		}
	default:
		cell.Text, cell.Tone, cell.Hint = strconv.Itoa(len(s.rows))+" pending", "warn", categoryHint(s.rows)
		for _, r := range s.rows {
			if toneOf(r) == "bad" {
				cell.Tone = "bad"
			}
		}
	}
	return cell
}

func failureHint(f Failure) string {
	if f.Quota != nil {
		return "Quota " + f.Quota.Name + ": " + f.Quota.Resource + " " + f.Quota.Requested + " asked"
	}
	return f.Message
}

// categoryHint is "Resources 2 · Taints 1": the pods by their main reason.
func categoryHint(rows []Row) string {
	counts := map[Category]int{}
	for _, r := range rows {
		counts[r.Main]++
	}
	var parts []string
	for _, c := range Categories {
		if n := counts[c]; n > 0 {
			parts = append(parts, CategoryLabels[c]+" "+strconv.Itoa(n))
		}
	}
	return strings.Join(parts, " · ")
}

// workloadCells answers the Pending column of a workload list, from one read
// of the pending pods and one of the FailedCreate events.
func (p Pending) workloadCells(r *http.Request, h extension.Host, cluster, plural string) (map[string]CellAnswer, error) {
	kind := workloadKinds[plural]
	pods, _, err := readPending(r, h, cluster, "", "", MaxPending, p.now())
	if err != nil {
		return nil, err
	}
	// The refusals are read apart: a person who may not list events still
	// gets the pods, and the cells say only what was read.
	failures, _, err := readFailures(r, h, cluster, "")
	if err != nil {
		return nil, err
	}
	out := map[string]CellAnswer{}
	for key, s := range groupWorkloads(pods, failures) {
		parts := strings.SplitN(key, "/", 3)
		if parts[0] != kind || (len(s.rows) == 0 && s.failure == nil) {
			continue
		}
		out[parts[1]+"/"+parts[2]] = cellOf(cluster, plural, parts[1], parts[2], s)
	}
	return out, nil
}

// WorkloadView is `GET /workload/{kind}/{namespace}/{name}`: the pending pods
// of one workload, and the refusal of its controller when it has one.
type WorkloadView struct {
	Cluster   string    `json:"cluster"`
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Read      time.Time `json:"read"`
	// Pods is the pending pods' read; Capped says MaxPending was reached.
	Pods   Section `json:"pods"`
	Capped bool    `json:"capped"`
	// Pending is how many pods of the workload wait, and Rows are those pods,
	// longest waiting first.
	Pending      int      `json:"pending"`
	Rows         []Row    `json:"rows"`
	Failure      *Failure `json:"failure,omitempty"`
	FailuresRead Section  `json:"failuresRead"`
}

const CodeBadKind = "bad_kind"

func (p Pending) workload(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, plural, namespace, name := r.PathValue("cluster"), r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name")
		kind, ok := workloadKinds[plural]
		if !ok {
			writeProblem(w, http.StatusBadRequest, CodeBadKind, quote(plural)+" is not a kind Pending reads the pods of.", nil)
			return
		}
		if !validObject(w, namespace, name) {
			return
		}
		view, err := p.workloadView(r, h, cluster, plural, kind, namespace, name)
		switch {
		case errors.Is(err, errCanceled):
		case err != nil:
			writeReadFailure(w, cluster, err)
		default:
			writeJSON(w, http.StatusOK, view)
		}
	})
}

func (p Pending) workloadView(r *http.Request, h extension.Host, cluster, plural, kind, namespace, name string) (*WorkloadView, error) {
	now := p.now()
	pods, page, err := readPending(r, h, cluster, namespace, "", MaxPending, now)
	if err != nil {
		return nil, err
	}
	failures, fsection, err := readFailures(r, h, cluster, namespace)
	if err != nil {
		return nil, err
	}
	v := &WorkloadView{
		Cluster: cluster, Kind: kind, Namespace: namespace, Name: name, Read: now,
		Pods: sectionOf(page, nil), Capped: int64(len(pods)) >= MaxPending,
		Rows: []Row{}, FailuresRead: fsection,
	}
	if s := groupWorkloads(pods, failures)[workloadKey(kind, namespace, name)]; s != nil {
		v.Rows, v.Failure = s.rows, s.failure
		if v.Rows == nil {
			v.Rows = []Row{}
		}
	}
	sort.SliceStable(v.Rows, func(i, j int) bool {
		if !v.Rows[i].Created.Equal(v.Rows[j].Created) {
			return v.Rows[i].Created.Before(v.Rows[j].Created)
		}
		return v.Rows[i].Name < v.Rows[j].Name
	})
	v.Pending = len(v.Rows)
	return v, nil
}
