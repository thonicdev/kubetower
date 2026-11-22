package security

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// The bounds of a policy answer, beside the read's.
const (
	// MaxPolicyRows is the most policies an overview lists, the worst first.
	MaxPolicyRows = 1000
	// MaxPolicyResources is the most resources an overview lists.
	MaxPolicyResources = 5000
	// MaxPolicyDetail is the most results one policy's or one workload's
	// answer lists.
	MaxPolicyDetail = 2000
)

// PolicyOverview is `GET /policies`: what the policy engines found, by
// policy, by namespace and by resource. It answers without trivy-operator.
type PolicyOverview struct {
	Cluster string `json:"cluster"`
	Read    string `json:"read"`
	// Engines is false when no policy source is served at all.
	Engines    bool              `json:"engines"`
	Sources    []Source          `json:"sources"`
	Totals     PolicyCounts      `json:"totals"`
	Failing    int               `json:"failing"`
	Namespaces []PolicyNamespace `json:"namespaces"`
	Policies   []PolicyRow       `json:"policies"`
	// PoliciesTotal is how many policies have a non-passing result.
	PoliciesTotal int              `json:"policiesTotal"`
	Resources     []PolicyResource `json:"resources"`
	// ResourcesTotal is how many resources have one.
	ResourcesTotal int      `json:"resourcesTotal"`
	Notes          []string `json:"notes"`
}

// PolicyNamespace is one namespace's bar.
type PolicyNamespace struct {
	Namespace string       `json:"namespace"`
	Counts    PolicyCounts `json:"counts"`
	Resources int          `json:"resources"`
}

// PolicyRow is one policy that does not pass everywhere.
type PolicyRow struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// Kind is a Gatekeeper constraint's kind.
	Kind string `json:"kind,omitempty"`
	// Mode is Enforce, Audit or Mixed (Kyverno), deny, dryrun or warn
	// (Gatekeeper), "" when unknown.
	Mode      string       `json:"mode"`
	Category  string       `json:"category,omitempty"`
	Severity  string       `json:"severity,omitempty"`
	Counts    PolicyCounts `json:"counts"`
	Resources int          `json:"resources"`
	// Listed and Violations are Gatekeeper's: the violations it wrote, and
	// how many there are.
	Listed     int `json:"listed,omitempty"`
	Violations int `json:"violations,omitempty"`
}

// PolicyResource is one resource with a non-passing result.
type PolicyResource struct {
	Namespace string       `json:"namespace,omitempty"`
	Kind      string       `json:"kind"`
	Name      string       `json:"name"`
	Workload  *WorkloadRef `json:"workload,omitempty"`
	Counts    PolicyCounts `json:"counts"`
	Policies  int          `json:"policies"`
}

