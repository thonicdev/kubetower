package web

// What a Secret's Manifest panel carries, and
// what the reveal door writes down.
//
// `detail.Trimmed` masks the values and `internal/detail/data_test.go` pins that.
// What is pinned here is the two things the panel adds on top: that no rendering
// of the document - `raw` or the tokens - lets a value through, and that the
// panel tells the editor which keys it masked, so the editor's reveal can ask
// for exactly those through the one door and never has to parse the document to
// find out.

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/identity"
)

// planted is a value that must never reach the wire. It is a base64 string
// because that is what a Secret holds, and it is one no fixture elsewhere uses.
const planted = "cGxhbnRlZC1jcmVkZW50aWFs"

func plantedSecret(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	raw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "db", "namespace": "shop", "resourceVersion": "7"},
		"type":     "Opaque",
		"data": map[string]any{
			"password": planted,
			"username": "YWRtaW4=",
			// Not UTF-8 once decoded, so not something the door can answer with
			// text; the panel must not promise the editor a reveal for it.
			"keystore.jks": "/v8AAQ==",
		},
	}}
	return raw
}

// The object as `Reader.Get` would hand it to the panel: the document through
// `detail.Trimmed`, the data block through the same reading `Data` uses. Built
// by hand here because the panel is driven without a cluster, and built from
// the *exported* halves so the fixture cannot hold a document Trimmed would not
// have produced.
func plantedObject(t *testing.T) detail.Object {
	t.Helper()
	raw := plantedSecret(t)
	document, err := yaml.Marshal(detail.Trimmed(raw).Object)
	if err != nil {
		t.Fatalf("rendering the trimmed object: %v", err)
	}
	return detail.Object{
		Kind: "secrets", Title: "Secret", Name: "db", Namespace: "shop", ResourceVersion: "7",
		YAML: string(document),
		Data: &detail.DataSet{
			Kind: "Secret", Secret: true, Type: "Opaque", ResourceVersion: "7",
			Entries: []detail.DataEntry{
				{Key: "keystore.jks", Field: detail.FieldData, Secret: true, Encoded: true, Binary: true},
				{Key: "password", Field: detail.FieldData, Secret: true, Encoded: true},
				{Key: "username", Field: detail.FieldData, Secret: true, Encoded: true},
			},
		},
	}
}

func TestASecretsManifestPanelCarriesNoValueAndNamesWhatItMasked(t *testing.T) {
	panel := manifestPanelOf(plantedObject(t), true, "")

	if strings.Contains(panel.Raw, planted) {
		t.Fatalf("the planted value is in the document the editor is given:\n%s", panel.Raw)
	}
	for _, line := range panel.Lines {
		for _, token := range line.Tokens {
			if strings.Contains(token.Text, planted) {
				t.Fatalf("the planted value is in a token on line %d: %q", line.Number, token.Text)
			}
		}
	}
	// The keys survive - a Secret rendered without them is indistinguishable
	// from an empty one - and each carries the mask.
	if !strings.Contains(panel.Raw, "password: "+detail.SecretValueMask) {
		t.Errorf("the key is not there with its mask:\n%s", panel.Raw)
	}

	// What the editor may ask the door for: the text keys, sorted as the Data
	// tab sorts them, and not the binary one - the door answers text, and a
	// reveal it cannot honor is a control that fails after the click.
	if got := strings.Join(panel.MaskedKeys, ","); got != "password,username" {
		t.Errorf("maskedKeys = %q, want password,username", got)
	}
}

// A ConfigMap masks nothing and says so by saying nothing: an empty list is
// what tells the editor there is no reveal to offer.
func TestAConfigMapsManifestPanelNamesNoMaskedKey(t *testing.T) {
	object := detail.Object{
		Kind: "configmaps", Title: "ConfigMap", Name: "settings", Namespace: "shop",
		YAML: "apiVersion: v1\ndata:\n  LOG_LEVEL: debug\nkind: ConfigMap\n",
		Data: &detail.DataSet{Kind: "ConfigMap", Entries: []detail.DataEntry{
			{Key: "LOG_LEVEL", Field: detail.FieldData, Inline: "debug", Drawn: true},
		}},
	}
	if panel := manifestPanelOf(object, true, ""); len(panel.MaskedKeys) != 0 {
		t.Errorf("a ConfigMap's panel claims masked keys: %v", panel.MaskedKeys)
	}
	// And a kind with no data block at all.
	if panel := manifestPanelOf(detail.Object{Kind: "pods", YAML: "kind: Pod\n"}, true, ""); len(panel.MaskedKeys) != 0 {
		t.Errorf("a pod's panel claims masked keys: %v", panel.MaskedKeys)
	}
}

// --- the audit line ------------------------------------------------------------

