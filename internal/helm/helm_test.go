package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- the fixture, written the way Helm writes ------------------------------------

// helmish is a release as Helm's own document, in Helm's own field names, so the
// decoder is exercised against the *spelling* and not only against the shape.
//
// Written as a `map` and not as this package's `storedRelease`, deliberately:
// marshalling the struct the reader unmarshals would make every field name agree
// with itself by construction, and the field names are exactly what a fixture
// has to be able to disagree about. Every key below was read off a live release
// — see decode.go — and `manifest` is in there because the reader
// must be indifferent to the 1.9 MB of it a real release carries.
func helmish(name, namespace string, revision int, status, chart, chartVersion, appVersion,
	description string, lastDeployed time.Time) map[string]any {
	deployed := "null"
	if !lastDeployed.IsZero() {
		deployed = lastDeployed.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"name":      name,
		"namespace": namespace,
		"version":   revision,
		"info": map[string]any{
			"first_deployed": "2024-05-15T22:31:11.7651335+02:00",
			"last_deployed":  deployed,
			// The empty string and not null, which is what the live document
			// carried and what `time.Time.UnmarshalJSON` refuses.
			"deleted":     "",
			"description": description,
			"status":      status,
			"notes":       "In order to access the server UI you have the following options:",
		},
		"chart": map[string]any{
			"metadata": map[string]any{
				"name":       chart,
				"version":    chartVersion,
				"appVersion": appVersion,
				"apiVersion": "v2",
			},
			"templates": []any{map[string]any{"name": "templates/deployment.yaml", "data": "..."}},
			// The chart's own defaults, which are **not** what the values panel
			// draws and are in the fixture so that a reader taking them by
			// mistake is caught rather than agreed with.
			"values": map[string]any{"replicaCount": 1, "server": map[string]any{"insecure": false}},
		},
		// The values the operator supplied - `config`, which is what
		// `helm get values` prints without `--all`.
		"config":   map[string]any{"replicaCount": 3, "server": map[string]any{"insecure": true}},
		"manifest": "---\n# Source: " + chart + "/templates/deployment.yaml\n",
	}
}

// encode is Helm's own encoding, reproduced: JSON, gzip, base64. The Secret's
// own base64 is Kubernetes' and client-go does it, which is why there is one
// here and two on the wire.
func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshalling the fixture: %v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		t.Fatalf("gzipping the fixture: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the gzip writer: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// releaseSecret is one revision as Helm stores it, labels and all.
func releaseSecret(t *testing.T, doc map[string]any, data []byte) *corev1.Secret {
	t.Helper()
	name, _ := doc["name"].(string)
	namespace, _ := doc["namespace"].(string)
	revision, _ := doc["version"].(int)
	info, _ := doc["info"].(map[string]any)
	status, _ := info["status"].(string)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1." + name + ".v" + strconv.Itoa(revision),
			Namespace: namespace,
			Labels: map[string]string{
				ownerLabel:   ownerHelm,
				nameLabel:    name,
				versionLabel: strconv.Itoa(revision),
				statusLabel:  status,
				"modifiedAt": "1715805073",
			},
		},
		Type: ReleaseType,
		Data: map[string][]byte{releaseKey: data},
	}
}

func revisionOf(t *testing.T, name, namespace string, revision int, status, chartVersion,
	appVersion, description string, when time.Time) *corev1.Secret {
	t.Helper()
	doc := helmish(name, namespace, revision, status, "argo-cd", chartVersion, appVersion,
		description, when)
	return releaseSecret(t, doc, encode(t, doc))
}

// stubClusters is the Clusters interface with one client in it.
type stubClusters struct {
	client *cluster.Client
	err    error
}

func (s stubClusters) Client(string) (*cluster.Client, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.client, nil
}

func readerOver(objects ...runtime.Object) (*Reader, *fake.Clientset) {
	api := fake.NewSimpleClientset(objects...)
	return NewReader(stubClusters{client: &cluster.Client{Context: "test", Clients: api}}, accesstest.Allow()), api
}

// --- the storage format ----------------------------------------------------------

