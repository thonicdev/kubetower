package security

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

func annotated(o *fakeObj, key, value string) *fakeObj {
	if o.annotations == nil {
		o.annotations = map[string]string{}
	}
	o.annotations[key] = value
	return o
}

func TestTheAnnotationIsReadAsVerdictsAndNeverGuessed(t *testing.T) {
	for _, c := range []struct {
		name        string
		annotations map[string]string
		want        map[string]string
		unreadable  bool
		none        bool
	}{
		{"none", nil, nil, false, true},
		{"another annotation", map[string]string{"app": "x"}, nil, false, true},
		{"verdicts", map[string]string{annotationVerify: `{"ghcr.io/a/b:1":"pass","ghcr.io/a/c:2":"fail","docker.io/d:3":"skip"}`},
			map[string]string{"ghcr.io/a/b:1": "pass", "ghcr.io/a/c:2": "fail", "docker.io/d:3": "skip"}, false, false},
		{"booleans of older releases", map[string]string{annotationVerify: `{"a:1":true,"b:2":false}`},
			map[string]string{"a:1": "pass", "b:2": "fail"}, false, false},
		{"scoped, when the plain one is absent", map[string]string{annotationVerifyScoped: `{"pol/rule":{"a:1":"pass"}}`},
			map[string]string{"a:1": "pass"}, false, false},
		{"the plain one wins", map[string]string{annotationVerify: `{"a:1":"fail"}`, annotationVerifyScoped: `{"pol/rule":{"a:1":"pass"}}`},
			map[string]string{"a:1": "fail"}, false, false},
		{"not JSON", map[string]string{annotationVerify: `pass`}, nil, true, false},
		{"no verdict in it", map[string]string{annotationVerify: `{"a:1":"maybe"}`}, nil, true, false},
		{"over 16 KiB", map[string]string{annotationVerify: `{"a:1":"pass","pad":"` + strings.Repeat("x", MaxAnnotation) + `"}`}, nil, true, false},
	} {
		v := verifiedOf(c.annotations)
		if c.none {
			if v != nil {
				t.Errorf("%s: %+v, want no verdict at all", c.name, v)
			}
			continue
		}
		if v == nil || v.unreadable != c.unreadable {
			t.Errorf("%s: %+v, unreadable want %v", c.name, v, c.unreadable)
			continue
		}
		if fmt.Sprint(v.images) != fmt.Sprint(c.want) && !c.unreadable {
			t.Errorf("%s: %v, want %v", c.name, v.images, c.want)
		}
	}
	// Exactly 16 KiB is still read.
	pad := MaxAnnotation - len(`{"a:1":"pass","p":""}`)
	if v := verifiedOf(map[string]string{annotationVerify: `{"a:1":"pass","p":"` + strings.Repeat("x", pad) + `"}`}); v == nil || v.unreadable {
		t.Errorf("16 KiB exactly: %+v", v)
	}
}

// signed is a cluster of four Deployments: one verified, one failed on one
// of its pods, one skipped, one no admission looked at.
func signed(t *testing.T) *fakeHost {
	h := newHost(t)
	for _, d := range []string{"ok", "bad", "skip", "none"} {
		h.add("replicasets", replicaSet("sc", d+"-1", d, 1))
		h.add(KindVulnerability, vulnReport("replicaset-"+d+"-1-app", scan{"sc", "ReplicaSet", d + "-1", "app"}, "sc/"+d, "1", Counts{Low: 1}))
	}
	h.add("pods",
		annotated(pod("sc", "ok-1-a", nil, "ReplicaSet", "ok-1"), annotationVerify, `{"sc/ok:1":"pass","sc/side:1":"pass"}`),
		annotated(pod("sc", "bad-1-a", nil, "ReplicaSet", "bad-1"), annotationVerify, `{"sc/bad:1":"pass"}`),
		annotated(pod("sc", "bad-1-b", nil, "ReplicaSet", "bad-1"), annotationVerify, `{"sc/bad:1":"fail"}`),
		annotated(pod("sc", "skip-1-a", nil, "ReplicaSet", "skip-1"), annotationVerify, `{"sc/skip:1":"skip","sc/side:1":"pass"}`),
		pod("sc", "none-1-a", nil, "ReplicaSet", "none-1"))
	return h
}

