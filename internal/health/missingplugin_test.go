package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// A credential plugin that is not installed, reported as what it is.
//
// # What it is not
//
// A GUI-launched process on macOS inherits launchd's PATH and not a shell's, so
// `/opt/homebrew/bin` is invisible to it while a login shell re-derives it (see
// internal/binpath). **A missing plugin does not print "unauthorized", and the
// classifier does not shadow it.** Both are asserted rather than assumed,
// because a confident wrong diagnosis here sends somebody to an IAM policy for a
// fault in their shell. The arm that matches this failure is the last one in the
// text switch, so nothing above it can swallow it, and the message client-go
// produces contains no permissions vocabulary at all.
//
// What the sentence must do is say **where it looked**, not only that the
// plugin "could not be run": that is the one fact that turns this from a
// mystery into an install command.

// observedMissingPlugin is client-go's own wording, captured by standing up a
// real client against a real exec block naming a binary that does not exist.
// The end-to-end test below re-derives it every run, which is what
// keeps missingPlugin's regexp honest if client-go ever rephrases it.
const observedMissingPlugin = `Get "https://example.eks.amazonaws.com/version?timeout=32s": getting credentials: exec: executable aws not found`

func TestAMissingPluginNamesTheBinaryAndWhereItLooked(t *testing.T) {
	// Stand in for a macOS window opened from the Finder: launchd's PATH, which
	// has none of the places these tools install into.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	state, detail, _ := classify(errors.New(observedMissingPlugin))

	if state != StateMissing {
		t.Errorf("state = %q, want %q", state, StateMissing)
	}
	// The binary the *kubeconfig* asked for, not a guess at which one it should
	// have been: a cluster can authenticate through `gke-gcloud-auth-plugin` or
	// `kubelogin` just as easily, and naming `aws` for either would be a lie
	// with an install command attached.
	if !strings.Contains(detail, "aws") {
		t.Errorf("detail = %q and never names the executable the kubeconfig asked for", detail)
	}
	// And the PATH, which is the whole point. Without it the operator is told
	// a program is missing while looking at a terminal in which it plainly is
	// not — see the package-level comment above.
	if !strings.Contains(detail, "/usr/sbin") {
		t.Errorf("detail = %q and does not say which PATH was searched, which is the one "+
			"fact that distinguishes `not installed` from `this process cannot see it`", detail)
	}
}

// The property this file is for: a **missing binary** must never be reported as
// a **permissions** problem.
//
// Classify on the reason: not a destination standing in for a diagnosis, and
// not one kind of failure standing in for another. The cost is the same — the health page names which of three people
// fixes a row, and "your cloud administrator" for a program that is not installed
// is a ticket nobody can close.
func TestAMissingPluginIsNotReportedAsAPermissionsProblem(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")

	state, detail, _ := classify(errors.New(observedMissingPlugin))

	for _, wrong := range []string{StateSSO, StateIAM, StateKO, StatePlugin} {
		if state == wrong {
			t.Errorf("state = %q for a binary that does not exist", state)
		}
	}
	// The words too, not only the state: the sentence is what the operator reads.
	for _, word := range []string{"unauthorized", "expired", "sso", "assume", "iam",
		"forbidden", "credentials are valid"} {
		if strings.Contains(strings.ToLower(detail), word) {
			t.Errorf("detail = %q contains %q, which points at a permissions problem for a "+
				"failure that never reached the cluster", detail, word)
		}
	}
}