// The whole feature rests on this and nothing else does. Get it wrong and the
// page says a cluster running forty releases has none: a failed answer drawn as
// an empty one, the most reassuring failure there is.
func TestTheReleaseIsReadOutOfHelmsOwnEncoding(t *testing.T) {
	when := time.Date(2026, 8, 30, 15, 22, 7, 179687200, time.FixedZone("CEST", 2*3600))
	doc := helmish("argocd", "argocd", 41, "deployed", "argo-cd", "10.4.2", "v3.5.2",
		"Upgrade complete", when)

	got, err := decodeRelease(encode(t, doc))
	if err != nil {
		t.Fatalf("decoding a release written the way Helm writes: %v", err)
	}
	if got.Name != "argocd" || got.Namespace != "argocd" || got.Version != 41 {
		t.Errorf("identity: got %q/%q v%d, want argocd/argocd v41", got.Namespace, got.Name, got.Version)
	}
	if got.Info.Status != "deployed" || got.Info.Description != "Upgrade complete" {
		t.Errorf("info: got %q / %q", got.Info.Status, got.Info.Description)
	}
	if got.Chart.Metadata.Name != "argo-cd" {
		t.Errorf("chart name: got %q", got.Chart.Metadata.Name)
	}
	// The two versions, separately. A single field would pass a test that only
	// looked at one of them, which is the whole reason the page has two columns.
	if got.Chart.Metadata.Version != "10.4.2" {
		t.Errorf("chart version: got %q, want 10.4.2", got.Chart.Metadata.Version)
	}
	if got.Chart.Metadata.AppVersion != "v3.5.2" {
		t.Errorf("app version: got %q, want v3.5.2", got.Chart.Metadata.AppVersion)
	}
	if !got.Info.LastDeployed.Time.Equal(when) {
		t.Errorf("last deployed: got %v, want %v", got.Info.LastDeployed.Time, when)
	}
}

// Helm's decoder sniffs for gzip rather than assuming it, because releases
// written by early Helm 3 are plain JSON at that point. A reader that always
// decompressed would refuse exactly those, and the message would read as a
// corrupt Secret rather than as an old one.
func TestAReleaseThatWasNeverGzippedStillReads(t *testing.T) {
	doc := helmish("plain", "default", 1, "deployed", "chart", "1.0.0", "9", "Install complete",
		time.Now())
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("%v", err)
	}
	got, err := decodeRelease([]byte(base64.StdEncoding.EncodeToString(body)))
	if err != nil {
		t.Fatalf("decoding an un-gzipped release: %v", err)
	}
	if got.Name != "plain" || got.Version != 1 {
		t.Errorf("got %q v%d", got.Name, got.Version)
	}
}

// `"deleted": ""` is what the live document carried, and `time.Time`'s own
// UnmarshalJSON refuses it. A decoder that used a plain time.Time would fail the
// whole release on the one field nothing draws, and the page would then report a
// perfectly good release as unreadable.
func TestAnEmptyTimestampDoesNotFailTheWholeRelease(t *testing.T) {
	for _, spelling := range []string{`""`, `null`} {
		body := `{"name":"x","namespace":"y","version":3,` +
			`"info":{"deleted":` + spelling + `,"status":"deployed","last_deployed":` + spelling + `}}`
		got, err := decodeRelease([]byte(base64.StdEncoding.EncodeToString([]byte(body))))
		if err != nil {
			t.Fatalf("deleted=%s: %v", spelling, err)
		}
		if got.Version != 3 || got.Info.Status != "deployed" {
			t.Errorf("deleted=%s: got v%d %q", spelling, got.Version, got.Info.Status)
		}
		if !got.Info.LastDeployed.Time.IsZero() {
			t.Errorf("deleted=%s: last_deployed should be the zero time, got %v",
				spelling, got.Info.LastDeployed.Time)
		}
	}
}

// One base64 layer undone and stopped is the failure this feature is most likely
// to ship with, so it is asserted as a failure rather than left to be noticed:
// what is left is a base64 alphabet, no JSON parser touches it, and the page
// would report the cluster as empty.
func TestStoppingAfterOneBase64LayerIsAnError(t *testing.T) {
	doc := helmish("argocd", "argocd", 1, "deployed", "argo-cd", "10.4.2", "v3.5.2", "", time.Now())
	once, err := base64.StdEncoding.DecodeString(string(encode(t, doc)))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := json.Marshal(json.RawMessage(once)); err == nil {
		// Not the assertion, just the reason: `once` is gzip, and the point is
		// that the reader's own second step is what makes it JSON.
		_ = err
	}
	if _, err := decodeRelease(once); err == nil {
		t.Fatal("a single-layer body decoded; the double encoding is not being undone")
	}
}

// --- the fold --------------------------------------------------------------------