func TestAWorkloadCarriesTheWorstVerdictOfItsPodsAndNoneWithoutAnnotation(t *testing.T) {
	o := decode[Overview](t, serve(t, signed(t), "/"))
	for name, want := range map[string]string{"ok": VerdictPass, "bad": VerdictFail, "skip": VerdictSkip} {
		row := rowOf(t, o, "Deployment", name)
		if row.Signature == nil || row.Signature.Verdict != want {
			t.Errorf("%s: %+v, want %s", name, row.Signature, want)
		}
	}
	if bad := rowOf(t, o, "Deployment", "bad").Signature; len(bad.Images) != 1 || bad.Images[0] != (ImageVerdict{"sc/bad:1", VerdictFail}) {
		t.Errorf("an image is as bad as its worst pod: %+v", bad.Images)
	}
	if row := rowOf(t, o, "Deployment", "none"); row.Signature != nil {
		t.Errorf("no annotation is no verdict, never unsigned: %+v", row.Signature)
	}
	if o.SignatureFailed != 1 || o.Verified != 1 {
		t.Errorf("failed %d verified %d", o.SignatureFailed, o.Verified)
	}
	if s := o.Signatures; s.Kind != KindSignatures || s.State != StateRead || !s.Complete || s.Reports != 4 {
		t.Errorf("source %+v", s)
	}
	body := serve(t, signed(t), "/").Body.String()
	if !strings.Contains(body, `"signature":null`) {
		t.Errorf("a workload with no verdict carries null: %s", body)
	}
	d := decode[Detail](t, serve(t, signed(t), "/workload/sc/Deployment/bad"))
	if d.Signature == nil || d.Signature.Verdict != VerdictFail {
		t.Errorf("detail %+v", d.Signature)
	}
	for path, want := range map[string]string{"/brief/sc/bad?kind=deployments": VerdictFail, "/brief/sc/ok-1-a?kind=pods": VerdictPass} {
		if b := decode[Brief](t, serve(t, signed(t), path)); b.Signature == nil || b.Signature.Verdict != want {
			t.Errorf("%s: %+v", path, b.Signature)
		}
	}
	if b := decode[Brief](t, serve(t, signed(t), "/brief/sc/none?kind=deployments")); b.Signature != nil {
		t.Errorf("brief with no verdict: %+v", b.Signature)
	}
}

func TestTheSignaturesSourceSaysItsState(t *testing.T) {
	// No pod carries the annotation: absent, drawn `—`.
	o := decode[Overview](t, serve(t, shop(t), "/"))
	if o.Signatures.State != StateAbsent || o.Verified != 0 || o.SignatureFailed != 0 {
		t.Errorf("no annotation: %+v", o.Signatures)
	}
	// Every annotation found is unreadable.
	h := newHost(t)
	h.add("pods", annotated(pod("a", "p", nil, "", ""), annotationVerify, "{"))
	o = decode[Overview](t, serve(t, h, "/"))
	if o.Signatures.State != StateUnreadable {
		t.Errorf("unreadable: %+v", o.Signatures)
	}
	if row := rowOf(t, o, "Pod", "p"); row.Signature == nil || row.Signature.Verdict != VerdictUnreadable || row.Signature.Unreadable != 1 {
		t.Errorf("row %+v", row.Signature)
	}
	// One read, one not: read, partial.
	h.add("pods", annotated(pod("a", "q", nil, "", ""), annotationVerify, `{"a:1":"pass"}`))
	o = decode[Overview](t, serve(t, h, "/"))
	if s := o.Signatures; s.State != StateRead || s.Complete || s.Reports != 1 || !strings.Contains(s.Note, "1 pod annotations unreadable") {
		t.Errorf("partial: %+v", s)
	}
	// The pods are refused or fail: the source says so.
	h.listErr["pods"] = fmt.Errorf("x: %w", extension.ErrDenied)
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateDenied {
		t.Errorf("denied: %+v", s)
	}
	h.listErr["pods"] = fmt.Errorf("x: %w", extension.ErrUnreviewable)
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateDenied || s.Note != extension.UnreviewedText {
		t.Errorf("unreviewable: %+v", s)
	}
	h.listErr["pods"] = fmt.Errorf("the API server did not answer")
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateUnreadable {
		t.Errorf("failed: %+v", s)
	}
	// Narrowed to the namespaces the person may list.
	h = signed(t)
	h.allowed = []string{"sc"}
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateRead || s.Complete {
		t.Errorf("narrowed: %+v", s)
	}
	// No operator: absent too, and nothing read.
	h = newHost(t)
	h.reg.served = map[string]bool{}
	if rec := serve(t, h, "/"); rec.Code != http.StatusOK || decode[Overview](t, rec).Signatures.State != StateAbsent {
		t.Errorf("no operator: %s", rec.Body.String())
	}
}

