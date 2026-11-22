package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/helm"
)

// The other half of `TestTheHelmPayloadsCarryEveryMemberTheContractDeclares`,
// which lives in internal/api and drives the real handlers over `fakeReads` —
// so on its own it passes against this adapter filling nothing at all.
//
// `HelmReleaseDetail.notes` and `.values` are declared in the
// contract, drawn by `web/ui/src/pages/helm/HelmRelease.tsx`, and must be
// assigned by the real producer rather than by a fixture. A fake that fills
// exactly the members the server never sends leaves a member test green while
// two panels of the page are permanently empty: the shape to avoid is
// **a test whose only source for a member is its own fixture**.
//
// This is what closes it: a real `*helm.Reader` over a fake API server holding
// release Secrets in Helm's own encoding, through the real `apiReads.HelmRelease`.
// Nothing in it can be satisfied by a fixture in internal/api.
func TestTheHelmReleasePageIsFilledFromTheProducersThatHoldIt(t *testing.T) {
	server := helmPageFixture(t,
		helmRevisionSecret(t, 1, "deployed", "Install complete", "", nil),
		helmRevisionSecret(t, 2, "deployed", "Upgrade complete",
			"The ingress controller is now running.\nGet its address with:\n  kubectl get svc",
			map[string]any{"controller": map[string]any{"replicaCount": 2}}),
	)

	detail, fail := apiReads{server}.HelmRelease(context.Background(), "prod", "default", "ingress")
	if fail != nil {
		t.Fatalf("the release page failed: %+v", fail.Reason)
	}
	// An empty answer and a failed answer are different answers, and both differ
	// from a page this test walked over without looking at anything.
	if detail.Release.Revision != 2 || len(detail.Revisions) != 2 {
		t.Fatalf("revision %d with %d revisions, want 2 and 2; there is nothing to measure",
			detail.Release.Revision, len(detail.Revisions))
	}

	// Both are said to be there and neither is on the answer: they arrive only
	// through their reveals (helmreveal_test.go).
	if !detail.NotesRendered {
		t.Error("the page says the chart rendered no notes; Helm renders NOTES.txt into " +
			"`info.notes` of the release document and this release has some")
	}
	if !detail.ValuesSupplied {
		t.Error("the page says no values were supplied; the values an operator supplied are " +
			"`config` of the same document, and this release has some")
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "replicaCount") {
		t.Errorf("the release page's default answer carries the supplied values: %s", body)
	}
	if strings.Contains(string(body), "ingress controller is now running") {
		t.Errorf("the release page's default answer carries the notes: %s", body)
	}
}

