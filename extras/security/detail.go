package security

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Detail is `GET /workload/{namespace}/{kind}/{name}`: one workload's
// findings, summarized from its reports' bodies.
type Detail struct {
	Cluster    string      `json:"cluster"`
	Read       string      `json:"read"`
	Workload   WorkloadRef `json:"workload"`
	Sources    []Source    `json:"sources"`
	Containers []Container `json:"containers"`
	// Exposure names every Service and Ingress that reaches it from outside.
	Exposure []string `json:"exposure"`
	// CVEs are the worst first, at most MaxCVEs; CVEsTotal is how many the
	// reports held.
	CVEs      []CVE    `json:"cves"`
	CVEsTotal int      `json:"cvesTotal"`
	Checks    []Check  `json:"checks"`
	Secrets   []Secret `json:"secrets"`
	// Signature is what the admission wrote about its images; null where no
	// pod of it carries the annotation and no policy report a verdict.
	Signature *Signature `json:"signature"`
	// Policies is what the policy engines found about the workload, null
	// where none is served.
	Policies *WorkloadPolicies `json:"policies,omitempty"`
	// Notes are the reads that were refused, failed or narrowed.
	Notes []string `json:"notes"`
}

// Container is one container's image, as its vulnerability report says.
type Container struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	OS        string `json:"os,omitempty"`
	EOSL      *bool  `json:"eosl,omitempty"`
	Scanned   string `json:"scanned,omitempty"`
	Counts    Counts `json:"counts"`
}

// CVE is one finding, without its description.
type CVE struct {
	ID        string   `json:"id"`
	Severity  string   `json:"severity"`
	Score     *float64 `json:"score,omitempty"`
	Pkg       string   `json:"pkg"`
	Installed string   `json:"installed"`
	Fixed     string   `json:"fixed"`
	Title     string   `json:"title"`
	Link      string   `json:"link,omitempty"`
	Container string   `json:"container"`
}

// Check is one failed configuration check.
type Check struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Category string `json:"category,omitempty"`
}