// PolicyResult is one non-passing result.
type PolicyResult struct {
	Engine    string `json:"engine"`
	Policy    string `json:"policy"`
	Rule      string `json:"rule,omitempty"`
	Result    string `json:"result"`
	Severity  string `json:"severity,omitempty"`
	Category  string `json:"category,omitempty"`
	Message   string `json:"message,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Audit     bool   `json:"audit,omitempty"`
}

// PolicyDetail is `GET /policies/{engine}/{name}`.
type PolicyDetail struct {
	Cluster string         `json:"cluster"`
	Read    string         `json:"read"`
	Sources []Source       `json:"sources"`
	Policy  *PolicyRow     `json:"policy"`
	Results []PolicyResult `json:"results"`
	Total   int            `json:"total"`
	Notes   []string       `json:"notes"`
}

// WorkloadPolicies is the policy half of one workload's answers.
type WorkloadPolicies struct {
	Sources []Source       `json:"sources"`
	Counts  PolicyCounts   `json:"counts"`
	Results []PolicyResult `json:"results"`
	Total   int            `json:"total"`
	// signed is the image verifications the namespace's reports carry, for
	// the workload's signature.
	signed *reportVerdicts
}

// signedOf is the image verifications a workload's policy read found; nil
// where none was read.
func signedOf(w *WorkloadPolicies) *reportVerdicts {
	if w == nil {
		return nil
	}
	return w.signed
}

func resultOf(r policyResult) PolicyResult {
	return PolicyResult{Engine: r.engine, Policy: r.policy, Rule: r.rule, Result: r.outcome, Severity: r.severity,
		Category: r.category, Message: r.message, Namespace: r.res.namespace, Kind: r.res.kind, Name: r.res.name, Audit: r.audit}
}

func served(sources []Source) bool {
	for _, s := range sources {
		if s.State != StateAbsent {
			return true
		}
	}
	return false
}

// policyIndex is the owners read before the reports, to tie a resource to its
// workload: pods, ReplicaSets and Jobs as tables, as the overview reads them.
func policyIndex(r *http.Request, h extension.Host, cluster, namespace string) (*index, []string) {
	var pods, sets, jobs []object
	var notes []string
	var mu sync.Mutex
	read := func(what string, q extension.ListQuery, keep func([]extension.Row)) task {
		return task{
			run: func() error {
				page, err := h.List(r, cluster, q)
				if err != nil {
					return err
				}
				keep(page.Rows)
				return nil
			},
			fail: func(err error) {
				mu.Lock()
				notes = append(notes, what+" "+failedWord(err)+", so a resource may read as itself rather than its workload")
				mu.Unlock()
			},
		}
	}
	runAll([]task{
		read("Pods", extension.ListQuery{GVR: podsGVR, Namespace: namespace, Columns: true, Limit: MaxRead},
			func(rows []extension.Row) { pods = objects(rows, false) }),
		read("ReplicaSets", extension.ListQuery{GVR: replicaSetsGVR, Namespace: namespace, Columns: true, Limit: MaxRead},
			func(rows []extension.Row) { sets = objects(rows, true) }),
		read("Jobs", extension.ListQuery{GVR: jobsGVR, Namespace: namespace, Columns: true, Limit: MaxRead},
			func(rows []extension.Row) { jobs = objects(rows, false) }),
	})
	return newIndex(&snapshot{pods: pods, sets: sets, jobs: jobs}), notes
}

// workloadOfResource is the workload a resource is part of: a Pod's
// controller, a ReplicaSet's Deployment, a Job's CronJob, or itself.
func workloadOfResource(ix *index, res resource) WorkloadRef {
	self := WorkloadRef{Namespace: res.namespace, Kind: res.kind, Name: res.name}
	switch res.kind {
	case "Pod":
		if w, ok := ix.pods[res.namespace+"/"+res.name]; ok {
			return w
		}
	case "ReplicaSet", "Job":
		if w, ok := ix.parentOf(res.namespace, res.kind, res.name); ok {
			return w
		}
	}
	return self
}

func (x Security) policies(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ns := r.URL.Query().Get("ns")
		if ns != "" && !validNamespace(ns) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ns)+" is not a namespace name.", nil)
			return
		}
		p, err := readPolicies(r, h, cluster, policyFilter{namespace: ns})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out := PolicyOverview{Cluster: cluster, Read: x.clock().UTC().Format(time.RFC3339), Sources: p.sources,
			Engines: served(p.sources), Namespaces: []PolicyNamespace{}, Policies: []PolicyRow{},
			Resources: []PolicyResource{}, Notes: p.notes}
		if out.Engines {
			x.fillOverview(&out, p, p.owners)
		}
		if out.Notes == nil {
			out.Notes = []string{}
		}
		writeAnswer(w, out)
	})
}

func (x Security) fillOverview(out *PolicyOverview, p *policyRead, ix *index) {
	for _, a := range p.byPolicy {
		out.Totals.add(a.counts)
		row := PolicyRow{Engine: a.engine, Name: a.name, Kind: a.kind, Mode: a.mode, Category: a.category,
			Severity: a.severity, Counts: a.counts, Resources: len(a.resources), Listed: a.listed, Violations: a.violations}
		if row.Counts.any() {
			out.Policies = append(out.Policies, row)
		}
	}
	sort.Slice(out.Policies, func(i, j int) bool {
		a, b := out.Policies[i], out.Policies[j]
		if a.Counts.Fail+a.Counts.Error != b.Counts.Fail+b.Counts.Error {
			return a.Counts.Fail+a.Counts.Error > b.Counts.Fail+b.Counts.Error
		}
		if a.Counts.Warn != b.Counts.Warn {
			return a.Counts.Warn > b.Counts.Warn
		}
		return policyKey(a.Engine, a.Name) < policyKey(b.Engine, b.Name)
	})
	out.PoliciesTotal = len(out.Policies)
	if len(out.Policies) > MaxPolicyRows {
		out.Policies = out.Policies[:MaxPolicyRows]
	}

	byNS := map[string]*PolicyNamespace{}
	for _, ra := range p.byResource {
		ref := workloadOfResource(ix, ra.res)
		row := PolicyResource{Namespace: ra.res.namespace, Kind: ra.res.kind, Name: ra.res.name, Counts: ra.counts,
			Policies: len(ra.policies)}
		if ra.res.namespace != "" && knownKind(ref.Kind) {
			row.Workload = &ref
		}
		out.Resources = append(out.Resources, row)
		if ra.res.namespace != "" {
			n := byNS[ra.res.namespace]
			if n == nil {
				n = &PolicyNamespace{Namespace: ra.res.namespace}
				byNS[ra.res.namespace] = n
			}
			n.Counts.add(ra.counts)
			n.Resources++
		}
	}
	if out.Totals.Fail+out.Totals.Error > 0 || len(out.Resources) > 0 {
		for _, r := range out.Resources {
			if r.Counts.Fail+r.Counts.Error > 0 {
				out.Failing++
			}
		}
	}
	sort.Slice(out.Resources, func(i, j int) bool {
		a, b := out.Resources[i], out.Resources[j]
		if a.Counts.Fail+a.Counts.Error != b.Counts.Fail+b.Counts.Error {
			return a.Counts.Fail+a.Counts.Error > b.Counts.Fail+b.Counts.Error
		}
		if a.Counts.Warn != b.Counts.Warn {
			return a.Counts.Warn > b.Counts.Warn
		}
		return a.Namespace+"/"+a.Kind+"/"+a.Name < b.Namespace+"/"+b.Kind+"/"+b.Name
	})
	out.ResourcesTotal = len(out.Resources)
	if len(out.Resources) > MaxPolicyResources {
		out.Resources = out.Resources[:MaxPolicyResources]
	}
	for _, n := range byNS {
		out.Namespaces = append(out.Namespaces, *n)
	}
	sort.Slice(out.Namespaces, func(i, j int) bool {
		a, b := out.Namespaces[i].Counts, out.Namespaces[j].Counts
		if a.Fail+a.Error+a.Warn != b.Fail+b.Error+b.Warn {
			return a.Fail+a.Error+a.Warn > b.Fail+b.Error+b.Warn
		}
		return out.Namespaces[i].Namespace < out.Namespaces[j].Namespace
	})
	if len(out.Namespaces) > MaxNamespaces {
		out.Namespaces = out.Namespaces[:MaxNamespaces]
	}
}

func (x Security) policyDetail(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, engine, name := r.PathValue("cluster"), strings.ToLower(r.PathValue("engine")), r.PathValue("name")
		if engine == "" || name == "" || len(name) > 512 || strings.ContainsAny(engine, "/ ") {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "Name a policy as /policies/<engine>/<name>.", nil)
			return
		}
		p, err := readPolicies(r, h, cluster, policyFilter{engine: engine, policy: name})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out := PolicyDetail{Cluster: cluster, Read: x.clock().UTC().Format(time.RFC3339), Sources: p.sources,
			Results: []PolicyResult{}, Total: p.total, Notes: p.notes}
		if a := p.byPolicy[policyKey(engine, name)]; a != nil {
			out.Policy = &PolicyRow{Engine: a.engine, Name: a.name, Kind: a.kind, Mode: a.mode, Category: a.category,
				Severity: a.severity, Counts: a.counts, Resources: len(a.resources), Listed: a.listed, Violations: a.violations}
		}
		for i, rr := range p.results {
			if i == MaxPolicyDetail {
				break
			}
			out.Results = append(out.Results, resultOf(rr))
		}
		if out.Notes == nil {
			out.Notes = []string{}
		}
		writeAnswer(w, out)
	})
}

// workloadPolicies is one workload's policy results, read in its namespace:
// the reports there walked and kept only where they are about the workload or
// one of its pods, ReplicaSets or Jobs, each result once. nil when no policy
// source is served.
func workloadPolicies(r *http.Request, h extension.Host, cluster string, of WorkloadRef) (*WorkloadPolicies, error) {
	p, err := readPolicies(r, h, cluster, policyFilter{namespace: of.Namespace, noCluster: true})
	if err != nil {
		return nil, err
	}
	if !served(p.sources) {
		return nil, nil
	}
	ix := p.owners
	// A pod, a ReplicaSet or a Job asked for is its workload's.
	ref := workloadOfResource(ix, resource{namespace: of.Namespace, kind: of.Kind, name: of.Name})
	out := &WorkloadPolicies{Sources: p.sources, Results: []PolicyResult{}, signed: p.signed}
	for _, rr := range p.results {
		if workloadOfResource(ix, rr.res) != ref {
			continue
		}
		var c PolicyCounts
		switch rr.outcome {
		case "fail":
			c.Fail = 1
		case "warn":
			c.Warn = 1
		case "error":
			c.Error = 1
		}
		if rr.audit {
			c.Audit = 1
		}
		out.Counts.add(c)
		out.Total++
		if len(out.Results) < MaxPolicyDetail {
			out.Results = append(out.Results, resultOf(rr))
		}
	}
	return out, nil
}
