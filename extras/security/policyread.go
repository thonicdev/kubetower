package security

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The policy sources: the PolicyReport schema in its two groups, Gatekeeper's
// constraints, and Kyverno's policies, read for their mode only. A Source's
// Kind is the qualified plural, or `constraints.gatekeeper.sh` for every
// constraint kind together.
const (
	KindPolicyReports        = "policyreports.wgpolicyk8s.io"
	KindClusterPolicyReports = "clusterpolicyreports.wgpolicyk8s.io"
	KindOpenReports          = "reports.openreports.io"
	KindClusterOpenReports   = "clusterreports.openreports.io"
	KindGatekeeper           = "constraints.gatekeeper.sh"
	KindKyvernoPolicies      = "policies.kyverno.io"
	KindKyvernoCluster       = "clusterpolicies.kyverno.io"
	KindKyvernoValidating    = "validatingpolicies.policies.kyverno.io"
	KindKyvernoImage         = "imagevalidatingpolicies.policies.kyverno.io"
	KindKyvernoNsValidating  = "namespacedvalidatingpolicies.policies.kyverno.io"
	KindKyvernoNsImage       = "namespacedimagevalidatingpolicies.policies.kyverno.io"
)

// reportSources are the report kinds, read through Each; namespaced first.
var reportSources = []struct {
	kind       string
	namespaced bool
}{
	{KindPolicyReports, true}, {KindOpenReports, true},
	{KindClusterPolicyReports, false}, {KindClusterOpenReports, false},
}

// kyvernoSources are Kyverno's policies, read for their mode.
var kyvernoSources = []string{KindKyvernoCluster, KindKyvernoPolicies, KindKyvernoValidating, KindKyvernoImage,
	KindKyvernoNsValidating, KindKyvernoNsImage}

// celKinds are Kyverno's CEL policies, whose mode is spec.validationActions.
var celKinds = map[string]bool{KindKyvernoValidating: true, KindKyvernoImage: true, KindKyvernoNsValidating: true,
	KindKyvernoNsImage: true}

// The bounds of one policy read. Each that cuts something is said.
const (
	// MaxPolicyReports is the most reports walked of one kind: past it the
	// source is partial.
	MaxPolicyReports = 20000
	// MaxPolicyResults is the most non-passing results kept, the worst first;
	// every one is still counted.
	MaxPolicyResults = 50000
	// MaxConstraintKinds is the most Gatekeeper constraint kinds read.
	MaxConstraintKinds = 200
	// MaxKyvernoPolicies is the most Kyverno policies read of one kind.
	MaxKyvernoPolicies = 1000
	// MaxPolicyMessage is the most characters of a result's message kept.
	MaxPolicyMessage = 160
)

// PolicyCounts is non-passing results by kind of outcome; Audit is the
// failures a policy lets through because it only audits.
type PolicyCounts struct {
	Fail  int `json:"fail"`
	Warn  int `json:"warn"`
	Error int `json:"error"`
	Audit int `json:"audit"`
}

func (c *PolicyCounts) add(o PolicyCounts) {
	c.Fail, c.Warn, c.Error, c.Audit = c.Fail+o.Fail, c.Warn+o.Warn, c.Error+o.Error, c.Audit+o.Audit
}

func (c PolicyCounts) any() bool { return c.Fail+c.Warn+c.Error > 0 }

// result is one non-passing result, summarized: never `properties`, a
// message clipped.
type policyResult struct {
	engine, policy, rule string
	outcome, severity    string
	category, message    string
	res                  resource
	audit                bool
}

// resource is what a result is about.
type resource struct {
	namespace, kind, name string
}

func (r resource) key() string { return r.namespace + "/" + r.kind + "/" + r.name }

// policyKey names a policy across engines.
func policyKey(engine, name string) string { return engine + "/" + name }

// modes is each Kyverno policy's mode, and each of its rules', as read. A
// namespaced policy is keyed namespace/name.
type modes struct {
	policy map[string]string // policy name → Enforce, Audit or Mixed
	rule   map[string]string // policy/rule → Enforce or Audit
}