// The decision the task turns on: the current revision is the highest one, which
// is what `helm list` shows. A failed upgrade is the case where that differs from
// the deployed one, and the row has to carry both facts or it is a lie either way.
func TestAFailedUpgradeShowsTheFailureAndNamesTheDeployedRevision(t *testing.T) {
	now := time.Now()
	reader, _ := readerOver(
		revisionOf(t, "argocd", "argocd", 40, StatusDeployed, "10.4.1", "v3.5.1",
			"Upgrade complete", now.Add(-72*time.Hour)),
		revisionOf(t, "argocd", "argocd", 41, "failed", "10.4.2", "v3.5.2",
			"Upgrade \"argocd\" failed: timed out waiting for the condition", now.Add(-time.Hour)),
	)

	report := reader.Releases(t.Context(), "test", nil)
	if report.State != StatePresent {
		t.Fatalf("state %v, detail %q", report.State, report.Detail)
	}
	if len(report.Releases) != 1 {
		t.Fatalf("two revisions of one release folded to %d rows, want 1", len(report.Releases))
	}
	got := report.Releases[0]
	if got.Revision != 41 {
		t.Errorf("current revision %d, want 41 — the highest, which is what `helm list` shows", got.Revision)
	}
	if got.Status != "failed" {
		t.Errorf("status %q, want failed", got.Status)
	}
	if got.ChartVersion != "10.4.2" {
		t.Errorf("chart version %q, want the failed revision's 10.4.2", got.ChartVersion)
	}
	// The half that keeps the row honest.
	if got.DeployedRevision != 40 {
		t.Errorf("DeployedRevision %d, want 40; without it the row says nothing is running",
			got.DeployedRevision)
	}
}

// And the ordinary case: nothing is claimed about a deployed revision elsewhere
// when the current one *is* it. A field that were always set would make the page
// draw "revision 41 is the deployed one" beside revision 41.
func TestAHealthyReleaseNamesNoOtherDeployedRevision(t *testing.T) {
	reader, _ := readerOver(
		revisionOf(t, "argocd", "argocd", 41, StatusDeployed, "10.4.2", "v3.5.2",
			"Upgrade complete", time.Now()),
	)
	report := reader.Releases(t.Context(), "test", nil)
	if len(report.Releases) != 1 {
		t.Fatalf("%d rows", len(report.Releases))
	}
	if got := report.Releases[0].DeployedRevision; got != 0 {
		t.Errorf("DeployedRevision %d on a healthy release, want 0", got)
	}
}

// Twelve revisions are one row. Twelve rows is the same defect class as
// svcgroup's wrong split, and it is what the fold exists to prevent.
func TestEveryRevisionOfOneReleaseIsOneRow(t *testing.T) {
	var objects []runtime.Object
	for revision := 1; revision <= 12; revision++ {
		status := StatusSuperseded
		if revision == 12 {
			status = StatusDeployed
		}
		objects = append(objects, revisionOf(t, "redis", "cache", revision, status,
			"18.1."+strconv.Itoa(revision), "7.2.1", "Upgrade complete", time.Now()))
	}
	reader, _ := readerOver(objects...)

	report := reader.Releases(t.Context(), "test", nil)
	if len(report.Releases) != 1 {
		t.Fatalf("12 revisions gave %d rows, want 1", len(report.Releases))
	}
	if report.Releases[0].Revision != 12 {
		t.Errorf("current revision %d, want 12", report.Releases[0].Revision)
	}
	// And the narrowing did its job: only the non-superseded revision was read.
	if report.Releases[0].Revisions != 1 {
		t.Errorf("the list read %d revisions of this release; the selector should have "+
			"narrowed it to the one that is not superseded", report.Releases[0].Revisions)
	}
}

// Two namespaces, one release name. Folding on the name alone would report one
// installation where there are two — and would attribute one's chart version to
// the other.
func TestTwoNamespacesWithTheSameReleaseNameAreTwoReleases(t *testing.T) {
	reader, _ := readerOver(
		revisionOf(t, "redis", "team-a", 1, StatusDeployed, "18.1.0", "7.2.1", "Install complete", time.Now()),
		revisionOf(t, "redis", "team-b", 3, StatusDeployed, "19.0.0", "7.4.0", "Upgrade complete", time.Now()),
	)
	report := reader.Releases(t.Context(), "test", nil)
	if len(report.Releases) != 2 {
		t.Fatalf("%d rows, want 2", len(report.Releases))
	}
	for _, r := range report.Releases {
		switch r.Namespace {
		case "team-a":
			if r.ChartVersion != "18.1.0" || r.Revision != 1 {
				t.Errorf("team-a: %s at revision %d", r.ChartVersion, r.Revision)
			}
		case "team-b":
			if r.ChartVersion != "19.0.0" || r.Revision != 3 {
				t.Errorf("team-b: %s at revision %d", r.ChartVersion, r.Revision)
			}
		default:
			t.Errorf("unexpected namespace %q", r.Namespace)
		}
	}
}