// Revealing is an act with a subject, and the contract says so of the door:
// "in a shared profile it is a line in the audit log like any other". This is
// the line, in the shape
// `internal/edit` already writes for a write (`write: cluster=... fields=...`),
// so one grep finds every act this process has taken on a Secret.
//
// It carries the key and the outcome and never the value: an audit
// that copies a Secret's value defeats the masking through the back door.
func TestTheRevealLineNamesTheKeyAndTheOutcomeAndNeverAValue(t *testing.T) {
	q := api.EnvQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"},
		Key:       "password", Container: "api",
	}
	line := revealLine(context.Background(), q, access.Allowed, nil)
	for _, want := range []string{"reveal:", `cluster="prod"`, "kind=secrets", "namespace=shop", "name=db", "key=password", "decision=allowed", "outcome=ok"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lacks %s: %q", want, line)
		}
	}
	// Nothing was refused, so there is no reason to give, and the field is absent
	// rather than empty: `reason=` with nothing after it is a question a reader
	// has to go and answer.
	if strings.Contains(line, "reason=") {
		t.Errorf("a successful reveal was given a reason: %q", line)
	}
	// The line is built from the query alone, which holds no value by
	// construction - the type has no field for one. Said as an assertion so a
	// later signature taking the answer cannot quietly start printing it.
	if strings.Contains(line, "value") {
		t.Errorf("the line mentions a value: %q", line)
	}
}