// The three answers those two panels can carry, and they are three rather than
// two.
//
// A chart that ships no `NOTES.txt` is ordinary, and so is a release installed
// with no `-f` and no `--set`. Both are **answers**: there are none. A current
// revision whose document will not decode is not an answer at all, and a page
// that drew "no values" over it would be telling a person their release has no
// overrides when nothing looked.
func TestTheHelmPanelsSayNoneAndUnreadDifferently(t *testing.T) {
	// `config` absent and `notes` empty, which is what Helm writes for a release
	// installed from a chart with no NOTES.txt and no overrides.
	plain := helmPageFixture(t, helmRevisionSecret(t, 1, "deployed", "Install complete", "", nil))
	detail, fail := apiReads{plain}.HelmRelease(context.Background(), "prod", "default", "ingress")
	if fail != nil {
		t.Fatalf("the plain release failed: %+v", fail.Reason)
	}
	if detail.Release.Revision != 1 {
		t.Fatalf("revision %d; there is nothing to measure", detail.Release.Revision)
	}
	if detail.NotesRendered || detail.ValuesSupplied {
		t.Errorf("a chart with no notes and a release with no overrides produced notesRendered %v "+
			"and valuesSupplied %v; both should be false", detail.NotesRendered, detail.ValuesSupplied)
	}
	if detail.PayloadNote != "" {
		t.Errorf("the page was read and still said %q; a note here means *unread*, and "+
			"saying it over a release that simply has none is the same collapse from the "+
			"other side", detail.PayloadNote)
	}

	// The same release whose current revision is a Secret nothing can decode. It
	// still has its labels, so the page renders — with no facts out of the
	// document, and that is exactly when the two panels must not claim "none".
	broken := helmRevisionSecret(t, 2, "deployed", "Upgrade complete", "hi", nil)
	broken.Data["release"] = []byte("not base64 at all, and not a release either ***")
	page := helmPageFixture(t,
		helmRevisionSecret(t, 1, "superseded", "Install complete", "hello", nil), broken)

	detail, fail = apiReads{page}.HelmRelease(context.Background(), "prod", "default", "ingress")
	if fail != nil {
		t.Fatalf("a release with one unreadable revision failed entirely: %+v", fail.Reason)
	}
	if detail.Release.Revision != 2 {
		t.Fatalf("the current revision is %d, want 2; the history renumbered itself around "+
			"the gap and this test is measuring the wrong revision", detail.Release.Revision)
	}
	if detail.PayloadNote == "" {
		t.Error("the current revision's document would not decode and the page did not say " +
			"so; an empty values panel then reads as a release with no overrides")
	}
	if detail.NotesRendered || detail.ValuesSupplied {
		t.Errorf("nothing could be read and the page carried notesRendered %v and valuesSupplied %v; "+
			"they would have to have come from another revision", detail.NotesRendered, detail.ValuesSupplied)
	}
}

// --- the fixture, written the way Helm writes -------------------------------------

// helmRevisionSecret is one revision of `default/ingress` as Helm stores it.
//
// A map and not internal/helm's own struct, for that package's reason: a fixture
// marshalled from the struct the reader unmarshals agrees with itself by
// construction, and the field names — `info.notes`, `config` — are exactly what
// it has to be able to disagree about.
func helmRevisionSecret(t *testing.T, revision int, status, description, notes string,
	config map[string]any) *corev1.Secret {
	t.Helper()
	doc := map[string]any{
		"name":      "ingress",
		"namespace": "default",
		"version":   revision,
		"info": map[string]any{
			"first_deployed": "2026-05-15T22:31:11.7651335+02:00",
			"last_deployed":  "2026-09-14T10:00:0" + strconv.Itoa(revision) + "+02:00",
			"deleted":        "",
			"description":    description,
			"status":         status,
			"notes":          notes,
		},
		"chart": map[string]any{"metadata": map[string]any{
			"name": "ingress-nginx", "version": "4.11.2", "appVersion": "1.11.2",
		}},
		// The two the list decode must never allocate, present so that a reader
		// which widened `storedRelease` instead of reading the page's own payload
		// would be paying for them on every row.
		"manifest": "---\n# Source: ingress-nginx/templates/deployment.yaml\n",
	}
	if config != nil {
		doc["config"] = config
	}

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

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.ingress.v" + strconv.Itoa(revision),
			Namespace: "default",
			Labels: map[string]string{
				"owner": "helm", "name": "ingress",
				"version": strconv.Itoa(revision), "status": status,
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))},
	}
}

// helmPageFixture is a server whose Helm reader is a real one over a fake API
// server. The kubeconfig exists only so that `New` has a cluster manager; every
// read this test makes goes through the reader installed below.
func helmPageFixture(t *testing.T, objects ...runtime.Object) *Server {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: remote
  cluster:
    server: https://prod.example:6443
contexts:
- name: prod
  context: {cluster: remote, user: none}
users:
- name: none
  user: {token: none}
`), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}

	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{Clusters: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server.helm = helm.NewReader(oneCluster{
		client: &cluster.Client{Context: "prod", Clients: fake.NewSimpleClientset(objects...)},
	}, accesstest.Allow())
	return server
}

// oneCluster is helm.Clusters with a single client in it.
type oneCluster struct{ client *cluster.Client }

func (o oneCluster) Client(string) (*cluster.Client, error) { return o.client, nil }