// --- the narrowing ---------------------------------------------------------------

// The measurement in the package comment is the whole argument for the list's
// selector, and a selector is exactly the kind of string that survives being
// changed by accident. This asserts what went on the wire.
func TestTheListAsksForTheRevisionsThatCanBeCurrent(t *testing.T) {
	reader, api := readerOver(
		revisionOf(t, "argocd", "argocd", 1, StatusDeployed, "10.4.2", "v3.5.2", "", time.Now()),
	)
	reader.Releases(t.Context(), "test", nil)

	var asked []k8stesting.ListActionImpl
	for _, action := range api.Actions() {
		if list, ok := action.(k8stesting.ListActionImpl); ok && action.GetResource().Resource == "secrets" {
			asked = append(asked, list)
		}
	}
	if len(asked) == 0 {
		t.Fatal("no Secret listing happened at all")
	}
	label := asked[0].GetListRestrictions().Labels.String()
	if !strings.Contains(label, ownerLabel+"="+ownerHelm) {
		t.Errorf("label selector %q does not narrow to Helm's own Secrets", label)
	}
	if !strings.Contains(label, StatusSuperseded) {
		t.Errorf("label selector %q does not exclude superseded revisions; every revision of "+
			"every release would then be fetched, which measured 6 637 191 bytes for one "+
			"release", label)
	}
}

// A selection of several is one listing per selected namespace (a cluster-wide
// listing narrowed here would count other teams' release Secrets against the
// person's ceiling). **One listing failing fails the page**, so a table is never
// drawn from two of the three namespaces asked for: a partial answer is not
// presented as a whole one.
func TestASelectionOfSeveralIsOneListingPerNamespaceAndFailsWhole(t *testing.T) {
	reader, api := readerOver(
		revisionOf(t, "argocd", "argocd", 1, StatusDeployed, "10.4.2", "v3.5.2", "", time.Now()),
		revisionOf(t, "redis", "cache", 1, StatusDeployed, "18.1.0", "7.2.1", "", time.Now()),
		revisionOf(t, "loki", "logging", 1, StatusDeployed, "6.0.0", "3.0.0", "", time.Now()),
	)

	report := reader.Releases(t.Context(), "test", []string{"argocd", "cache", "argocd"})

	var listed []string
	for _, action := range api.Actions() {
		if action.Matches("list", "secrets") {
			listed = append(listed, action.GetNamespace())
		}
	}
	if strings.Join(listed, ",") != "argocd,cache" {
		t.Errorf("listed %q for a selection of argocd and cache (argocd named twice), want each once", listed)
	}
	if len(report.Releases) != 2 {
		t.Fatalf("%d rows, want the two selected", len(report.Releases))
	}
	for _, r := range report.Releases {
		if r.Namespace == "logging" {
			t.Errorf("logging was not selected and is on the page")
		}
	}

	// The second listing fails: the page is not drawn from the first alone.
	api.PrependReactor("list", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "cache" {
			return true, nil, errors.New("connection reset by peer")
		}
		return false, nil, nil
	})
	half := reader.Releases(t.Context(), "test", []string{"argocd", "cache"})
	if half.State != StateUnreachable || len(half.Releases) != 0 {
		t.Errorf("one of two listings failed and the page is %v with %d rows; want unreachable, and no half a table",
			half.State, len(half.Releases))
	}
}

// A selection of exactly one is narrowed by the API server, because that is free
// and it is the common case.
func TestASelectionOfOneIsNarrowedByTheApiServer(t *testing.T) {
	reader, api := readerOver(
		revisionOf(t, "argocd", "argocd", 1, StatusDeployed, "10.4.2", "v3.5.2", "", time.Now()),
	)
	reader.Releases(t.Context(), "test", []string{"argocd"})

	for _, action := range api.Actions() {
		if action.Matches("list", "secrets") {
			if action.GetNamespace() != "argocd" {
				t.Errorf("listed namespace %q, want argocd", action.GetNamespace())
			}
			return
		}
	}
	t.Fatal("no listing happened")
}

