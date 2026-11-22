package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/hotbars"
	"github.com/thonicdev/kubetower/internal/nameplates"
)

// Where the preference writes are not registered, the preference files
// are not read either.
//
// The stale state this guards against is ordinary: a volume written while the
// writes existed on one replica, or shared with a desktop. Read, it would freeze
// one person's rail and columns into everybody's defaults - and the list
// payload's stored `visible` would then contradict the column's own `secondary`,
// which is the default the browser-kept columns compare against, so a tick on
// that column would write nothing and Reset would keep the stale widths.
//
// Both arms over the **same** stores on the same directory: the desktop must
// still read them, or the cheapest way to pass is to read nothing anywhere.
func TestNoPreferenceFileIsReadWhereItCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	table := listFixtureTable()
	titles := titlesOfTable(table)

	// The stale state: a secondary column shown, a width, a nameplate, a hidden
	// tile - each written through its own store, so it is on disk exactly as a
	// console that had the writes would have left it.
	cols := columns.New(dir, nil)
	if _, err := cols.SetVisible("pods", "Controlled by", true, true, titles); err != nil {
		t.Fatalf("SetVisible: %v", err)
	}
	if _, err := cols.Resize("pods", []columns.Column{{Title: "CPU", Width: 200}}, titles); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	contexts := []string{"prod", "staging"}
	plates := nameplates.New(dir, nil)
	if _, err := plates.Set("prod", nameplates.Nameplate{Name: "Somebody's name"}, contexts); err != nil {
		t.Fatalf("Set: %v", err)
	}
	bars := hotbars.New(dir, nil)
	if _, err := bars.Hide("staging", contexts); err != nil {
		t.Fatalf("Hide: %v", err)
	}

	for _, c := range []struct {
		name                  string
		inCluster, replicated bool
		pristine              bool
	}{
		{"desktop", false, false, false},
		{"server profile, one replica", true, false, true},
		{"several replicas", false, true, true},
	} {
		// Fresh stores over the same directory, as a restart would load them.
		server := sharedPrefsServer(t, Options{
			Columns: columns.New(dir, nil), Nameplates: nameplates.New(dir, nil),
			Hotbars: hotbars.New(dir, nil), InCluster: c.inCluster, Replicated: c.replicated,
		})

		page := apiReads{server}.pageOf(context.Background(),
			api.ListQuery{Cluster: "prod", Kind: "pods"},
			listKind{slug: "pods", title: "Pods", refresh: 60 * time.Second}, table)
		byLabel := map[string]api.Column{}
		for _, column := range page.Columns {
			byLabel[column.Label] = column
		}
		controlled, cpu := byLabel["Controlled by"], byLabel["CPU"]

		prefs := apiPrefs{server}.Preferences()

		if c.pristine {
			// Pristine: what the descriptor says, and the payload agreeing with
			// `secondary` is what lets a browser tick and untick that column.
			if controlled.Visible != !controlled.Secondary {
				t.Errorf("%s: Controlled by is visible=%v secondary=%v; the stored visibility was read",
					c.name, controlled.Visible, controlled.Secondary)
			}
			if cpu.Width != nil || page.ColumnsCustomised {
				t.Errorf("%s: CPU width %v, customized %v; the stored widths were read",
					c.name, cpu.Width, page.ColumnsCustomised)
			}
			if len(prefs.Rail.Nameplates) != 0 || len(prefs.Rail.Hidden) != 0 || len(prefs.Columns.Kinds) != 0 {
				t.Errorf("%s: preferences read back %+v; the files were read", c.name, prefs)
			}
			continue
		}
		// The control: the desktop reads every one of them.
		if !controlled.Visible || cpu.Width == nil || !page.ColumnsCustomised {
			t.Errorf("%s: the stored columns were not read: %+v, %+v", c.name, controlled, cpu)
		}
		if prefs.Rail.Nameplates["prod"].Name != "Somebody's name" || len(prefs.Rail.Hidden) != 1 {
			t.Errorf("%s: the stored rail was not read: %+v", c.name, prefs.Rail)
		}
	}
}

func sharedPrefsServer(t *testing.T, opts Options) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: remote
  cluster: {server: https://127.0.0.1:1}
contexts:
- name: prod
  context: {cluster: remote, user: none}
- name: staging
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
	opts.Clusters = manager
	server, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

// The settings clear is still registered on one replica of the server profile,
// and its `preferences` and `all` scopes must not rewrite the files
// the profile no longer reads. They do not, on any profile: the act refuses every
// scope but the archive (`apiSettings.Clear`). Pinned here so that wiring it
// later has to decide the server profile on purpose rather than by accident.
func TestTheClearRewritesNoPreferenceFileUnderTheProfile(t *testing.T) {
	server := sharedPrefsServer(t, Options{InCluster: true})
	for _, scope := range []string{"preferences", "all"} {
		if _, err := (apiSettings{server}).Clear(scope); !errors.Is(err, api.ErrNotInProfile) {
			t.Errorf("Clear(%q) under the server profile answered %v, want ErrNotInProfile", scope, err)
		}
	}
}