// The door writes its line whatever the outcome. A record saying *alice
// asked* without saying whether it happened does not answer the question it
// will be asked - and a refused reveal is the one an audit reader is looking
// for. Driven through the real reader against a cluster nothing listens on, so
// the read fails and the line has to be written on the failure path.
func TestARefusedRevealStillWritesItsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	body := `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context: {cluster: nowhere, user: nobody}
users:
- name: nobody
  user: {token: none}
current-context: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing a kubeconfig: %v", err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("loading the kubeconfig: %v", err)
	}

	var captured bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&captured)
	t.Cleanup(func() { log.SetOutput(previous) })

	reads := apiReads{&Server{clusters: clusters, objects: detail.NewReader(clusters, nil)}}
	_, fail := reads.RevealEnv(context.Background(), api.EnvQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"},
		Key:       "password",
	})
	if fail == nil {
		t.Fatal("a reveal against a cluster nothing listens on succeeded; the fixture is wrong")
	}

	lines := captured.String()
	if !strings.Contains(lines, "reveal: ") || !strings.Contains(lines, "key=password") {
		t.Fatalf("the refused reveal wrote no line; the record says nothing happened:\n%s", lines)
	}
	if strings.Contains(lines, "outcome=ok") {
		t.Errorf("a refused reveal was recorded as ok:\n%s", lines)
	}
}

// --- the door reveals text, and says so about anything else -------------------

// plantedSet is the data block of `plantedSecret` as `detail.Reader.Data` hands
// it over with one key revealed: the measurements on every entry, the value on
// the one that was asked for, and nothing on the rest.
//
// Built by hand for the reason `plantedObject` is: `internal/web` holds a
// concrete `*detail.Reader` and cannot be given a double, so a fixture is the
// only way into the mapper at all. What that costs is that `Binary` is asserted
// here rather than derived - the flag's meaning is pinned where it is computed,
// by `internal/detail`'s TestBinaryValuesAreNamedRatherThanPrinted, over this
// same `/v8AAQ==`.
func plantedSet(revealed string) detail.DataSet {
	set := detail.DataSet{
		Kind: "Secret", Secret: true, Type: "Opaque", ResourceVersion: "7",
		Entries: []detail.DataEntry{
			// 0xFE 0xFF 0x00 0x01 - four bytes, one of them a NUL, and not UTF-8.
			{Key: "keystore.jks", Field: detail.FieldData, Secret: true, Encoded: true,
				Binary: true, Size: 4, Stored: len("/v8AAQ==")},
			// A Secret key holding the empty string. Real, ordinary, and the answer
			// the refusal above must not be confused with.
			{Key: "empty", Field: detail.FieldData, Secret: true, Encoded: true},
			{Key: "password", Field: detail.FieldData, Secret: true, Encoded: true,
				Size: len("planted-credential"), Stored: len(planted)},
		},
	}
	for i := range set.Entries {
		if set.Entries[i].Key == revealed && !set.Entries[i].Binary {
			set.Entries[i].Value = map[string]string{"password": "planted-credential", "empty": ""}[revealed]
			set.Revealed = revealed
		}
	}
	return set
}

// The door reveals text. Asked for a key whose value is not text it
// must not answer `{key, value: ""}` - a **revealed empty value**, which the contract
// calls "a real and confusing thing to meet in a Secret" and which the Data tab
// draws identically to a key somebody emptied. `revealInto` skips a binary entry,
// so `entry.Value` is the empty string and the mapper must not send it inside an
// `ok` envelope, the one arm that means *this is the value*.
//
// It is refused instead, and the refusal is a state and a code rather than a
// sentence a client has to read: `unreadable` / `undecodable`, which is the
// pairing this package already uses for a payload it holds and cannot decode.
func TestRevealingABinaryKeyIsRefusedRatherThanAnsweredEmpty(t *testing.T) {
	scope := api.Scope{Cluster: "prod", Resource: "secrets", Namespace: "shop", Name: "db"}

	value, fail := revealedValueOf(plantedSet("keystore.jks"), "keystore.jks", scope)
	if fail == nil {
		t.Fatalf("a binary key was revealed as %+v; an empty value that is not one is the defect", value)
	}
	if value != (api.EnvValue{}) {
		t.Errorf("the refusal came with a payload: %+v", value)
	}
	if fail.State != api.StateUnreadable {
		t.Errorf("state = %q, want %q", fail.State, api.StateUnreadable)
	}
	if fail.Reason.Code != api.CodeUndecodable {
		t.Errorf("reason code = %q, want %q - a client branches on the code and never on the prose",
			fail.Reason.Code, api.CodeUndecodable)
	}
	if !strings.Contains(fail.Reason.Message, "not text") {
		t.Errorf("the message does not say what is wrong: %q", fail.Reason.Message)
	}
	if fail.Reason.Retryable == nil || *fail.Reason.Retryable {
		t.Error("a binary value is offered a retry button; asking again produces the same bytes")
	}
	if fail.Reason.Scope == nil || fail.Reason.Scope.Name != "db" {
		t.Errorf("the refusal does not say what was asked: %+v", fail.Reason.Scope)
	}

	// The answer the refusal must not have swallowed. A Secret key holding the
	// empty string is a real thing, it is `ok` with an empty value, and telling
	// the two apart is the whole point of refusing rather than answering.
	blank, fail := revealedValueOf(plantedSet("empty"), "empty", scope)
	if fail != nil {
		t.Fatalf("a genuinely empty value was refused: %+v", fail.Reason)
	}
	if blank.Key != "empty" || blank.Value != "" {
		t.Errorf("the revealed empty value came back as %+v", blank)
	}

	// And the ordinary case still answers, with the value and not the key.
	shown, fail := revealedValueOf(plantedSet("password"), "password", scope)
	if fail != nil {
		t.Fatalf("a text key was refused: %+v", fail.Reason)
	}
	if shown.Value != "planted-credential" {
		t.Errorf("value = %q, want the decoded value", shown.Value)
	}
	if shown.Source != "Secret" {
		t.Errorf("source = %q, want the kind that holds it", shown.Source)
	}

	// A key that is not there is a third answer again, and not this one.
	if _, fail = revealedValueOf(plantedSet(""), "no-such-key", scope); fail == nil || fail.State != api.StateAbsent {
		t.Errorf("an absent key is not %v", fail)
	}
}

// The record has to survive the new branch: a line that says *alice asked*
// without saying whether it happened does not answer the question it will be
// asked - and `outcome=unreadable` alone puts "this key is not text" in the same
// bucket as "the cluster stopped answering", which are the door working and an
// incident respectively.
func TestTheLineOfARefusedBinaryRevealSaysWhichRefusalItWas(t *testing.T) {
	q := api.EnvQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"},
		Key:       "keystore.jks", Container: "api",
	}
	_, fail := revealedValueOf(plantedSet("keystore.jks"), q.Key, api.Scope{Cluster: "prod"})
	line := revealLine(context.Background(), q, access.Allowed, fail)

	for _, want := range []string{"reveal:", "key=keystore.jks", "outcome=unreadable", "reason=undecodable", "container=api"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lacks %s: %q", want, line)
		}
	}
	if strings.Contains(line, "outcome=ok") {
		t.Errorf("a refused reveal was recorded as ok: %q", line)
	}
	// The code and never the message: a message is prose and a later one could
	// quote the value it is refusing to show.
	if strings.Contains(line, "not text") {
		t.Errorf("the line copies the refusal's prose: %q", line)
	}
}

// TestARevealLineCannotForgeAWriteLine: every name on the reveal line comes from
// the query string, and a key holding a newline must not end the line and begin
// a standalone `write:` line attributed to anybody. The line is built through
// identity.Line: one line whatever the names hold, ending with the real
// `who=` and `via=` of the person who asked.
func TestARevealLineCannotForgeAWriteLine(t *testing.T) {
	ctx := identity.With(context.Background(), identity.Who{Name: "mallory@example.com", Method: identity.SSO})
	q := api.EnvQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop\rx", Name: "db y"},
		Key:       "x\nwrite: cluster=\"prod\" kind=secrets name=db who=\"ceo@corp.example\" via=sso",
		Container: "api\u0085z",
	}
	line := revealLine(ctx, q, access.Allowed, nil)
	if strings.ContainsAny(line, "\n\r \u0085") {
		t.Fatalf("the reveal line carries a raw line break, so it is two lines: %q", line)
	}
	if !strings.HasPrefix(line, "reveal: ") {
		t.Errorf("the line does not start as a reveal: %q", line)
	}
	if want := ` who="mallory@example.com" via=sso`; !strings.HasSuffix(line, want) {
		t.Errorf("the reveal line does not end with the person who asked (%s): %q", want, line)
	}
}