// --- the four absences -----------------------------------------------------------

// An empty answer and a failed answer are different answers, as four sentences
// that are actually different. A page rendering all four as an empty table would
// say a cluster runs nothing when it could not be read.
func TestEveryWayOfHavingNoRowsSaysSomethingDifferent(t *testing.T) {
	forbidden := &metav1.Status{
		Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden,
		Message: `secrets is forbidden: User "system:serviceaccount:kt:kt" cannot list resource "secrets"`,
	}

	cases := []struct {
		what  string
		build func() *Reader
		state State
		// says are phrases the sentence must carry. Phrases and not words: a
		// single word can appear in a sentence that means something else.
		says []string
	}{
		{
			what: "the cluster could not be asked",
			build: func() *Reader {
				return NewReader(stubClusters{err: errors.New("no context named test")}, accesstest.Allow())
			},
			state: StateUnreachable,
			says:  []string{"could not be asked", "is unknown"},
		},
		{
			what: "this console may not list Secrets",
			build: func() *Reader {
				api := fake.NewSimpleClientset()
				api.PrependReactor("list", "secrets",
					func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, nil, &apiStatusError{status: forbidden}
					})
				return NewReader(stubClusters{client: &cluster.Client{Context: "test", Clients: api}}, accesstest.Allow())
			},
			state: StateUnreadable,
			says:  []string{"may not list Secrets", "cannot be established", "`list` on `secrets`"},
		},
		{
			what:  "there is no release here",
			build: func() *Reader { r, _ := readerOver(); return r },
			state: StateEmpty,
			says:  []string{"No Helm release", "Secret in its own", "storage driver"},
		},
		{
			what: "a release Secret would not decode",
			build: func() *Reader {
				doc := helmish("argocd", "argocd", 1, StatusDeployed, "argo-cd", "10.4.2", "v3.5.2",
					"", time.Now())
				broken := releaseSecret(t, doc, []byte("this is not what Helm writes"))
				r, _ := readerOver(broken)
				return r
			},
			state: StateUndecodable,
			says:  []string{"not one of them could be read", "not an empty cluster"},
		},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		report := tc.build().Releases(t.Context(), "test", nil)
		if report.State != tc.state {
			t.Errorf("%s: state %v, want %v (detail %q)", tc.what, report.State, tc.state, report.Detail)
		}
		if len(report.Releases) != 0 {
			t.Errorf("%s: %d rows drawn", tc.what, len(report.Releases))
		}
		for _, phrase := range tc.says {
			if !strings.Contains(report.Detail, phrase) {
				t.Errorf("%s: sentence %q does not carry %q", tc.what, report.Detail, phrase)
			}
		}
		if before, clash := seen[report.Detail]; clash {
			t.Errorf("%s and %s produce the same sentence: %q", tc.what, before, report.Detail)
		}
		seen[report.Detail] = tc.what
	}
}

// A cluster that could not be asked must never be rendered as a cluster with
// nothing on it — and the *state* is what a template branches on, so it is
// asserted as well as the sentence.
func TestAnUnreachableClusterIsNotAnEmptyOne(t *testing.T) {
	reader := NewReader(stubClusters{err: errors.New("Unauthorized")}, accesstest.Allow())
	if got := reader.Releases(t.Context(), "prod", nil).State; got == StateEmpty {
		t.Fatal("an unreachable cluster reported StateEmpty")
	}
	if got := reader.History(t.Context(), "prod", "argocd", "argocd").State; got == StateEmpty {
		t.Fatal("an unreachable cluster reported StateEmpty on a release's own page")
	}
}

// One Secret that will not decode beside three that will is not an absence, it is
// a note. A page that dropped it silently would be short a release with nothing
// saying so.
func TestOneUnreadableSecretIsSaidAndTheRestAreDrawn(t *testing.T) {
	doc := helmish("broken", "default", 1, StatusDeployed, "chart", "1.0.0", "1", "", time.Now())
	reader, _ := readerOver(
		revisionOf(t, "argocd", "argocd", 1, StatusDeployed, "10.4.2", "v3.5.2", "", time.Now()),
		releaseSecret(t, doc, []byte("not base64 at all !!")),
	)

	report := reader.Releases(t.Context(), "test", nil)
	if report.State != StatePresent {
		t.Fatalf("state %v", report.State)
	}
	if len(report.Releases) != 1 {
		t.Fatalf("%d rows, want the one that decoded", len(report.Releases))
	}
	if !strings.Contains(report.Note, "could not be read") {
		t.Errorf("note %q does not say a Secret was skipped", report.Note)
	}
	if len(report.Undecoded) != 1 || report.Undecoded[0].Secret != "sh.helm.release.v1.broken.v1" {
		t.Errorf("Undecoded %+v does not name the Secret; a count is not actionable", report.Undecoded)
	}
}