// One image's verdicts are capped, the worst first.
func TestAWorkloadListsAtMostMaxSignatureImages(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"z:1":"fail"`)
	for i := 0; i < MaxSignatureImages+10; i++ {
		fmt.Fprintf(&b, `,"img-%03d:1":"pass"`, i)
	}
	b.WriteString("}")
	h := newHost(t)
	h.add("pods", annotated(pod("a", "p", nil, "", ""), annotationVerify, b.String()))
	row := rowOf(t, decode[Overview](t, serve(t, h, "/")), "Pod", "p")
	if len(row.Signature.Images) != MaxSignatureImages || row.Signature.Images[0].Verdict != VerdictFail {
		t.Errorf("%d images, first %+v", len(row.Signature.Images), row.Signature.Images[0])
	}
}

// imageResult is a result of a Kyverno ImageValidatingPolicy, as Kyverno 1.20
// writes it in a PolicyReport.
func imageResult(policy, outcome, message string) map[string]any {
	return map[string]any{"policy": policy, "rule": policy, "result": outcome, "source": "KyvernoImageValidatingPolicy",
		"message": message, "properties": map[string]any{"process": "background scan"}}
}

const unsignedImage = "ghcr.io/kyverno/test-verify-image:unsigned@sha256:74a98f0e4d750c9052f092a7f7a72de7b20f94f176a490088f7a744c76c53ea5"

// kyvernoSigned is what Kyverno 1.20 left on kind: no annotation on any pod,
// and one report per resource - the Deployment, its ReplicaSet, its Pod -
// with the image policy's verdict beside another policy's failure.
func kyvernoSigned(t *testing.T) *fakeHost {
	h := newHost(t)
	servePolicies(h)
	unsigned := "the image is not signed with the test key; verification details: image \"" + unsignedImage +
		"\", attestor \"cosign\": failed to verify cosign signatures: no matching signatures: invalid signature"
	limits := map[string]any{"policy": "require-limits", "rule": "validation", "result": "fail", "source": "KyvernoValidatingPolicy",
		"message": "limits are required"}
	for _, w := range []struct{ name, rs, pod, outcome, message string }{
		{"signed", "signed-5d9c7f", "signed-5d9c7f-q2x8z", "pass", ""},
		{"unsigned", "unsigned-cbb869fd8", "unsigned-cbb869fd8-7gq78", "fail", unsigned},
	} {
		h.add("replicasets", replicaSet("sec-test", w.rs, w.name, 1))
		h.add("pods", pod("sec-test", w.pod, nil, "ReplicaSet", w.rs))
		for _, about := range []struct{ kind, name string }{{"Deployment", w.name}, {"ReplicaSet", w.rs}, {"Pod", w.pod}} {
			h.add("policyreports", policyReport("sec-test", "r-"+about.name, about.kind, about.name,
				imageResult("verify-test-image", w.outcome, w.message), limits))
		}
	}
	return h
}

func TestKyvernosImagePolicyVerdictIsReadFromThePolicyReports(t *testing.T) {
	h := kyvernoSigned(t)
	o := decode[Overview](t, serve(t, h, "/"))
	good, unsigned := rowOf(t, o, "Deployment", "signed").Signature, rowOf(t, o, "Deployment", "unsigned").Signature
	if good == nil || good.Verdict != VerdictPass || len(good.Images) != 1 || good.Images[0] != (ImageVerdict{"verify-test-image", VerdictPass}) {
		t.Errorf("signed: %+v", good)
	}
	if unsigned == nil || unsigned.Verdict != VerdictFail || len(unsigned.Images) != 1 || unsigned.Images[0] != (ImageVerdict{unsignedImage, VerdictFail}) {
		t.Errorf("unsigned: %+v", unsigned)
	}
	if s := o.Signatures; s.State != StateRead || !s.Complete || s.Reports != 2 || s.Note != "" {
		t.Errorf("source %+v", s)
	}
	if o.SignatureFailed != 1 || o.Verified != 1 {
		t.Errorf("failed %d verified %d", o.SignatureFailed, o.Verified)
	}
	// The reports are walked, in the namespace asked, bounded, never as a table.
	qs := h.reportQueries("policyreports")
	if len(qs) != 1 || qs[0].Each == nil || qs[0].Columns || qs[0].Limit != MaxPolicyReports {
		t.Errorf("policy report reads %+v", qs)
	}
	for path, want := range map[string]string{
		"/workload/sec-test/Deployment/signed": VerdictPass, "/workload/sec-test/Deployment/unsigned": VerdictFail,
	} {
		if d := decode[Detail](t, serve(t, kyvernoSigned(t), path)); d.Signature == nil || d.Signature.Verdict != want {
			t.Errorf("%s: %+v", path, d.Signature)
		}
	}
	for path, want := range map[string]string{
		"/brief/sec-test/signed?kind=deployments": VerdictPass, "/brief/sec-test/unsigned-cbb869fd8-7gq78?kind=pods": VerdictFail,
	} {
		if b := decode[Brief](t, serve(t, kyvernoSigned(t), path)); b.Signature == nil || b.Signature.Verdict != want {
			t.Errorf("%s: %+v", path, b.Signature)
		}
	}
	// The newer report group alone says the same.
	h = kyvernoSigned(t)
	h.objects["reports"], h.objects["policyreports"] = h.objects["policyreports"], nil
	if sig := rowOf(t, decode[Overview](t, serve(t, h, "/")), "Deployment", "unsigned").Signature; sig == nil || sig.Verdict != VerdictFail {
		t.Errorf("openreports: %+v", sig)
	}
	// A report whose image policy did not run, or no policy engine at all,
	// is no verdict: absent, and no report read where none is served.
	h = newHost(t)
	servePolicies(h)
	h.add("pods", pod("a", "p", nil, "", ""))
	h.add("policyreports", policyReport("a", "r", "Pod", "p", polResult("require-labels", "check", "fail", "", "")))
	if o := decode[Overview](t, serve(t, h, "/")); o.Signatures.State != StateAbsent || rowOf(t, o, "Pod", "p").Signature != nil {
		t.Errorf("no image policy: %+v", o.Signatures)
	}
	h = signed(t)
	_ = decode[Overview](t, serve(t, h, "/"))
	if n := len(h.reportQueries("policyreports")) + len(h.reportQueries("reports")); n != 0 {
		t.Errorf("%d policy report reads where none is served", n)
	}
}

func TestTheAnnotationAndThePolicyReportsMergeTheWorstWinning(t *testing.T) {
	h := kyvernoSigned(t)
	// An older Kyverno's annotation on the signed pod: one image failed.
	for _, p := range h.objects["pods"] {
		if p.name == "signed-5d9c7f-q2x8z" {
			annotated(p, annotationVerify, `{"ghcr.io/x/side:1":"fail","ghcr.io/x/main:1":"pass"}`)
		}
	}
	o := decode[Overview](t, serve(t, h, "/"))
	sig := rowOf(t, o, "Deployment", "signed").Signature
	if sig == nil || sig.Verdict != VerdictFail || len(sig.Images) != 3 || sig.Images[0] != (ImageVerdict{"ghcr.io/x/side:1", VerdictFail}) {
		t.Errorf("merged: %+v", sig)
	}
	if s := o.Signatures; s.State != StateRead || s.Reports != 2 {
		t.Errorf("source %+v", s)
	}
	// The annotation alone, the reports served and empty, still reads.
	h = signed(t)
	servePolicies(h)
	if sig := rowOf(t, decode[Overview](t, serve(t, h, "/")), "Deployment", "bad").Signature; sig == nil || sig.Verdict != VerdictFail {
		t.Errorf("annotation only: %+v", sig)
	}
	// The same image in a pass from the reports and a fail on the pod: fail.
	h = kyvernoSigned(t)
	for _, p := range h.objects["pods"] {
		if p.name == "signed-5d9c7f-q2x8z" {
			annotated(p, annotationVerify, `{"verify-test-image":"fail"}`)
		}
	}
	sig = rowOf(t, decode[Overview](t, serve(t, h, "/")), "Deployment", "signed").Signature
	if sig == nil || len(sig.Images) != 1 || sig.Images[0].Verdict != VerdictFail {
		t.Errorf("same image: %+v", sig)
	}
}

func TestAPolicyReportReadThatFailedIsNeverAbsent(t *testing.T) {
	// Refused, nothing else found: the source says so.
	h := kyvernoSigned(t)
	h.listErr["policyreports"] = fmt.Errorf("x: %w", extension.ErrDenied)
	s := decode[Overview](t, serve(t, h, "/")).Signatures
	if s.State != StateDenied || s.Complete || !strings.Contains(s.Note, KindPolicyReports) {
		t.Errorf("denied: %+v", s)
	}
	h.listErr["policyreports"] = fmt.Errorf("the API server did not answer")
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateUnreadable || !strings.Contains(s.Note, "did not answer") {
		t.Errorf("failed: %+v", s)
	}
	// Refused, but the other group answered: read, partial, and said.
	h = kyvernoSigned(t)
	h.add("reports", h.objects["policyreports"]...)
	h.listErr["policyreports"] = fmt.Errorf("x: %w", extension.ErrDenied)
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateRead || s.Complete || !strings.Contains(s.Note, KindPolicyReports) {
		t.Errorf("one group refused: %+v", s)
	}
	// The pods refused, the reports read: read, partial, and the pods named.
	h = kyvernoSigned(t)
	h.listErr["pods"] = fmt.Errorf("x: %w", extension.ErrDenied)
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateRead || s.Complete || !strings.Contains(s.Note, "pods") {
		t.Errorf("pods refused: %+v", s)
	}
	// Narrowed to the namespaces the person may list: partial.
	h = kyvernoSigned(t)
	h.allowed = []string{"sec-test"}
	if s := decode[Overview](t, serve(t, h, "/")).Signatures; s.State != StateRead || s.Complete {
		t.Errorf("narrowed: %+v", s)
	}
}

// One resource keeps at most MaxSignatureImages images, never at the cost of
// a failure.
func TestTheReportVerdictsAreBounded(t *testing.T) {
	v := newReportVerdicts()
	res := resource{"a", "Pod", "p"}
	for i := 0; i < MaxSignatureImages+10; i++ {
		v.add(res, imageResult("pol", "pass", fmt.Sprintf(`image "img-%03d:1" verified`, i)))
	}
	v.add(res, imageResult("pol", "error", `image "z:1": no answer`))
	if n := len(v.by[res]); n != MaxSignatureImages || v.by[res]["z:1"] != VerdictFail {
		t.Errorf("%d images, z:1 %q", n, v.by[res]["z:1"])
	}
	v.add(res, map[string]any{"policy": "pol", "result": "fail", "source": "kyverno"})
	v.add(resource{"a", "Pod", "q"}, imageResult("pol", "warn", ""))
	if len(v.by) != 1 {
		t.Errorf("only image verifications are kept: %v", v.by)
	}
}
