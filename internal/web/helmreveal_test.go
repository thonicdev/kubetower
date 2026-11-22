package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/helm"
)

// A Helm release's supplied values reach a view through a reveal, reviewed and
// recorded like a Secret's key: the release page does not answer them on a
// plain view.

// helmRevealServer is a server under the server profile, with an audit log on
// out, whose Helm reader holds one release with supplied values at revision 2,
// and whose reviews allow Secrets when allowSecrets says so.
func helmRevealServer(t *testing.T, allowSecrets bool, out io.Writer) (*Server, *accesstest.Reviews, *fake.Clientset) {
	t.Helper()
	return helmRevealServerOf(t, allowSecrets, out,
		helmRevisionSecret(t, 1, "superseded", "Install complete", "", nil),
		helmRevisionSecret(t, 2, "deployed", "Upgrade complete", helmNotesWithAPassword,
			map[string]any{"auth": map[string]any{"password": "hunter2"}}),
	)
}

// helmRevealServerOf is helmRevealServer holding the release Secrets given.
func helmRevealServerOf(t *testing.T, allowSecrets bool, out io.Writer, secrets ...runtime.Object) (*Server, *accesstest.Reviews, *fake.Clientset) {
	t.Helper()
	manager, _ := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets" || allowSecrets, nil
	}}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(out)})
	s, err := New(Options{Clusters: manager, InCluster: true, Access: gate})
	if err != nil {
		t.Fatal(err)
	}
	clients := fake.NewSimpleClientset(secrets...)
	s.helm = helm.NewReader(oneCluster{client: &cluster.Client{Context: "prod", Clients: clients}}, gate)
	return s, reviews, clients
}

var revealIngress = api.HelmRevealQuery{Cluster: "prod", Namespace: "default", Name: "ingress", Revision: 2}

// helmNotesWithAPassword is what charts print in their `NOTES.txt`: how to
// reach the thing, and the admin password they generated.
const helmNotesWithAPassword = "Get the admin password with:\n  echo s3cr3t-admin-pw"

// helmReveals is the two doors, by the key each records, answering the part as
// text so one test can walk both.
var helmReveals = map[string]func(apiReads, api.HelmRevealQuery) (string, *api.Failure){
	"values": func(r apiReads, q api.HelmRevealQuery) (string, *api.Failure) {
		got, fail := r.RevealHelmValues(grace(), q)
		return got.Values, fail
	},
	"notes": func(r apiReads, q api.HelmRevealQuery) (string, *api.Failure) {
		got, fail := r.RevealHelmNotes(grace(), q)
		return got.Notes, fail
	},
}

// readTheReleases is whether anything listed the release Secrets.
func readTheReleases(clients *fake.Clientset) bool {
	for _, action := range clients.Actions() {
		if action.GetResource().Resource == "secrets" {
			return true
		}
	}
	return false
}

// captureLog is the process log for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })
	return &buf
}

func TestTheReleasePagesDefaultAnswerCarriesNoValues(t *testing.T) {
	s, _, _ := helmRevealServer(t, true, &revealSink{})
	detail, fail := apiReads{s}.HelmRelease(grace(), "prod", "default", "ingress")
	if fail != nil {
		t.Fatalf("the release page failed: %+v", fail.Reason)
	}
	if detail.Release.Revision != 2 || !detail.ValuesSupplied {
		t.Fatalf("revision %d, valuesSupplied %v; want 2 and true, or there is nothing to measure",
			detail.Release.Revision, detail.ValuesSupplied)
	}
	body := mustJSON(t, detail)
	if strings.Contains(body, "hunter2") || strings.Contains(body, "password") {
		t.Errorf("the default answer carries the supplied values: %s", body)
	}
}

// The notes must not go out on every view of the page either: charts print
// generated passwords in them. This fails if the answer carries `notes` with
// the password.
func TestTheReleasePagesDefaultAnswerCarriesNoNotes(t *testing.T) {
	s, _, _ := helmRevealServer(t, true, &revealSink{})
	detail, fail := apiReads{s}.HelmRelease(grace(), "prod", "default", "ingress")
	if fail != nil {
		t.Fatalf("the release page failed: %+v", fail.Reason)
	}
	body := mustJSON(t, detail)
	if strings.Contains(body, "s3cr3t-admin-pw") || strings.Contains(body, "admin password") {
		t.Errorf("the default answer carries the notes: %s", body)
	}
	if !detail.NotesRendered {
		t.Errorf("the default answer does not say the chart rendered notes: %s", body)
	}
}