// A Secret that is not a Helm release never reaches the decoder. The label
// selector is the API server's opinion; this is ours, and the two exist
// separately because the fake clientset honors label selectors and ignores
// field selectors — so the field selector is untestable here and this is not.
func TestASecretThatIsNotAReleaseIsIgnored(t *testing.T) {
	impostor := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "helm-ish",
			Namespace: "default",
			Labels:    map[string]string{ownerLabel: ownerHelm, nameLabel: "x", versionLabel: "1"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{releaseKey: []byte("nonsense")},
	}
	reader, _ := readerOver(impostor)
	report := reader.Releases(t.Context(), "test", nil)
	if report.State != StateEmpty {
		t.Errorf("state %v, want StateEmpty: a Secret typed Opaque is not a Helm release", report.State)
	}
	if len(report.Undecoded) != 0 {
		t.Errorf("it reached the decoder and was reported as broken: %+v", report.Undecoded)
	}
}

// --- one release's own page ------------------------------------------------------

// The history is why an operator opens a release at all, and the chart version
// per revision is the fact that only exists inside each revision's own document.
func TestAReleasesOwnPageReadsEveryRevisionNewestFirst(t *testing.T) {
	now := time.Now()
	var objects []runtime.Object
	for revision := 1; revision <= 4; revision++ {
		status := StatusSuperseded
		if revision == 4 {
			status = StatusDeployed
		}
		objects = append(objects, revisionOf(t, "argocd", "argocd", revision, status,
			"10.4."+strconv.Itoa(revision), "v3.5."+strconv.Itoa(revision),
			"Upgrade complete", now.Add(-time.Duration(5-revision)*time.Hour)))
	}
	reader, api := readerOver(objects...)

	history := reader.History(t.Context(), "test", "argocd", "argocd")
	if history.State != StatePresent {
		t.Fatalf("state %v, detail %q", history.State, history.Detail)
	}
	if len(history.Revisions) != 4 {
		t.Fatalf("%d revisions, want 4 — including the superseded ones, which the list "+
			"deliberately does not read", len(history.Revisions))
	}
	if history.Revisions[0].Revision != 4 || history.Revisions[3].Revision != 1 {
		t.Errorf("order is %d…%d, want newest first", history.Revisions[0].Revision,
			history.Revisions[3].Revision)
	}
	if history.Revisions[0].ChartVersion != "10.4.4" || history.Revisions[3].ChartVersion != "10.4.1" {
		t.Errorf("chart versions per revision are wrong: %q … %q",
			history.Revisions[0].ChartVersion, history.Revisions[3].ChartVersion)
	}
	if !history.Revisions[0].Current || !history.Revisions[0].Deployed {
		t.Errorf("the newest revision is neither marked current nor deployed")
	}
	if history.Revisions[1].Current {
		t.Errorf("revision %d is marked current as well", history.Revisions[1].Revision)
	}

	// And it asked for the release by name rather than reading the cluster and
	// filtering here.
	for _, action := range api.Actions() {
		if list, ok := action.(k8stesting.ListActionImpl); ok && action.Matches("list", "secrets") {
			label := list.GetListRestrictions().Labels.String()
			if !strings.Contains(label, nameLabel+"=argocd") {
				t.Errorf("history selector %q does not name the release", label)
			}
			if strings.Contains(label, StatusSuperseded) {
				t.Errorf("history selector %q excludes superseded revisions, which are the "+
					"history", label)
			}
			if action.GetNamespace() != "argocd" {
				t.Errorf("history listed namespace %q", action.GetNamespace())
			}
		}
	}
}

