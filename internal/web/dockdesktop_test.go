package web

import (
	"errors"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// The desktop terminal opens a window pointed at the cluster it was asked for,
// and hands back no WebSocket address for a native OS window.
//
// `apiDock.OpenDesktopTerminal` must not call `OpenDesktopTerminal("", "", "",
// "")` with an empty cluster, nor answer `Channel: "/api/ws/assistant"`.
// Both are silent: a window pointed at nothing opens and looks right, and it is
// on whatever context the operator's own kubeconfig happens to name, which on a
// large estate is the difference between reading a log and restarting the wrong
// deployment.
//
// The window's whole point is a `KUBECONFIG` set to one cluster and to nothing
// else — that is what the template's own confirmation says to the operator, at
// `desktopterm.go`.
func TestTheDesktopTerminalIsPointedAtTheClusterItWasAskedFor(t *testing.T) {
	fake := &fakeDesktop{supported: true}
	dock := apiDock{&Server{claude: fake}}

	session, err := dock.OpenDesktopTerminal(api.DesktopTerminalRequest{
		Cluster: "prod-eks", Namespace: "argocd", Kind: "pods", Name: "argocd-server-1",
	})
	if err != nil {
		t.Fatalf("opening a desktop terminal: %v", err)
	}

	if fake.cluster != "prod-eks" {
		t.Errorf("the window was opened on %q, want %q: the cluster reached the terminal "+
			"layer as the empty string and the window pointed at nothing", fake.cluster, "prod-eks")
	}
	if fake.namespace != "argocd" || fake.kind != "pods" || fake.name != "argocd-server-1" {
		t.Errorf("the selection did not travel: namespace=%q kind=%q name=%q",
			fake.namespace, fake.kind, fake.name)
	}

	// No channel. This is an application window on the operator's machine, not a
	// socket — and `/api/ws/assistant` is served by nothing in any case.
	if session.Channel != "" {
		t.Errorf("channel is %q on a native window; a client told to dial that reaches "+
			"a 404 and reports the window as broken", session.Channel)
	}
	if session.ID == "" || session.OpenedAt == nil {
		t.Errorf("the session says nothing about itself: %+v", session)
	}
}

// A window with no cluster is refused rather than opened. Opening one is worse
// than refusing, because it looks right.
func TestADesktopTerminalWithNoClusterIsRefusedRatherThanPointedAtNothing(t *testing.T) {
	fake := &fakeDesktop{supported: true}
	if _, err := (apiDock{&Server{claude: fake}}).OpenDesktopTerminal(
		api.DesktopTerminalRequest{}); err == nil {
		t.Fatal("a window with no cluster was opened")
	} else if !errors.Is(err, api.ErrRefused) {
		t.Errorf("refused as %v, want api.ErrRefused", err)
	}
	if fake.opened {
		t.Error("the terminal layer was asked to open a window anyway")
	}
}

// A build that cannot open one says so rather than hiding the control. Every
// false in Capabilities is also refused at its own endpoint; hiding a button is
// not a control.
func TestABuildThatCannotOpenAWindowRefusesAtTheEndpoint(t *testing.T) {
	_, err := (apiDock{&Server{claude: &fakeDesktop{supported: false}}}).OpenDesktopTerminal(
		api.DesktopTerminalRequest{Cluster: "prod-eks"})
	if !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("answered %v, want api.ErrNotInProfile", err)
	}
}

// fakeDesktop records what reached the terminal layer. Only the two methods this
// operation uses are real; the rest satisfy the interface.
type fakeDesktop struct {
	Terminals
	supported bool
	opened    bool
	cluster   string
	namespace string
	kind      string
	name      string
}

func (f *fakeDesktop) DesktopTerminalSupported() bool { return f.supported }

func (f *fakeDesktop) OpenDesktopTerminal(cluster, namespace, kind, name string) error {
	f.opened = true
	f.cluster, f.namespace, f.kind, f.name = cluster, namespace, kind, name
	return nil
}