func TestARevealOfHelmNotesAnswersThemAndIsRecordedOnce(t *testing.T) {
	sink := &revealSink{}
	said := captureLog(t)
	s, _, _ := helmRevealServer(t, true, sink)
	notes, fail := apiReads{s}.RevealHelmNotes(grace(), revealIngress)
	if fail != nil {
		t.Fatalf("the reveal failed: %+v", fail.Reason)
	}
	if notes.Revision != 2 || notes.Notes != helmNotesWithAPassword {
		t.Fatalf("the reveal answered %+v", notes)
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Phase != audit.Attempt || got[1].Phase != audit.Outcome || got[0].ID != got[1].ID {
		t.Fatalf("one reveal wrote %+v, want exactly one act: an attempt and its outcome", got)
	}
	for _, r := range got {
		if r.Act != audit.Reveal || r.Verb != "get" || r.Resource != "secrets" || r.Cluster != "prod" ||
			r.Namespace != "default" || r.Name != "sh.helm.release.v1.ingress.v2" || r.Key != "notes" ||
			r.Who != "grace@example.com" {
			t.Errorf("the record does not say who revealed which release's notes at which revision: %+v", r)
		}
	}
	if got[1].Outcome != audit.Applied {
		t.Errorf("the outcome is %q, want applied", got[1].Outcome)
	}
	if strings.Contains(sink.buf.String(), "s3cr3t") {
		t.Errorf("the notes reached the audit record:\n%s", sink.buf.String())
	}
	line := said.String()
	if strings.Count(line, "reveal: ") != 1 || !strings.Contains(line, "name=sh.helm.release.v1.ingress.v2 key=notes") ||
		!strings.Contains(line, "outcome=ok") || strings.Contains(line, "s3cr3t") {
		t.Errorf("the process log reads %q, want one reveal line naming the revision and no notes", line)
	}
}

// Both doors refuse the same way: a person refused the Secret, or a reveal
// whose record cannot be written, reads nothing.
func TestBothHelmRevealsReadNothingWhenRefusedOrUnrecorded(t *testing.T) {
	for key, reveal := range helmReveals {
		t.Run(key, func(t *testing.T) {
			sink := &revealSink{}
			s, _, clients := helmRevealServer(t, false, sink)
			text, fail := reveal(apiReads{s}, revealIngress)
			if fail == nil || fail.State != api.StateForbidden || text != "" {
				t.Fatalf("a person refused the Secret got %q %+v, want forbidden", text, fail)
			}
			got := sink.records(t)
			if len(got) != 2 || got[0].Decision != "denied" || got[1].Outcome != audit.Refused || got[0].Key != key {
				t.Fatalf("a denied reveal wrote %+v, want an attempt naming %q and a refused outcome", got, key)
			}
			if readTheReleases(clients) {
				t.Errorf("the release Secrets were read although the reveal was denied: %v", clients.Actions())
			}

			s, _, clients = helmRevealServer(t, true, &revealSink{broken: true})
			text, fail = reveal(apiReads{s}, revealIngress)
			if fail == nil || text != "" {
				t.Fatalf("an unrecorded reveal answered %q %+v", text, fail)
			}
			if readTheReleases(clients) {
				t.Errorf("the release Secrets were read although the reveal's record could not be written")
			}
		})
	}
}

// A chart that ships no notes is an answer: the page says false, and a reveal
// answers ok with nothing. A current revision whose document would not decode
// is not: the page says why, and the reveal answers unreadable, recorded as a
// failure.
func TestTheHelmNotesRevealTellsNoneFromUnread(t *testing.T) {
	plain, _, _ := helmRevealServerOf(t, true, &revealSink{},
		helmRevisionSecret(t, 1, "deployed", "Install complete", "", nil))
	detail, fail := apiReads{plain}.HelmRelease(grace(), "prod", "default", "ingress")
	if fail != nil || detail.NotesRendered || detail.PayloadNote != "" {
		t.Fatalf("a chart with no notes drew notesRendered %v, payloadNote %q, fail %+v",
			detail.NotesRendered, detail.PayloadNote, fail)
	}
	none, fail := apiReads{plain}.RevealHelmNotes(grace(), api.HelmRevealQuery{
		Cluster: "prod", Namespace: "default", Name: "ingress", Revision: 1})
	if fail != nil || none.Notes != "" || none.Revision != 1 {
		t.Errorf("a reveal of a chart with no notes answered %+v %+v, want ok and empty", none, fail)
	}

	broken := helmRevisionSecret(t, 2, "deployed", "Upgrade complete", helmNotesWithAPassword, nil)
	broken.Data["release"] = []byte("not base64 at all, and not a release either ***")
	sink := &revealSink{}
	unread, _, _ := helmRevealServerOf(t, true, sink,
		helmRevisionSecret(t, 1, "superseded", "Install complete", "hello", nil), broken)
	page, fail := apiReads{unread}.HelmRelease(grace(), "prod", "default", "ingress")
	if fail != nil || page.NotesRendered || page.PayloadNote == "" {
		t.Fatalf("an unreadable current revision drew notesRendered %v, payloadNote %q, fail %+v; "+
			"want the reason and no claim", page.NotesRendered, page.PayloadNote, fail)
	}
	got, fail := apiReads{unread}.RevealHelmNotes(grace(), api.HelmRevealQuery{
		Cluster: "prod", Namespace: "default", Name: "ingress", Revision: 2})
	if fail == nil || fail.State != api.StateUnreadable || got.Notes != "" {
		t.Errorf("a reveal of notes nothing could read answered %+v %+v, want unreadable", got, fail)
	}
	if records := sink.records(t); len(records) != 2 || records[1].Outcome != audit.Failed {
		t.Errorf("the reveal of unreadable notes wrote %+v, want an attempt and a failed outcome", records)
	}
}

func TestARevealOfHelmValuesAnswersThemAndIsRecordedOnce(t *testing.T) {
	sink := &revealSink{}
	said := captureLog(t)
	s, _, _ := helmRevealServer(t, true, sink)
	values, fail := apiReads{s}.RevealHelmValues(grace(), revealIngress)
	if fail != nil {
		t.Fatalf("the reveal failed: %+v", fail.Reason)
	}
	if values.Revision != 2 || !strings.Contains(values.Values, "password: hunter2") {
		t.Fatalf("the reveal answered %+v", values)
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Phase != audit.Attempt || got[1].Phase != audit.Outcome || got[0].ID != got[1].ID {
		t.Fatalf("one reveal wrote %+v, want exactly one act: an attempt and its outcome", got)
	}
	for _, r := range got {
		if r.Act != audit.Reveal || r.Verb != "get" || r.Resource != "secrets" || r.Namespace != "default" ||
			r.Name != "sh.helm.release.v1.ingress.v2" || r.Key != "values" || r.Who != "grace@example.com" {
			t.Errorf("the record does not say who revealed which release's revision: %+v", r)
		}
	}
	if got[1].Outcome != audit.Applied {
		t.Errorf("the outcome is %q, want applied", got[1].Outcome)
	}
	if strings.Contains(sink.buf.String(), "hunter2") {
		t.Errorf("the value reached the audit record:\n%s", sink.buf.String())
	}
	line := said.String()
	if strings.Count(line, "reveal: ") != 1 || !strings.Contains(line, "name=sh.helm.release.v1.ingress.v2 key=values") ||
		!strings.Contains(line, "outcome=ok") || strings.Contains(line, "hunter2") {
		t.Errorf("the process log reads %q, want one reveal line naming the revision and no value", line)
	}
}

func TestARefusedRevealOfHelmValuesReadsNothingAndIsRecorded(t *testing.T) {
	sink := &revealSink{}
	s, _, clients := helmRevealServer(t, false, sink)
	values, fail := apiReads{s}.RevealHelmValues(grace(), revealIngress)
	if fail == nil || fail.State != api.StateForbidden || values.Values != "" {
		t.Fatalf("a person refused the Secret got %+v %+v, want forbidden", values, fail)
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Decision != "denied" || got[1].Outcome != audit.Refused {
		t.Fatalf("a denied reveal wrote %+v, want an attempt and a refused outcome", got)
	}
	if readTheReleases(clients) {
		t.Errorf("the release Secrets were read although the reveal was denied: %v", clients.Actions())
	}
}

func TestARevealOfHelmValuesWhoseRecordCannotBeWrittenReadsNothing(t *testing.T) {
	s, _, clients := helmRevealServer(t, true, &revealSink{broken: true})
	values, fail := apiReads{s}.RevealHelmValues(grace(), revealIngress)
	if fail == nil || values.Values != "" {
		t.Fatalf("an unrecorded reveal answered %+v %+v", values, fail)
	}
	if readTheReleases(clients) {
		t.Errorf("the release Secrets were read although the reveal's record could not be written")
	}
}

func TestARevealOfAnOlderRevisionIsAbsent(t *testing.T) {
	sink := &revealSink{}
	s, _, _ := helmRevealServer(t, true, sink)
	old := revealIngress
	old.Revision = 1
	values, fail := apiReads{s}.RevealHelmValues(grace(), old)
	if fail == nil || fail.State != api.StateAbsent || values.Values != "" {
		t.Fatalf("a reveal of revision 1 when 2 is current answered %+v %+v, want absent", values, fail)
	}
	if !strings.Contains(fail.Reason.Message, "revision 2") {
		t.Errorf("the refusal does not say which revision is current: %q", fail.Reason.Message)
	}
	if got := sink.records(t); len(got) != 2 || got[1].Outcome != audit.Failed {
		t.Errorf("the reveal of a moved-on release wrote %+v, want an attempt and a failed outcome", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