// name is the key a result's policy is read under: as the result names it
// when a policy is kept under that name, else a namespaced policy of that
// name in the result's namespace.
func (m modes) name(namespace, policy string) string {
	if _, ok := m.policy[policy]; ok || namespace == "" {
		return policy
	}
	return namespace + "/" + policy
}

// of is a result's mode: the rule's, else the policy's, else "".
func (m modes) of(namespace, policy, rule string) string {
	policy = m.name(namespace, policy)
	if v := m.rule[policy+"/"+rule]; v != "" {
		return v
	}
	return m.policy[policy]
}

// policyOf is a policy's own mode, as a result in namespace names it.
func (m modes) policyOf(namespace, policy string) string { return m.policy[m.name(namespace, policy)] }

// policyRead is everything one read of the policy sources kept.
type policyRead struct {
	sources []Source
	// results are the non-passing results kept, the worst first once sorted;
	// total is how many there were.
	results []policyResult
	total   int
	// counts and resources are over every result, kept or not.
	byPolicy   map[string]*policyAgg
	byResource map[string]*resourceAgg
	modes      modes
	// owners ties a resource to its workload; fold is whether every read of
	// it answered, so a result is counted once per workload.
	owners *index
	fold   bool
	seen   *seenResults
	// signed is the image verifications the reports carry, passes included
	// (signature.go).
	signed *reportVerdicts
	notes  []string
	mu     sync.Mutex
}

type policyAgg struct {
	engine, name, kind, mode, category, severity string
	counts                                       PolicyCounts
	resources                                    map[string]struct{}
	// listed is how many violations Gatekeeper wrote, when it cut them.
	listed, violations int
}

type resourceAgg struct {
	res      resource
	counts   PolicyCounts
	policies map[string]struct{}
}

func (p *policyRead) addNote(n string) {
	p.mu.Lock()
	p.notes = append(p.notes, n)
	p.mu.Unlock()
}

// policyFilter narrows a read: one namespace, one policy.
type policyFilter struct {
	namespace      string
	engine, policy string
	// noCluster skips the cluster-scoped reports, which no workload has.
	noCluster bool
}