// A release name off a URL becomes half of a label selector, so it is refused
// rather than escaped — and refused **before** any request, which is what the
// action count asserts.
func TestAReleaseNameThatCannotBeALabelIsRefusedWithoutAsking(t *testing.T) {
	for _, name := range []string{"argocd,kube-system", "arg ocd", "a/b", "-leading", "", strings.Repeat("x", 64)} {
		reader, api := readerOver()
		history := reader.History(t.Context(), "test", "argocd", name)
		if history.State == StatePresent {
			t.Errorf("%q was accepted", name)
		}
		if len(api.Actions()) != 0 {
			t.Errorf("%q reached the cluster: %d actions", name, len(api.Actions()))
		}
	}
}

// A release whose revisions are gone answers about the release and not about the
// cluster. "No Helm release called X in Y" and "this cluster has none" are
// different claims and only one of them is true here.
func TestAReleaseWithNoRevisionsSaysSoAboutThatRelease(t *testing.T) {
	reader, _ := readerOver(
		revisionOf(t, "redis", "cache", 1, StatusDeployed, "18.1.0", "7.2.1", "", time.Now()),
	)
	history := reader.History(t.Context(), "test", "argocd", "argocd")
	if history.State != StateEmpty {
		t.Fatalf("state %v", history.State)
	}
	for _, phrase := range []string{"argocd", "one Secret per revision"} {
		if !strings.Contains(history.Detail, phrase) {
			t.Errorf("sentence %q does not carry %q", history.Detail, phrase)
		}
	}
}

// A revision whose document will not decode keeps its number and its status,
// because those are in the labels — and it says why the rest of the row is
// blank. A history that renumbered itself around the gap would be a history
// nobody could check against `helm history`.
func TestARevisionThatWillNotDecodeKeepsItsNumberAndSaysWhy(t *testing.T) {
	doc := helmish("argocd", "argocd", 2, "failed", "argo-cd", "10.4.2", "v3.5.2", "", time.Now())
	reader, _ := readerOver(
		revisionOf(t, "argocd", "argocd", 1, StatusDeployed, "10.4.1", "v3.5.1", "Install complete", time.Now()),
		releaseSecret(t, doc, []byte("*** not helm ***")),
	)

	history := reader.History(t.Context(), "test", "argocd", "argocd")
	if history.State != StatePresent {
		t.Fatalf("state %v, detail %q", history.State, history.Detail)
	}
	if len(history.Revisions) != 2 {
		t.Fatalf("%d revisions, want 2", len(history.Revisions))
	}
	broken := history.Revisions[0]
	if broken.Revision != 2 || broken.Status != "failed" {
		t.Errorf("the unreadable revision lost its labels: %+v", broken)
	}
	if broken.Note == "" {
		t.Errorf("nothing says why revision 2 has no chart version")
	}
	if broken.ChartVersion != "" {
		t.Errorf("a chart version was invented for an unreadable revision: %q", broken.ChartVersion)
	}
	if !strings.Contains(history.Note, "could not be read") {
		t.Errorf("history note %q does not say a revision was unreadable", history.Note)
	}
}

// --- the bounds ------------------------------------------------------------------

// A capped list that does not admit it is a lie the operator acts on. The count
// bound is exercised rather than read, because the sentence is the guard.
func TestACappedReadSaysSo(t *testing.T) {
	var objects []runtime.Object
	for revision := 1; revision <= revisionLimit+5; revision++ {
		objects = append(objects, revisionOf(t, "argocd", "argocd", revision, StatusSuperseded,
			"10.4.2", "v3.5.2", "Upgrade complete", time.Now()))
	}
	reader, _ := readerOver(objects...)

	history := reader.History(t.Context(), "test", "argocd", "argocd")
	if len(history.Revisions) != revisionLimit {
		t.Errorf("read %d revisions, want the %d the bound allows", len(history.Revisions), revisionLimit)
	}
	if !strings.Contains(history.Note, "stopped") {
		t.Errorf("note %q does not say the read was cut short", history.Note)
	}
}

// A destination is not a diagnosis, on a display too. The exact message below is
// what an unreachable cluster produces, and the point of the test is the *tail* — the
// clause naming the failure has to survive the 200-character cut, which it does
// not while 140 characters of our own selectors sit in front of it.
func TestTheAddressIsStrippedBeforeTheSentenceIsCut(t *testing.T) {
	raw := errors.New(`Get "https://10.0.0.1:6443/api/v1/secrets?fieldSelector=type%3Dhelm.sh%2F` +
		`release.v1&labelSelector=owner%3Dhelm%2Cstatus+notin+%28superseded%29&limit=50": ` +
		`dial tcp 10.0.0.1:6443: connectex: A connection attempt failed because the ` +
		`connected party did not properly respond after a period of time`)

	got := reason(raw)
	if strings.Contains(got, "labelSelector") || strings.Contains(got, "https://") {
		t.Errorf("the request URL is still in the sentence: %q", got)
	}
	if !strings.HasPrefix(got, "dial tcp") {
		t.Errorf("the sentence should begin with the failure, not with punctuation: %q", got)
	}
	// The half the URL was eating.
	if !strings.Contains(got, "connection attempt failed") {
		t.Errorf("the clause that says what happened did not survive the cut: %q", got)
	}
}

