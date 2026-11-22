package security

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// What the admission wrote about a pod's images. Kyverno's verifyImages rules
// annotate every pod they checked with a JSON object, image → `pass`, `fail`
// or `skip` (api/kyverno/constants.go, pkg/engine/api/imageverifymetadata.go
// at v1.20.0); older releases wrote `true` and `false`. The annotation is
// metadata, so it arrives in the pods' table read: reading it costs nothing.
//
// Nothing here verifies a signature: the console never asks a registry or a
// transparency log. It shows the verdict the admission already gave, and a pod
// with no annotation has no verdict - unknown, never "not signed".
const (
	annotationVerify       = "kyverno.io/verify-images"
	annotationVerifyScoped = "kyverno.io/verify-images-scoped"
)

// The verdicts, as Kyverno spells them, and the one this extra adds for an
// annotation it could not read.
const (
	VerdictPass       = "pass"
	VerdictFail       = "fail"
	VerdictSkip       = "skip"
	VerdictUnreadable = "unreadable"
)

// KindSignatures names the signatures' source: not a kind of report, the
// annotation read on the pods and the image verifications of the policy
// reports.
const KindSignatures = "signatures"

const (
	// MaxAnnotation is the most bytes of an annotation read; a longer one is
	// unreadable rather than parsed.
	MaxAnnotation = 16 << 10
	// MaxSignatureImages is the most images one workload's verdict lists.
	MaxSignatureImages = 50
	// maxImageName clips an image's name as the annotation spells it.
	maxImageName = 512
)

// verdicts is one pod's annotation, read: image → verdict, or unreadable.
type verdicts struct {
	images     map[string]string
	unreadable bool
}

// verifiedOf reads a pod's annotation; nil when there is none. The scoped
// annotation is read only when the plain one is absent.
func verifiedOf(annotations map[string]string) *verdicts {
	raw, ok := annotations[annotationVerify]
	if !ok {
		if raw, ok = annotations[annotationVerifyScoped]; !ok {
			return nil
		}
	}
	if len(raw) > MaxAnnotation {
		return &verdicts{unreadable: true}
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return &verdicts{unreadable: true}
	}
	v := &verdicts{images: map[string]string{}}
	collect(data, v.images, 0)
	if len(v.images) == 0 {
		v.unreadable = true
	}
	return v
}

// collect keeps image → verdict, one level of nesting deep: the scoped
// annotation groups the same object under each policy and rule.
func collect(data map[string]any, into map[string]string, depth int) {
	for k, val := range data {
		switch x := val.(type) {
		case string:
			if verdict := verdictOf(x); verdict != "" {
				into[clipImage(k)] = worstVerdict(into[clipImage(k)], verdict)
			}
		case bool:
			verdict := VerdictFail
			if x {
				verdict = VerdictPass
			}
			into[clipImage(k)] = worstVerdict(into[clipImage(k)], verdict)
		case map[string]any:
			if depth == 0 {
				collect(x, into, depth+1)
			}
		}
	}
}

func clipImage(s string) string {
	if len(s) > maxImageName {
		return s[:maxImageName]
	}
	return s
}

func verdictOf(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case VerdictPass, "true":
		return VerdictPass
	case VerdictFail, "false":
		return VerdictFail
	case VerdictSkip:
		return VerdictSkip
	}
	return ""
}

// verdictRank orders the verdicts, the worst highest.
var verdictRank = map[string]int{"": 0, VerdictPass: 1, VerdictSkip: 2, VerdictFail: 3}

func worstVerdict(a, b string) string {
	if verdictRank[b] > verdictRank[a] {
		return b
	}
	return a
}

// Signature is what the admission wrote about a workload's images, across
// its pods and the policy reports about it and its pods: an image's verdict
// is the worst any carries. Verdict is
// `fail` when any image failed, `skip` when none failed and one was skipped,
// `pass` when every image passed, and `unreadable` when every annotation was.
// A workload with no annotation and no image verification in a report has
// no Signature at all.
type Signature struct {
	Verdict string         `json:"verdict"`
	Images  []ImageVerdict `json:"images"`
	// Unreadable is how many of its pods carried an annotation that could not
	// be read.
	Unreadable int `json:"unreadable,omitempty"`
}

// ImageVerdict is one image, as the annotation names it.
type ImageVerdict struct {
	Image   string `json:"image"`
	Verdict string `json:"verdict"`
}