// readPolicies reads every policy source, reports through Each and summarized
// as they come, Gatekeeper's constraints and Kyverno's policies whole and
// bounded. An unknown cluster or an unreadable catalog is the request's
// error; anything else is a source's state or a note.
func readPolicies(r *http.Request, h extension.Host, cluster string, f policyFilter) (*policyRead, error) {
	registry, err := h.Definitions(r, cluster)
	if errors.Is(err, extension.ErrUnknownCluster) {
		return nil, err
	}
	if err != nil {
		return nil, catalogueError{err}
	}
	p := &policyRead{byPolicy: map[string]*policyAgg{}, byResource: map[string]*resourceAgg{},
		modes: modes{policy: map[string]string{}, rule: map[string]string{}}, signed: newReportVerdicts()}
	var fatal error
	var fatalMu sync.Mutex
	setFatal := func(err error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = err
		}
		fatalMu.Unlock()
	}
	lookup := func(src *Source) (extension.CRD, bool) {
		crd, err := registry.Lookup(r.Context(), src.Kind)
		switch {
		case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed):
			src.State = StateAbsent
			return crd, false
		case err != nil:
			src.State, src.Note = StateUnreadable, "Resolving "+src.Kind+" failed: "+textutil.FirstLineN(err.Error(), 200)
			return crd, false
		}
		return crd, true
	}

	// The report kinds resolved first: the owners are read only where a
	// report or a constraint may need them.
	reports := make([]Source, len(reportSources))
	var reportCRDs []extension.CRD
	for i, rs := range reportSources {
		src := &reports[i]
		*src = Source{Kind: rs.kind}
		if !rs.namespaced && (f.noCluster || f.namespace != "") {
			src.State = StateAbsent
			reportCRDs = append(reportCRDs, extension.CRD{})
			continue
		}
		crd, ok := lookup(src)
		if !ok {
			crd = extension.CRD{}
		}
		reportCRDs = append(reportCRDs, crd)
	}
	wantOwners := false
	for i := range reports {
		wantOwners = wantOwners || reportCRDs[i].Plural != ""
	}
	if !wantOwners && (f.engine == "" || f.engine == engineGatekeeper) {
		if all, err := registry.All(r.Context()); err == nil {
			for _, c := range all {
				wantOwners = wantOwners || c.Group == KindGatekeeper
			}
		}
	}

	// Kyverno's modes first: a result is counted as audit or not as it is
	// read, so the modes must be known before any report is walked. The
	// owners beside them, for the same reason.
	kyverno := make([]Source, len(kyvernoSources))
	var tasks []task
	p.owners = newIndex(&snapshot{})
	if wantOwners {
		tasks = append(tasks, task{
			run: func() error {
				ix, notes := policyIndex(r, h, cluster, f.namespace)
				p.mu.Lock()
				p.owners, p.fold = ix, len(notes) == 0
				p.notes = append(p.notes, notes...)
				p.mu.Unlock()
				return nil
			},
			fail: func(error) {},
		})
	}
	for i, kind := range kyvernoSources {
		src := &kyverno[i]
		*src = Source{Kind: kind}
		crd, ok := lookup(src)
		if !ok {
			continue
		}
		tasks = append(tasks, task{
			run: func() error {
				page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: MaxKyvernoPolicies})
				if err != nil {
					return err
				}
				p.mu.Lock()
				for i := range page.Items {
					p.modes.read(&page.Items[i], kind)
					page.Items[i].Object = nil
				}
				p.mu.Unlock()
				src.State, src.Complete, src.Note, src.Reports = StateRead, page.Complete, page.Note, len(page.Items)
				return nil
			},
			fail: func(err error) {
				if errors.Is(err, extension.ErrUnknownCluster) {
					setFatal(err)
				}
				classify(src, err)
			},
		})
	}
	runAll(tasks)
	if fatal != nil {
		return nil, fatal
	}

	// The reports, one walk per kind, at most four at once. Results are
	// summarized as each report is handed over and the report dropped.
	seen := newSeen()
	p.seen = seen
	tasks = nil
	for i := range reportSources {
		src, crd := &reports[i], reportCRDs[i]
		if crd.Plural == "" {
			continue
		}
		tasks = append(tasks, task{
			run: func() error {
				n := 0
				q := extension.ListQuery{GVR: crd.GVR(), Namespace: f.namespace, Limit: MaxPolicyReports,
					Each: func(u unstructured.Unstructured) error {
						n++
						p.report(&u, seen, f)
						return nil
					}}
				page, err := h.List(r, cluster, q)
				if err != nil {
					return err
				}
				src.State, src.Complete, src.Note, src.Reports = StateRead, page.Complete, page.Note, n
				return nil
			},
			fail: func(err error) {
				if errors.Is(err, extension.ErrUnknownCluster) {
					setFatal(err)
				}
				classify(src, err)
			},
		})
	}

	// Gatekeeper: every constraint kind, each read whole - tens of objects
	// whose violations Gatekeeper itself caps.
	gk := Source{Kind: KindGatekeeper}
	if f.engine == "" || f.engine == engineGatekeeper {
		p.gatekeeper(r, h, cluster, registry, &gk, f, &tasks, setFatal)
	} else {
		gk.State = StateAbsent
	}
	runAll(tasks)
	if fatal != nil {
		return nil, fatal
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	p.sources = append(append(append([]Source{}, reports...), gk), kyverno...)
	p.finish()
	return p, nil
}

