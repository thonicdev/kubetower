package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// The state directory the deployment says is wiped at every restart.
//
// The console cannot see a volume - an emptyDir and a PersistentVolumeClaim are
// both a directory that answers every write - so it is told, through
// `KT_STATE_PERSISTENT`, and `SetupInfo.StateEphemeral` is where that reaches
// this package. What it must do with it is take away what could not survive a
// restart, and nothing else: the archive, and the one other setting kept on that
// disk, the node-shell image.
//
// # What each assertion guards
//
//   - `fillEphemeralArchive` must run (settingsOf falling through to
//     fillArchiveSettings would read `ok` with a retention of 21 days
//     on a disk the deployment says is wiped);
//   - `&& !s.setup.StateEphemeral` must stay in `offered`: otherwise the node-shell image
//     is offered as a setting;
//   - `StateEphemeral` must be handed to `api.Deps` in apimount.go: otherwise `PUT
//     /api/settings` is still registered and answers the mutation guard's 403
//     instead of the contract's 404;
//   - `POST /api/settings/clear` must be registered inside the
//     `!a.deps.StateEphemeral` block in internal/api's router, or it answers the
//     guard's 403 where the contract's 404 is wanted.

// serverWithState is serverFor with an archive behind it, so that what these
// tests measure is the state directory rather than a build assembled without an
// archive.
func serverWithState(t *testing.T, ephemeral bool) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: remote
  cluster: {server: https://127.0.0.1:1}
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
	server, err := New(Options{
		Clusters: manager,
		Terminal: shellsPresent{},
		Archive:  filledArchive(),
		Setup:    SetupInfo{StateDir: "/var/lib/kubetower", StateEphemeral: ephemeral},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

func TestAnEphemeralStateDirectoryOffersNoArchive(t *testing.T) {
	out := settingsOf(serverWithState(t, true))

	if out.Info.StatePersistent {
		t.Error("info.statePersistent is true on a state directory the deployment says is wiped")
	}
	if got := out.Archive.State(); got != api.StateAbsent {
		t.Errorf("archive = %q, want %q: a window of events on a disk wiped at the next start "+
			"is a promise nothing keeps", got, api.StateAbsent)
	}
	reason := out.Archive.Reason()
	if reason == nil {
		t.Fatal("the archive is absent and does not say why")
	}
	// **Not the sentence a build with no archive says.** That one is a missing
	// feature; this one is a missing volume, and the person acts on the
	// difference.
	if reason.Code != api.CodeNotSupportedHere {
		t.Errorf("reason code = %q, want %q", reason.Code, api.CodeNotSupportedHere)
	}
	if !strings.Contains(reason.Message, "does not survive a restart") {
		t.Errorf("the reason does not name the state directory: %q", reason.Message)
	}
	if out.General.ArchiveEnabled != nil || out.General.CaptureLogs != nil || out.General.RetentionDays != nil {
		t.Errorf("general carries the archive's settings over an archive that is absent: %+v", out.General)
	}
	if out.RetentionChoices != nil {
		t.Errorf("retentionChoices = %v; there is no retention to choose", out.RetentionChoices)
	}
	if out.NodeShell.Available || out.NodeShell.Enabled || out.NodeShell.Image != "" {
		t.Errorf("the node-shell image is offered as a setting on a disk that forgets it: %+v", out.NodeShell)
	}
}

// TestAPersistentStateDirectoryIsUnchanged protects every deployment that did
// not ask for any of this: the desktop, which is told nothing, and the chart
// with persistence on.
func TestAPersistentStateDirectoryIsUnchanged(t *testing.T) {
	out := settingsOf(serverWithState(t, false))

	if !out.Info.StatePersistent {
		t.Error("info.statePersistent is false on a directory nobody said was ephemeral")
	}
	if got := out.Archive.State(); got != api.StateOK {
		t.Errorf("archive = %q, want %q", got, api.StateOK)
	}
	if out.General.RetentionDays == nil || *out.General.RetentionDays != 21 {
		t.Errorf("retentionDays = %v, want 21 read back from the archive", out.General.RetentionDays)
	}
	if len(out.RetentionChoices) == 0 {
		t.Error("no retention choices on a persistent archive")
	}
	if !out.NodeShell.Available {
		t.Error("the desktop lost its node-shell image setting")
	}
}

// TestTheWritesFollowTheStateDirectory is the wiring from this package to the
// router, which `internal/api`'s own test cannot see: it builds its Deps by
// hand. Clear is among them: it discards what the state directory keeps, and
// there it keeps nothing.
//
// Served rather than walked, because `Mount` puts the whole contract behind
// one `/api/` pattern. The two answers are distinguishable without a session:
// a registered mutation meets the guard first and answers 403 for the missing
// marker; an unregistered one answers the contract's 404.
func TestTheWritesFollowTheStateDirectory(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		mux := http.NewServeMux()
		serverWithState(t, ephemeral).mountAPI(mux)

		for _, address := range []struct{ method, path string }{
			{http.MethodPut, "/api/settings"},
			{http.MethodPut, "/api/settings/nodeshell"},
			{http.MethodPost, "/api/settings/clear"},
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(address.method, address.path, strings.NewReader("{}")))
			want := http.StatusForbidden
			if ephemeral {
				want = http.StatusNotFound
			}
			if rec.Code != want {
				t.Errorf("ephemeral=%v: %s %s answered %d, want %d (%s)",
					ephemeral, address.method, address.path, rec.Code, want, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
}

// Emptying the archive, asked of the handler where the state directory is wiped
// at every restart, says *that* - and not "this build has no archive":
// internal/launch opens no archive there, so a nil check would speak
// first and name a missing feature where the person is missing a volume.
//
// **No request reaches this**: the address is not registered there
// (TestTheWritesFollowTheStateDirectory). What this still holds is the second
// line behind that: were the route ever registered again - a mis-resolved merge
// in the router is the likely way - the act refuses with the right sentence and
// does not touch an archive, rather than emptying one on a disk nothing keeps.
//
// With an archive behind it as well as without: the production build has none,
// and one that had would still be a window on a disk nothing keeps, so the act
// must not reach it.
func TestClearingTheArchiveOnAnEphemeralStateDirectorySaysWhy(t *testing.T) {
	for _, withArchive := range []bool{false, true} {
		s := serverWithState(t, true)
		stub := filledArchive()
		s.archive = nil
		if withArchive {
			s.archive = stub
		}
		settings := apiSettings{s}

		_, previewErr := settings.PreviewClear("archive")
		_, clearErr := settings.Clear("archive")
		for name, err := range map[string]error{"preview": previewErr, "clear": clearErr} {
			if !errors.Is(err, api.ErrNotInProfile) {
				t.Errorf("archive=%v: %s answered %v, want ErrNotInProfile", withArchive, name, err)
				continue
			}
			if strings.Contains(err.Error(), "this build has no archive") ||
				!strings.Contains(err.Error(), "does not survive a restart") {
				t.Errorf("archive=%v: %s says %q, want the state directory's reason", withArchive, name, err)
			}
		}
		if stub.cleared != 0 {
			t.Errorf("archive=%v: the archive was emptied %d times on a console that offers none",
				withArchive, stub.cleared)
		}
	}
}