// Nothing above this arm may reach it first, and this arm may not reach past
// itself.
//
// Two halves, because they fail differently. The first is the shadowing check
// every ordered switch over text needs. The second is the one the arm itself
// could plausibly break: it matches four broad needles — "executable",
// "no such file", "exec plugin", "not found" — and only client-go's own phrasing
// carries a binary name in a known position, so everything else must keep the
// vaguer sentence rather than have a name invented for it.
func TestTheMissingPluginArmShadowsNothingAndInventsNoName(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")

	// Half one: the arms above still win where they should.
	for _, c := range []struct {
		what    string
		message string
		want    string
	}{
		// The pair that share the word "executable" and must not share an
		// answer: one ran, one was never there.
		{"a plugin that ran and refused",
			`exec: executable aws failed with exit code 255`, StatePlugin},
		{"a lapsed session mentioning the plugin",
			`getting credentials: Error loading SSO Token: Token has expired`, StateSSO},
		{"a refused role", `An error occurred (AccessDenied) when calling the AssumeRole`, StateIAM},
	} {
		if state, _, _ := classify(errors.New(c.message)); state != c.want {
			t.Errorf("%s classified %q, want %q", c.what, state, c.want)
		}
	}

	// Half two: the broad needles keep the vaguer sentence and no name is guessed.
	for _, message := range []string{
		`open /home/operator/.kube/ca.crt: no such file or directory`,
		`exec plugin: invalid apiVersion "client.authentication.k8s.io/v1alpha1"`,
	} {
		state, detail, _ := classify(errors.New(message))
		if state == StateMissing {
			t.Errorf("%q was reported as a missing plugin, and it names no binary — so the "+
				"page would say `authenticates through` with nothing to authenticate through",
				message)
		}
		if strings.Contains(detail, "cannot find it") {
			t.Errorf("detail = %q for %q, borrowing a sentence written for a different failure",
				detail, message)
		}
	}

	// Half three: the typed predicates below the whole text switch are still
	// reachable, which is what an arm at the bottom of an ordered switch is in a
	// position to break.
	unauthorized := apierrors.NewUnauthorized("Unauthorized")
	if state, _, _ := classify(unauthorized); state != StateSSO {
		t.Errorf("a typed 401 classified %q, want %q — the text arms have swallowed the "+
			"predicates below them", state, StateSSO)
	}
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Resource: "pods"}, "x", errors.New("nope"))
	if state, _, _ := classify(forbidden); state != StateKO {
		t.Errorf("a typed 403 classified %q, want %q", state, StateKO)
	}
}

// A process with no PATH at all says so, rather than printing "Searched: " and
// stopping — which reads as a rendering fault and invites a bug report about the
// page instead of about the machine.
func TestTheSearchedPathIsSaidEvenWhenThereIsNone(t *testing.T) {
	t.Setenv("PATH", "")

	_, detail, _ := classify(errors.New(observedMissingPlugin))

	if !strings.Contains(detail, "no PATH") {
		t.Errorf("detail = %q and does not distinguish an empty PATH from a short one", detail)
	}
}

// The end-to-end shape, and the reason it is worth its cost: **it re-derives
// client-go's own wording every run.**
//
// `missingPlugin`'s regexp is pinned to one format string in client-go
// 0.37.0's `wrapCmdRunErrorLocked`. A test written against a constant would keep
// passing for ever after client-go rephrased it, and the failure would be silent
// — the health page would quietly go back to the vaguer sentence with nothing
// saying why. This drives a real `kubernetes.Clientset` through a real exec
// block naming a binary that does not exist, so a rephrasing breaks here.
//
// It also records what the *other* pages print: internal/web's list and
// overview pages pass this error through `textutil.FirstLineN` unclassified, so
// what reaches the screen there is client-go's sentence verbatim — and it says
// "not found", never "unauthorized".
func TestClientGoStillSaysExecutableNotFoundAndTheClassifierStillReadsIt(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"major":"1","minor":"30"}`))
	}))
	defer srv.Close()

	// A name no machine has, so this cannot pass by finding something.
	const plugin = "kt-no-such-credential-plugin"

	cfg := &rest.Config{Host: srv.URL}
	cfg.ExecProvider = &clientcmdapi.ExecConfig{
		Command:         plugin,
		APIVersion:      "client.authentication.k8s.io/v1beta1",
		Args:            []string{"eks", "get-token"},
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}

	_, err = clientset.Discovery().ServerVersion()
	if err == nil {
		t.Fatal("the request succeeded, so no credential plugin was ever run")
	}

	// The wording the regexp depends on.
	if !strings.Contains(err.Error(), "executable "+plugin+" not found") {
		t.Fatalf("client-go no longer says `executable <name> not found`; it says:\n%s\n"+
			"missingPlugin's regexp in health.go is pinned to that phrasing and must move with it",
			err.Error())
	}
	// And the classifier still reads it.
	state, detail, _ := classify(err)
	if state != StateMissing {
		t.Errorf("a real missing plugin classified %q, want %q\n  from: %s", state, StateMissing, err)
	}
	if !strings.Contains(detail, plugin) {
		t.Errorf("detail = %q and does not name %q", detail, plugin)
	}
}