// Secret is one exposed secret, never its matched text.
type Secret struct {
	Rule     string `json:"rule"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Target   string `json:"target"`
}

// severities is the order findings are listed in, the worst first.
var severities = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}

// severity is a finding's severity as the contract spells it.
func severity(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if _, ok := severities[s]; ok {
		return s
	}
	return "UNKNOWN"
}

func validName(name string) bool { return len(validation.IsDNS1123Subdomain(name)) == 0 }

func (x Security) detail(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ref := WorkloadRef{Namespace: r.PathValue("namespace"), Kind: r.PathValue("kind"), Name: r.PathValue("name")}
		if !validNamespace(ref.Namespace) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ref.Namespace)+" is not a namespace name.", nil)
			return
		}
		if !knownKind(ref.Kind) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ref.Kind)+" is not a kind of workload: "+
				"Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob or Pod.", nil)
			return
		}
		if !validName(ref.Name) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ref.Name)+" is not a workload name.", nil)
			return
		}
		s, err := readSnapshot(r, h, cluster, ref.Namespace, wants{pods: true, owners: true, exposure: true})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out := Detail{Cluster: cluster, Read: x.clock().UTC().Format(time.RFC3339), Workload: ref, Sources: s.sources,
			Containers: []Container{}, Exposure: []string{}, CVEs: []CVE{}, Checks: []Check{}, Secrets: []Secret{}}
		var ix *index
		if s.operator {
			l := link(s)
			ix = l.ix
			if via := l.expose(s, map[string]bool{ref.key(): true})[ref.key()]; via != nil {
				out.Exposure = via
			}
			if res := l.workloads[ref.key()]; res != nil {
				if err := summarise(r, h, cluster, s, res, &out); err != nil {
					writeFailure(w, cluster, err)
					return
				}
			}
		}
		pol, err := workloadPolicies(r, h, cluster, ref)
		switch {
		case errors.Is(err, extension.ErrUnknownCluster):
			writeFailure(w, cluster, err)
			return
		case err != nil:
			s.addNote("The policy reports could not be read: " + textutil.FirstLineN(err.Error(), 200))
		default:
			out.Policies = pol
		}
		if ix != nil {
			out.Signature = signatures(s, ix, signedOf(pol))[ref.key()]
		}
		out.Notes = s.notes
		if out.Notes == nil {
			out.Notes = []string{}
		}
		writeAnswer(w, out)
	})
}

// summarize reads the bodies of the reports the linkage chose for the
// workload, one kind at a time, and keeps a summary of each: the body is
// dropped as soon as it is read. A read of bodies that fails is its source's
// state; an unknown cluster is the request's error.
func summarise(r *http.Request, h extension.Host, cluster string, s *snapshot, res *result, out *Detail) error {
	for _, kind := range reportKinds {
		var c *chosen
		switch kind {
		case KindVulnerability:
			c = &res.vuln
		case KindConfigAudit:
			c = &res.config
		default:
			c = &res.secret
		}
		if len(c.reports) == 0 {
			continue
		}
		items, err := bodies(r, h, cluster, s.crds[kind], c.reports)
		if errors.Is(err, extension.ErrUnknownCluster) {
			return err
		}
		if err != nil {
			classify(s.source(kind), err)
			continue
		}
		switch kind {
		case KindVulnerability:
			summariseVulnerabilities(items, out)
		case KindConfigAudit:
			summariseChecks(items, out)
		default:
			summariseSecrets(items, out)
		}
	}
	return nil
}

// bodies reads the chosen reports whole, in their namespace: by the
// trivy-operator labels naming what they scanned, or, where a name too long
// for a label is only a hash there, each by its own name. Only the reports
// chosen are kept, in a stable order.
func bodies(r *http.Request, h extension.Host, cluster string, crd extension.CRD, reports []*reportRow) ([]unstructured.Unstructured, error) {
	names := make(map[string]bool, len(reports))
	for _, rep := range reports {
		names[rep.name] = true
	}
	first := reports[0]
	var queries []extension.ListQuery
	if kind, name := first.labels[labelKind], first.labels[labelName]; kind != "" && name != "" && first.labels[labelNameHash] == "" {
		queries = append(queries, extension.ListQuery{GVR: crd.GVR(), Namespace: first.namespace,
			LabelSelector: labelKind + "=" + kind + "," + labelName + "=" + name})
	} else {
		for _, rep := range reports {
			queries = append(queries, extension.ListQuery{GVR: crd.GVR(), Namespace: rep.namespace,
				FieldSelector: "metadata.name=" + rep.name})
		}
	}
	var out []unstructured.Unstructured
	for _, q := range queries {
		page, err := h.List(r, cluster, q)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			if names[page.Items[i].GetName()] {
				out = append(out, page.Items[i])
			}
		}
		page.Items = nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

func str(m map[string]any, path ...string) string {
	s, _, _ := unstructured.NestedString(m, path...)
	return s
}

func num(m map[string]any, path ...string) int {
	v, ok, _ := unstructured.NestedFieldNoCopy(m, path...)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func list(m map[string]any, path ...string) []any {
	v, ok, _ := unstructured.NestedFieldNoCopy(m, path...)
	if !ok {
		return nil
	}
	l, _ := v.([]any)
	return l
}

func summaryCounts(report map[string]any) Counts {
	return Counts{Critical: num(report, "summary", "criticalCount"), High: num(report, "summary", "highCount"),
		Medium: num(report, "summary", "mediumCount"), Low: num(report, "summary", "lowCount"),
		Unknown: num(report, "summary", "unknownCount")}
}

func clipTitle(s string) string { return textutil.FirstLineN(s, MaxTitle) }

func summariseVulnerabilities(items []unstructured.Unstructured, out *Detail) {
	for i := range items {
		u := &items[i]
		report, _ := u.Object["report"].(map[string]any)
		container := u.GetLabels()[labelContainer]
		c := Container{Container: container,
			Image:   imageName(str(report, "artifact", "repository"), str(report, "artifact", "tag")),
			OS:      strings.TrimSpace(str(report, "os", "family") + " " + str(report, "os", "name")),
			Scanned: str(report, "updateTimestamp"), Counts: summaryCounts(report)}
		if eosl, ok, _ := unstructured.NestedBool(report, "os", "eosl"); ok {
			c.EOSL = &eosl
		}
		out.Containers = append(out.Containers, c)
		for _, v := range list(report, "vulnerabilities") {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			out.CVEsTotal++
			cve := CVE{ID: str(m, "vulnerabilityID"), Severity: severity(str(m, "severity")), Pkg: str(m, "resource"),
				Installed: str(m, "installedVersion"), Fixed: str(m, "fixedVersion"), Title: clipTitle(str(m, "title")),
				Link: str(m, "primaryLink"), Container: container}
			if score, ok := m["score"].(float64); ok {
				cve.Score = &score
			} else if score, ok := m["score"].(int64); ok {
				f := float64(score)
				cve.Score = &f
			}
			out.CVEs = append(out.CVEs, cve)
		}
		// The body is summarized: nothing of it is kept past here.
		items[i].Object = nil
	}
	sort.SliceStable(out.CVEs, func(i, j int) bool {
		a, b := &out.CVEs[i], &out.CVEs[j]
		if severities[a.Severity] != severities[b.Severity] {
			return severities[a.Severity] < severities[b.Severity]
		}
		sa, sb := -1.0, -1.0
		if a.Score != nil {
			sa = *a.Score
		}
		if b.Score != nil {
			sb = *b.Score
		}
		if sa != sb {
			return sa > sb
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Container < b.Container
	})
	if len(out.CVEs) > MaxCVEs {
		out.CVEs = append([]CVE(nil), out.CVEs[:MaxCVEs]...)
	}
}

func summariseChecks(items []unstructured.Unstructured, out *Detail) {
	out.Checks = append(out.Checks, distinctChecks(items)...)
}

// distinctChecks is the failed checks of a workload's config audit reports,
// each check ID once: trivy reports some checks once for the pod and again
// for each container (AVD-KSV-0118), and its summary counts every one, so a
// workload would read three failures in one place and two in another. The
// bodies are dropped as they are read; the worst come first.
func distinctChecks(items []unstructured.Unstructured) []Check {
	seen := map[string]bool{}
	var checks []Check
	for i := range items {
		report, _ := items[i].Object["report"].(map[string]any)
		for _, v := range list(report, "checks") {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if success, _ := m["success"].(bool); success {
				continue
			}
			check := Check{ID: str(m, "checkID"), Title: clipTitle(str(m, "title")), Severity: severity(str(m, "severity")),
				Category: str(m, "category")}
			if seen[check.ID] {
				continue
			}
			seen[check.ID] = true
			checks = append(checks, check)
		}
		items[i].Object = nil
	}
	sort.SliceStable(checks, func(i, j int) bool {
		a, b := checks[i], checks[j]
		if severities[a.Severity] != severities[b.Severity] {
			return severities[a.Severity] < severities[b.Severity]
		}
		return a.ID < b.ID
	})
	return checks
}

// checkCounts is distinct failed checks counted by severity.
func checkCounts(checks []Check) Counts {
	var c Counts
	for _, k := range checks {
		switch k.Severity {
		case "CRITICAL":
			c.Critical++
		case "HIGH":
			c.High++
		case "MEDIUM":
			c.Medium++
		case "LOW":
			c.Low++
		default:
			c.Unknown++
		}
	}
	return c
}

func summariseSecrets(items []unstructured.Unstructured, out *Detail) {
	for i := range items {
		report, _ := items[i].Object["report"].(map[string]any)
		for _, v := range list(report, "secrets") {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			// `match` is never read: the rule, the file and the severity are
			// what to act on, and the matched text is the secret.
			out.Secrets = append(out.Secrets, Secret{Rule: str(m, "ruleID"), Title: clipTitle(str(m, "title")),
				Category: str(m, "category"), Severity: severity(str(m, "severity")), Target: str(m, "target")})
		}
		items[i].Object = nil
	}
	sort.SliceStable(out.Secrets, func(i, j int) bool {
		a, b := out.Secrets[i], out.Secrets[j]
		if severities[a.Severity] != severities[b.Severity] {
			return severities[a.Severity] < severities[b.Severity]
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Rule < b.Rule
	})
}