// signatures folds the pods' annotations, and the image verifications rv
// holds from the policy reports, onto their workloads: an image's verdict is
// the worst either carries.
func signatures(s *snapshot, ix *index, rv *reportVerdicts) map[string]*Signature {
	byWorkload := map[string]map[string]string{}
	unreadable := map[string]int{}
	merge := func(key string, from map[string]string) {
		images := byWorkload[key]
		if images == nil {
			images = map[string]string{}
			byWorkload[key] = images
		}
		for image, verdict := range from {
			images[image] = worstVerdict(images[image], verdict)
		}
	}
	for i := range s.pods {
		p := &s.pods[i]
		if p.verified == nil {
			continue
		}
		key := ix.pods[p.namespace+"/"+p.name].key()
		if p.verified.unreadable {
			unreadable[key]++
			continue
		}
		merge(key, p.verified.images)
	}
	if rv != nil {
		rv.mu.Lock()
		for res, images := range rv.by {
			merge(workloadOfResource(ix, res).key(), images)
		}
		rv.mu.Unlock()
	}
	out := make(map[string]*Signature, len(byWorkload)+len(unreadable))
	for key, images := range byWorkload {
		sig := &Signature{Images: make([]ImageVerdict, 0, len(images)), Unreadable: unreadable[key]}
		for image, verdict := range images {
			sig.Verdict = worstVerdict(sig.Verdict, verdict)
			sig.Images = append(sig.Images, ImageVerdict{Image: image, Verdict: verdict})
		}
		sort.Slice(sig.Images, func(i, j int) bool {
			a, b := sig.Images[i], sig.Images[j]
			if a.Verdict != b.Verdict {
				return verdictRank[a.Verdict] > verdictRank[b.Verdict]
			}
			return a.Image < b.Image
		})
		if len(sig.Images) > MaxSignatureImages {
			sig.Images = sig.Images[:MaxSignatureImages]
		}
		out[key] = sig
	}
	for key, n := range unreadable {
		if out[key] == nil {
			out[key] = &Signature{Verdict: VerdictUnreadable, Images: []ImageVerdict{}, Unreadable: n}
		}
	}
	return out
}

// The verdict as Kyverno 1.20 and later records it. An ImageValidatingPolicy
// writes no annotation on the pod, in Deny or in Audit: its verdict is a
// result in the policy reports, one report per resource (the Pod, its
// ReplicaSet, its Deployment), a pass included. The image is the one the
// message names (`image "<ref>"`); a result that names none is listed under
// its policy's name.
const imageVerificationSource = "kyvernoimagevalidatingpolicy"

var imageInMessage = regexp.MustCompile(`image "([^"]+)"`)

// reportVerdicts is the image verifications read from the policy reports, by
// the resource each report is about: image → verdict, the worst kept.
type reportVerdicts struct {
	mu sync.Mutex
	by map[resource]map[string]string
}

func newReportVerdicts() *reportVerdicts {
	return &reportVerdicts{by: map[resource]map[string]string{}}
}