// read keeps one Kyverno policy's mode and its rules'.
func (m modes) read(u *unstructured.Unstructured, kind string) {
	name := u.GetName()
	if u.GetNamespace() != "" {
		name = u.GetNamespace() + "/" + name
	}
	spec, _ := u.Object["spec"].(map[string]any)
	if celKinds[kind] {
		mode := ""
		for _, a := range list(spec, "validationActions") {
			switch s, _ := a.(string); strings.ToLower(s) {
			case "deny":
				mode = "Enforce"
			case "audit", "warn":
				if mode == "" {
					mode = "Audit"
				}
			}
		}
		m.policy[name] = mode
		return
	}
	def := normalMode(str(spec, "validationFailureAction"))
	seen := map[string]bool{}
	for _, v := range list(spec, "rules") {
		rule, _ := v.(map[string]any)
		mode := normalMode(str(rule, "validate", "failureAction"))
		if mode == "" {
			mode = def
		}
		if _, ok := rule["validate"]; !ok {
			continue
		}
		m.rule[name+"/"+str(rule, "name")] = mode
		// Kyverno's autogen copies a pod rule for each controller.
		m.rule[name+"/autogen-"+str(rule, "name")] = mode
		m.rule[name+"/autogen-cronjob-"+str(rule, "name")] = mode
		seen[mode] = true
	}
	switch {
	case len(seen) > 1:
		m.policy[name] = "Mixed"
	case len(seen) == 1:
		for mode := range seen {
			m.policy[name] = mode
		}
	default:
		m.policy[name] = def
	}
}

func normalMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "enforce":
		return "Enforce"
	case "audit":
		return "Audit"
	}
	return ""
}

// seenResults dedupes a result the two report groups both carry, during a
// migration from one to the other, and a result Kyverno writes again on each
// of one workload's objects.
type seenResults struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newSeen() *seenResults { return &seenResults{keys: map[string]struct{}{}} }

func (s *seenResults) first(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[key]; ok {
		return false
	}
	s.keys[key] = struct{}{}
	return true
}

// scopeOf is the resource a PolicyReport (either group) is about, and its
// UID, or its key where the report names none.
func scopeOf(u *unstructured.Unstructured) (resource, string) {
	scope, _ := u.Object["scope"].(map[string]any)
	res := resource{namespace: str(scope, "namespace"), kind: str(scope, "kind"), name: str(scope, "name")}
	uid := str(scope, "uid")
	if res.kind == "" {
		// A report with no scope is about its owner.
		if refs := u.GetOwnerReferences(); len(refs) > 0 {
			res.kind, res.name, uid = refs[0].Kind, refs[0].Name, string(refs[0].UID)
		}
	}
	if res.namespace == "" {
		res.namespace = u.GetNamespace()
	}
	if uid == "" {
		uid = res.key()
	}
	return res, uid
}

// report summarizes one PolicyReport (either group) as it is handed over.
func (p *policyRead) report(u *unstructured.Unstructured, seen *seenResults, f policyFilter) {
	res, uid := scopeOf(u)
	about := res
	// Counted once per workload where the owners were read, else per object.
	id := uid
	if res = p.workloadOf(res); p.fold {
		id = res.key()
	}
	for _, v := range list(u.Object, "results") {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if p.signed != nil {
			p.signed.add(about, m)
		}
		outcome := strings.ToLower(str(m, "result"))
		if outcome != "fail" && outcome != "warn" && outcome != "error" {
			continue
		}
		engine := engineOf(str(m, "source"))
		policy, rule := str(m, "policy"), str(m, "rule")
		if f.engine != "" && (engine != f.engine || policy != f.policy) {
			continue
		}
		if !seen.first(id + "\x00" + engine + "\x00" + policy + "\x00" + baseRule(rule)) {
			continue
		}
		rr := policyResult{engine: engine, policy: policy, rule: rule, outcome: outcome,
			severity: policySeverity(str(m, "severity")), category: str(m, "category"),
			message: textutil.FirstLineN(str(m, "message"), MaxPolicyMessage), res: res}
		rr.audit = outcome == "fail" && engine == engineKyverno && p.modes.of(res.namespace, policy, rule) == "Audit"
		p.keep(rr, "")
	}
	u.Object = nil
}

