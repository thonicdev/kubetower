package cnpg

import (
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// The Pods list's column and an instance pod's side panel.

// PodRole is one instance pod's role, as its labels say.
type PodRole struct {
	// Role is primary or replica (cnpg.io/instanceRole).
	Role string `json:"role"`
	// Cluster is the CloudNativePG cluster it is an instance of.
	Cluster string `json:"cluster"`
}

// PodRoles is GET /pods: every instance pod the person may list, keyed
// `namespace/name`.
type PodRoles struct {
	Pods     map[string]PodRole `json:"pods"`
	Complete bool               `json:"complete"`
	Note     string             `json:"note,omitempty"`
}

// instanceSelector is the label every instance pod carries and no other pod
// of the operator's does (a Pooler's or a Job's).
const instanceSelector = "cnpg.io/podRole=instance"

// podRoles reads the roles from the pods' labels alone: one list filtered by
// the API server, no Cluster read. The label can lag behind the status
// during a switchover; the Cluster's page reads the status.
func (x CNPG) podRoles(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, LabelSelector: instanceSelector, Limit: MaxPods})
		if err != nil {
			podListFailed(w, cluster, err)
			return
		}
		out := PodRoles{Pods: make(map[string]PodRole, len(page.Items)), Complete: page.Complete, Note: page.Note}
		for i := range page.Items {
			labels := page.Items[i].GetLabels()
			role, owner := labels["cnpg.io/instanceRole"], labels["cnpg.io/cluster"]
			if role == "" {
				role = labels["role"]
			}
			if owner == "" || role == "" {
				continue
			}
			out.Pods[page.Items[i].GetNamespace()+"/"+page.Items[i].GetName()] = PodRole{Role: role, Cluster: owner}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func podListFailed(w http.ResponseWriter, cluster string, err error) {
	if refused, ok := extension.RefusedRead(err, "You may not list pods here."); ok {
		writeJSON(w, refused.Status, problem{Code: refused.Code, Message: refused.Message})
		return
	}
	if isUnknownCluster(err) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
		return
	}
	writeProblem(w, http.StatusBadGateway, CodeListFailed, "The pods could not be listed.", err)
}

// PodAnswer is GET /pods/{namespace}/{name}: the Cluster an instance pod
// belongs to, summarized, for its side panel.
type PodAnswer struct {
	Pod  string  `json:"pod"`
	Role string  `json:"role"`
	Of   Summary `json:"of"`
}

func (x CNPG) pod(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + name, Limit: 1})
		if err != nil {
			podListFailed(w, cluster, err)
			return
		}
		owner, role := "", ""
		for i := range page.Items {
			if page.Items[i].GetName() == name {
				labels := page.Items[i].GetLabels()
				if labels["cnpg.io/podRole"] == "instance" || labels["cnpg.io/instanceName"] != "" {
					owner, role = labels["cnpg.io/cluster"], labels["cnpg.io/instanceRole"]
				}
			}
		}
		if owner == "" {
			writeProblem(w, http.StatusNotFound, CodeNotInstance, "This pod is not an instance of a CloudNativePG cluster.", nil)
			return
		}
		k, crd, ok := resolveClusters(w, r, h, cluster)
		if !ok {
			return
		}
		c, ok := readCluster(w, r, h, cluster, crd, namespace, owner)
		if !ok {
			return
		}
		now := x.clock().UTC()
		backups, _ := readBackups(r, h, cluster, namespace, k, owner)
		sum := summarise(c, backups[clusterKey(namespace, owner)], 0, 0, now)
		for _, in := range sum.Instances {
			if in.Name == name {
				role = in.Role
			}
		}
		writeJSON(w, http.StatusOK, PodAnswer{Pod: name, Role: role, Of: sum})
	})
}