// --- helpers ---------------------------------------------------------------------

// apiStatusError is a 403 the way the API server sends one, so
// apierrors.IsForbidden answers about it. A test that returned a bare
// errors.New("forbidden") would pass a reader that classified on the text rather
// than on the reason.
type apiStatusError struct{ status *metav1.Status }

func (e *apiStatusError) Error() string         { return e.status.Message }
func (e *apiStatusError) Status() metav1.Status { return *e.status }

// --- the release's own page: notes and values -------------------------------------

// The release's own page carries `HelmReleaseDetail.notes` and `.values`, which
// the contract declares and the page draws. The document they come from is
// already in memory - `decodeRelease` decompresses the whole of it and
// `storedRelease` does not name them. So this is not a second read of anything;
// it is `decodeCurrent` naming them, for the current revision and no other.
func TestAReleasesOwnPageCarriesItsNotesAndTheValuesItWasGiven(t *testing.T) {
	when := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	reader, _ := readerOver(
		revisionOf(t, "argocd", "argocd", 1, "superseded", "10.4.1", "v3.5.1", "Install complete", when),
		revisionOf(t, "argocd", "argocd", 2, "deployed", "10.4.2", "v3.5.2", "Upgrade complete", when),
	)

	page := reader.History(t.Context(), "test", "argocd", "argocd")
	if page.State != StatePresent || page.Release.Revision != 2 {
		t.Fatalf("state %v at revision %d; there is nothing to measure", page.State, page.Release.Revision)
	}

	if page.Notes != "In order to access the server UI you have the following options:" {
		t.Errorf("notes %q; Helm renders NOTES.txt into `info.notes` and the document is "+
			"already decompressed by the time this is decided", page.Notes)
	}

	// YAML and not the JSON Helm stores, so the panel is comparable with the
	// `-f values.yaml` that produced it - and `sigs.k8s.io/yaml` because that is
	// what the manifest and custom-resource panels already convert with.
	if !strings.Contains(page.Values, "replicaCount: 3") {
		t.Errorf("values %q carry no `replicaCount: 3`", page.Values)
	}
	if !strings.Contains(page.Values, "insecure: true") {
		t.Errorf("values %q carry no nested `insecure: true`", page.Values)
	}
	if strings.Contains(page.Values, "\"replicaCount\"") {
		t.Errorf("values %q are still JSON", page.Values)
	}

	// **`config` and not `chart.values`.** The chart's defaults say
	// `replicaCount: 1`; showing the merged result answers "what does this chart
	// do", and the question an operator opens this panel with is "what did we
	// change". `helm get values` without `--all` draws the same line.
	if strings.Contains(page.Values, "replicaCount: 1") {
		t.Errorf("values %q carry the chart's own default; this panel is the supplied "+
			"values, which is `helm get values` and not `--all`", page.Values)
	}

	if page.PayloadNote != "" {
		t.Errorf("the payload was read and the page still said %q", page.PayloadNote)
	}
}

// `{}` is what Helm writes for a release installed with no overrides at all, and
// `JSONToYAML` renders it as the two characters `{}`. A panel showing `{}` under
// the heading "Values" is a puzzle where a sentence was wanted, so the empty
// string is what reaches the page and the page says "none were supplied".
//
// Proving it fails: returning `string(out)` unconditionally from `valuesYAML`
// prints
//
//	helm_test.go: a release with no overrides produced values "{}\n"
func TestAReleaseWithNoOverridesHasNoValuesRatherThanEmptyBraces(t *testing.T) {
	for _, config := range []string{`{}`, `null`, ``} {
		values, err := valuesYAML([]byte(config))
		if err != nil {
			t.Fatalf("valuesYAML(%q): %v", config, err)
		}
		if values != "" {
			t.Errorf("a release with no overrides produced values %q from config %q",
				values, config)
		}
	}
}