const (
	engineKyverno    = "kyverno"
	engineGatekeeper = "gatekeeper"
)

// engineOf is the engine a result's source names: every Kyverno source
// (kyverno, KyvernoValidatingPolicy, KyvernoImageValidatingPolicy…) is
// kyverno.
func engineOf(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	switch {
	case s == "":
		return "unknown"
	case strings.HasPrefix(s, engineKyverno):
		return engineKyverno
	}
	return s
}

// baseRule is a rule without the prefix Kyverno's autogen gives the copy it
// writes for a controller.
func baseRule(rule string) string {
	for _, p := range []string{"autogen-cronjob-", "autogen-"} {
		if strings.HasPrefix(rule, p) {
			return strings.TrimPrefix(rule, p)
		}
	}
	return rule
}

// workloadOf is the resource a result counts against: its workload where
// every owner read answered, else itself.
func (p *policyRead) workloadOf(res resource) resource {
	if !p.fold || res.namespace == "" || res.name == "" {
		return res
	}
	w := workloadOfResource(p.owners, res)
	return resource{namespace: w.Namespace, kind: w.Kind, name: w.Name}
}

// policySeverity is the PolicyReport severity, in the order findings are
// sorted; "" when the result gives none.
func policySeverity(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "critical", "high", "medium", "low", "info":
		return s
	}
	return ""
}

var policySeverities = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4, "": 5}
var outcomes = map[string]int{"error": 0, "fail": 1, "warn": 2}

// keep counts one result and keeps its summary while under the bound.
func (p *policyRead) keep(rr policyResult, kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	pk := policyKey(rr.engine, rr.policy)
	agg := p.byPolicy[pk]
	if agg == nil {
		agg = &policyAgg{engine: rr.engine, name: rr.policy, kind: kind, resources: map[string]struct{}{}}
		if rr.engine == engineKyverno {
			agg.mode = p.modes.policyOf(rr.res.namespace, rr.policy)
		}
		p.byPolicy[pk] = agg
	}
	agg.counts.add(c)
	if agg.category == "" {
		agg.category = rr.category
	}
	if agg.severity == "" || policySeverities[rr.severity] < policySeverities[agg.severity] {
		agg.severity = rr.severity
	}
	if rr.res.name != "" {
		agg.resources[rr.res.key()] = struct{}{}
		ra := p.byResource[rr.res.key()]
		if ra == nil {
			ra = &resourceAgg{res: rr.res, policies: map[string]struct{}{}}
			p.byResource[rr.res.key()] = ra
		}
		ra.counts.add(c)
		ra.policies[pk] = struct{}{}
	}
	p.total++
	p.results = append(p.results, rr)
	if len(p.results) >= 2*MaxPolicyResults {
		sortResults(p.results)
		p.results = append([]policyResult(nil), p.results[:MaxPolicyResults]...)
	}
}

// sortResults puts the worst first: error, fail, warn; then severity.
func sortResults(rs []policyResult) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if outcomes[a.outcome] != outcomes[b.outcome] {
			return outcomes[a.outcome] < outcomes[b.outcome]
		}
		if policySeverities[a.severity] != policySeverities[b.severity] {
			return policySeverities[a.severity] < policySeverities[b.severity]
		}
		if a.policy != b.policy {
			return a.policy < b.policy
		}
		return a.res.key() < b.res.key()
	})
}

func (p *policyRead) finish() {
	sortResults(p.results)
	if len(p.results) > MaxPolicyResults {
		p.results = p.results[:MaxPolicyResults:MaxPolicyResults]
	}
}