// add keeps one report result when it is an image verification.
func (v *reportVerdicts) add(res resource, m map[string]any) {
	if res.namespace == "" || res.name == "" || !strings.EqualFold(strings.TrimSpace(str(m, "source")), imageVerificationSource) {
		return
	}
	verdict := ""
	switch strings.ToLower(strings.TrimSpace(str(m, "result"))) {
	case "pass":
		verdict = VerdictPass
	case "fail", "error":
		verdict = VerdictFail
	case "skip":
		verdict = VerdictSkip
	}
	if verdict == "" {
		return
	}
	var images []string
	for _, match := range imageInMessage.FindAllStringSubmatch(str(m, "message"), MaxSignatureImages) {
		images = append(images, clipImage(match[1]))
	}
	if len(images) == 0 {
		policy := str(m, "policy")
		if policy == "" {
			return
		}
		images = []string{clipImage(policy)}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	known := v.by[res]
	if known == nil {
		known = map[string]string{}
		v.by[res] = known
	}
	for _, image := range images {
		keepVerdict(known, image, verdict)
	}
}

// keepVerdict adds an image's verdict to at most MaxSignatureImages: past
// it, a worse verdict takes the place of the mildest one kept.
func keepVerdict(images map[string]string, image, verdict string) {
	if _, ok := images[image]; ok || len(images) < MaxSignatureImages {
		images[image] = worstVerdict(images[image], verdict)
		return
	}
	mildest, rank := "", verdictRank[verdict]
	for name, v := range images {
		if verdictRank[v] < rank {
			mildest, rank = name, verdictRank[v]
		}
	}
	if mildest != "" {
		delete(images, mildest)
		images[image] = verdict
	}
}

// signedTasks reads the namespaced policy reports of both groups for their
// image verifications alone, each walked through Each and dropped as it is
// read. A kind not served is absent and costs no read.
func signedTasks(r *http.Request, h extension.Host, cluster, namespace string, registry extension.CRDRegistry,
	s *snapshot, setFatal func(error)) []task {
	s.signed = newReportVerdicts()
	for _, rs := range reportSources {
		if rs.namespaced {
			s.signedFrom = append(s.signedFrom, Source{Kind: rs.kind})
		}
	}
	var tasks []task
	for i := range s.signedFrom {
		src := &s.signedFrom[i]
		crd, err := registry.Lookup(r.Context(), src.Kind)
		switch {
		case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed):
			src.State = StateAbsent
			continue
		case err != nil:
			src.State, src.Note = StateUnreadable, "Resolving "+src.Kind+" failed: "+textutil.FirstLineN(err.Error(), 200)
			continue
		}
		tasks = append(tasks, task{
			run: func() error {
				n := 0
				page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, Limit: MaxPolicyReports,
					Each: func(u unstructured.Unstructured) error {
						n++
						res, _ := scopeOf(&u)
						for _, v := range list(u.Object, "results") {
							if m, ok := v.(map[string]any); ok {
								s.signed.add(res, m)
							}
						}
						return nil
					}})
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
	return tasks
}

// readOutcome is how one table read beside the reports went.
type readOutcome struct {
	complete bool
	note     string
	err      error
}

// outcome keeps how the pods' read went, which the signatures' source says.
func (s *snapshot) outcome(what string, complete bool, note string, err error) {
	if what != "Pods" {
		return
	}
	s.mu.Lock()
	s.podsRead = &readOutcome{complete: complete, note: note, err: err}
	s.mu.Unlock()
}

// signatureSource is the signatures' source: read when at least one pod
// carries the annotation or a policy report carries an image verification,
// absent when neither does - the page then draws `—`, never "not signed" -,
// refused or unreadable as the pods' or the reports' read was when nothing
// was found, and unreadable when every annotation found was. Reports counts
// the pods with a verdict, from either.
func signatureSource(s *snapshot) Source {
	src := Source{Kind: KindSignatures, State: StateAbsent}
	complete, failed := true, ""
	var partial, failures []string
	fail := func(state, note string) {
		complete = false
		failures = append(failures, note)
		if failed == "" {
			failed = state
		}
	}
	pods := map[string]bool{}
	unreadable := 0
	if s.podsRead != nil {
		switch err := s.podsRead.err; {
		case errors.Is(err, extension.ErrDenied):
			fail(StateDenied, "You may not list pods here.")
		case errors.Is(err, extension.ErrUnreviewable):
			fail(StateDenied, extension.UnreviewedText)
		case err != nil:
			fail(StateUnreadable, "Pods could not be read.")
		default:
			for i := range s.pods {
				switch p := &s.pods[i]; {
				case p.verified == nil:
				case p.verified.unreadable:
					unreadable++
				default:
					pods[p.namespace+"/"+p.name] = true
				}
			}
			if !s.podsRead.complete {
				complete = false
				if s.podsRead.note != "" {
					partial = append(partial, s.podsRead.note)
				}
			}
		}
	}
	for _, rs := range s.signedFrom {
		switch rs.State {
		case StateRead:
			if !rs.Complete {
				complete = false
				if rs.Note != "" {
					partial = append(partial, rs.Kind+": "+rs.Note)
				}
			}
		case StateDenied, StateUnreadable:
			fail(rs.State, rs.Note)
		}
	}
	found := len(pods) > 0
	if s.signed != nil {
		s.signed.mu.Lock()
		found = found || len(s.signed.by) > 0
		for res := range s.signed.by {
			if res.kind == "Pod" {
				pods[res.namespace+"/"+res.name] = true
			}
		}
		s.signed.mu.Unlock()
	}
	switch {
	case !found && unreadable == 0:
		if failed != "" {
			src.State, src.Note = failed, strings.Join(failures, " · ")
		}
		return src
	case !found:
		src.State = StateUnreadable
	default:
		src.State = StateRead
	}
	src.Reports = len(pods)
	src.Complete = complete && unreadable == 0
	var notes []string
	if unreadable > 0 {
		notes = append(notes, strconv.Itoa(unreadable)+" pod annotations unreadable")
	}
	notes = append(append(notes, partial...), failures...)
	src.Note = strings.Join(notes, " · ")
	return src
}