// gatekeeper adds a task per constraint kind: each constraint's mode and
// total from its spec and status, and each violation Gatekeeper wrote as a
// result. Gatekeeper writes at most its own limit of violations a
// constraint; totalViolations stays exact, and both are kept.
func (p *policyRead) gatekeeper(r *http.Request, h extension.Host, cluster string, registry extension.CRDRegistry,
	src *Source, f policyFilter, tasks *[]task, setFatal func(error)) {
	all, err := registry.All(r.Context())
	if err != nil {
		src.State, src.Note = StateUnreadable, "Listing the constraint kinds failed: "+textutil.FirstLineN(err.Error(), 200)
		return
	}
	var kinds []extension.CRD
	for _, c := range all {
		if c.Group == KindGatekeeper {
			kinds = append(kinds, c)
		}
	}
	if len(kinds) == 0 {
		src.State = StateAbsent
		return
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].Plural < kinds[j].Plural })
	src.State, src.Complete = StateRead, true
	if len(kinds) > MaxConstraintKinds {
		kinds = kinds[:MaxConstraintKinds]
		src.Complete, src.Note = false, "Only the first constraint kinds were read."
	}
	var mu sync.Mutex
	for _, crd := range kinds {
		*tasks = append(*tasks, task{
			run: func() error {
				page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR()})
				if err != nil {
					return err
				}
				mu.Lock()
				src.Reports += len(page.Items)
				if !page.Complete {
					src.Complete, src.Note = false, page.Note
				}
				mu.Unlock()
				for i := range page.Items {
					p.constraint(&page.Items[i], crd.Kind, f)
					page.Items[i].Object = nil
				}
				return nil
			},
			fail: func(err error) {
				if errors.Is(err, extension.ErrUnknownCluster) {
					setFatal(err)
					return
				}
				mu.Lock()
				if src.State == StateRead {
					src.Complete = false
				}
				mu.Unlock()
				p.addNote(crd.Kind + " " + failedWord(err) + ": " + textutil.FirstLineN(err.Error(), 200))
			},
		})
	}
}

// constraint reads one Gatekeeper constraint.
func (p *policyRead) constraint(u *unstructured.Unstructured, kind string, f policyFilter) {
	name := strings.ToLower(kind) + "." + u.GetName()
	if f.engine != "" && name != f.policy {
		return
	}
	spec, _ := u.Object["spec"].(map[string]any)
	status, _ := u.Object["status"].(map[string]any)
	action := strings.ToLower(str(spec, "enforcementAction"))
	if action == "" {
		action = "deny"
	}
	total := num(status, "totalViolations")
	listed := 0
	for _, v := range list(status, "violations") {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		res := resource{namespace: str(m, "namespace"), kind: str(m, "kind"), name: str(m, "name")}
		if f.namespace != "" && res.namespace != f.namespace {
			continue
		}
		listed++
		msg := textutil.FirstLineN(str(m, "message"), MaxPolicyMessage)
		if res = p.workloadOf(res); p.fold && !p.seen.first(res.key()+"\x00"+engineGatekeeper+"\x00"+name+"\x00"+msg) {
			continue
		}
		va := strings.ToLower(str(m, "enforcementAction"))
		if va == "" {
			va = action
		}
		outcome := "fail"
		if va == "warn" {
			outcome = "warn"
		}
		p.keep(policyResult{engine: engineGatekeeper, policy: name, outcome: outcome, severity: "",
			message: msg, res: res,
			audit: va == "dryrun" || va == "warn"}, kind)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	agg := p.byPolicy[policyKey(engineGatekeeper, name)]
	if agg == nil {
		agg = &policyAgg{engine: engineGatekeeper, name: name, kind: kind, resources: map[string]struct{}{}}
		p.byPolicy[policyKey(engineGatekeeper, name)] = agg
	}
	agg.mode = action
	agg.listed, agg.violations = listed, total
	if f.namespace == "" && total > listed {
		// The violations Gatekeeper counted and did not write still count.
		extra := total - listed
		if action == "warn" {
			agg.counts.Warn += extra
		} else {
			agg.counts.Fail += extra
		}
		if action == "dryrun" || action == "warn" {
			agg.counts.Audit += extra
		}
	}
}
