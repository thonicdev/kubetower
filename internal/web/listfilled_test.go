package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The other half of `TestTheListPageCarriesEveryMemberTheContractDeclares`,
// which lives in internal/api and cannot reach this package — internal/web
// imports internal/api, so the dependency only runs one way.
//
// That test reads `api/openapi.yaml` and proves the member set on the wire is
// the member set the contract declares. It proves it over a **fake** backend, so
// on its own it would pass against this adapter filling none of them. Both
// failures are silent and neither test sees the other's, which is why there are
// two:
//
//   - the contract grows a member and the Go struct does not → caught there;
//   - the struct has the member and this adapter never fills it → caught here.
//
// A member such as `Column.gauged`, `Column.copyable`, `Column.secondary`,
// `Column.role`, `Cell.gauge`, `Cell.copy` or `Cell.hint` is declared and read by
// `web/ui/src/pages/resources/ResourceCell.tsx`; if it never reaches the wire,
// no usage bar is drawn, no cell can be copied, and `ROLE[column.role]` is
// `ROLE[undefined]` on every cell of every table.
//
// The descriptors in internal/resources carry every one of these facts.
// This is a mapping test, not a behavior one: what it pins is that the
// mapping does not drop them.
func TestTheListPageIsFilledFromTheProducersThatHoldIt(t *testing.T) {
	server := listPageFixture(t)
	reads := apiReads{server}

	page := reads.pageOf(context.Background(),
		api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "default,nosuch", Search: "api", Selector: "app=api"},
		listKind{slug: "pods", title: "Pods", refresh: 60 * time.Second},
		listFixtureTable())

	// An empty answer and a failed answer are different answers, and both differ
	// from a page this test walked over without looking at anything.
	if len(page.Columns) != 5 || len(page.Rows) != 2 {
		t.Fatalf("%d columns and %d rows, want 5 and 2: %+v", len(page.Columns), len(page.Rows), page)
	}
	byLabel := map[string]api.Column{}
	for _, column := range page.Columns {
		byLabel[column.Label] = column
	}

	// --- Column: the four facts about the kind ------------------------------

	// Gauged: internal/resources.gaugeCol, format.go:62. The client draws a bar
	// on `column.gauged && gauge !== undefined` and both were permanently false.
	if !byLabel["CPU"].Gauged {
		t.Error("CPU is a gaugeCol and did not arrive gauged; no bar can be drawn on any column of any kind")
	}
	if byLabel["Name"].Gauged {
		t.Error("Name arrived gauged; the flag is per column and not per table")
	}

	// Copyable: internal/resources.addrCol, format.go:38. Shown
	// is not the same as obtainable, and the cells truncate.
	if !byLabel["Address"].Copyable {
		t.Error("Address is an addrCol and did not arrive copyable; an ingress host cannot be obtained")
	}

	// Secondary: sideCol, format.go:32. It must survive *beside* Visible rather
	// than being collapsed into it — that conflation is what makes a client
	// unable to tell "the person hid this" from "it is off by default", which
	// decides whether a Reset control is offered.
	side := byLabel["Controlled by"]
	if !side.Secondary {
		t.Error("Controlled by is a sideCol and did not arrive secondary")
	}
	if side.Visible {
		t.Error("a secondary column is not drawn until it is asked for")
	}
	if byLabel["Name"].Secondary {
		t.Error("Name arrived secondary")
	}

	// Role: the *descriptor's* first column, never whichever one has been
	// dragged to the left. The list page renders the name styling for
	// List.NameAt, so a name moved to the far right is still a name.
	if byLabel["Name"].Role != api.RoleName {
		t.Errorf("Name.role = %q, want %q", byLabel["Name"].Role, api.RoleName)
	}
	if byLabel["Namespace"].Role != api.RoleNamespace {
		t.Errorf("Namespace.role = %q, want %q — the cell is a control and the branch is keyed on this",
			byLabel["Namespace"].Role, api.RoleNamespace)
	}
	if byLabel["CPU"].Role != api.RoleOther {
		t.Errorf("CPU.role = %q, want %q; the enum is closed and an empty string fails the contract",
			byLabel["CPU"].Role, api.RoleOther)
	}

	// LockedNote: internal/columns.Store.SetVisible refuses to hide the first
	// column, so the menu draws it as a fact rather than as a control that would
	// refuse. On that column and on no other.
	if byLabel["Name"].LockedNote == "" {
		t.Error("the first column carries no lockedNote; the column menu has a tick that silently refuses")
	}
	if byLabel["CPU"].LockedNote != "" {
		t.Errorf("CPU carries a lockedNote (%q) and can be hidden", byLabel["CPU"].LockedNote)
	}

	// Prose: no descriptor in internal/resources declares one and the
	// list page puts `.prose` on no cell of this grid, so `false` is
	// the faithful answer. Pinned so that a producer arriving later is a
	// deliberate change rather than a drift.
	for _, column := range page.Columns {
		if column.Prose {
			t.Errorf("%s arrived as prose and nothing produces one; see the report for unit 0",
				column.Label)
		}
	}

	// --- Cell ---------------------------------------------------------------

	measured := page.Rows[0].Cells[byLabel["CPU"].Key]
	if measured.Gauge == nil {
		t.Fatal("a measured cell carries no gauge; internal/resources.Cell.Gauge was dropped in cellOf")
	}
	if measured.Gauge.Percent != 72 || measured.Gauge.Tone != "warn" {
		t.Errorf("gauge = %+v, want {72 warn} — the percentage and the bar's own tone", *measured.Gauge)
	}
	if measured.Text != "1163m" {
		t.Errorf("text = %q, want the reading; the bar is beside it and never instead of it", measured.Text)
	}

	// An empty reading, on a gauge, where it looks fine. A node whose kubelet did
	// not answer draws an em dash and **no track at all**: an empty track reads
	// as 0%, and 0% reads as an idle machine, which is the opposite of the truth.
	unmeasured := page.Rows[1].Cells[byLabel["CPU"].Key]
	if unmeasured.Gauge != nil {
		t.Errorf("an unmeasured cell carries a gauge (%+v); it must draw no track at all", *unmeasured.Gauge)
	}

	address := page.Rows[0].Cells[byLabel["Address"].Key]
	if address.Copy != "a.example.com,b.example.com,c.example.com,d.example.com" {
		t.Errorf("copy = %q, want the whole reading and not the three the column had room for", address.Copy)
	}
	if len(address.Parts) != 2 {
		t.Errorf("%d parts, want 2 — the pieces a reader would click, separators included", len(address.Parts))
	}

	status := page.Rows[0].Cells[byLabel["Name"].Key]
	if status.Hint != "2 taints" {
		t.Errorf("hint = %q, want the sentence a bare number sends the reader to look up", status.Hint)
	}
	if status.Rank != 1 {
		t.Errorf("rank = %d, want 1 — what a worst-first ordering reads", status.Rank)
	}

	// --- ListPage -----------------------------------------------------------

	// A Secrets list must not push megabytes of plaintext through this process
	// thirty times a minute to render a column that says "4".
	if page.RefreshSeconds != 60 {
		t.Errorf("refreshSeconds = %d, want 60 — the kind's band, not the client's interval",
			page.RefreshSeconds)
	}
	if page.Count != 2 {
		t.Errorf("count = %d, want 2", page.Count)
	}
	if page.Search != "api" || page.Selector != "app=api" {
		t.Errorf("search=%q selector=%q, want them echoed back", page.Search, page.Selector)
	}
	if len(page.Namespaces) != 2 {
		t.Errorf("namespaces = %v, want the selection echoed back", page.Namespaces)
	}
	// A namespace was asked for that this cluster does not have. Said on the
	// page and not among the rows, because it is a fact about the *selection*
	// and has to appear even when there are rows.
	if page.UnknownNamespace == "" {
		t.Error("a namespace this cluster does not have drew no note; " +
			"\"Nothing here\" is then a claim about what a namespace contains, when it is not one")
	}
	// The read-only fence is a property of the **cluster**, not of
	// the kind: `readOnly(table.Kind)` would answer for a cluster called "pods".
	if page.ReadOnlyNote == "" {
		t.Error("a read-only cluster said nothing on the list, and refuses at the click instead")
	}

	// --- BulkAffordance -----------------------------------------------------

	if page.Bulk == nil {
		t.Fatal("pods offer bulk verbs and none arrived")
	}
	if page.Bulk.Scope != "prod|pods" {
		t.Errorf("scope = %q, want %q: the server names it, because the server is what knows "+
			"when the underlying set is a different one", page.Bulk.Scope, "prod|pods")
	}
}

// The verb set is a property of the kind: a Secrets list must not offer Cordon,
// nor a pod list Restart beside Delete.
//
// Restart is the one worth its own case. On a pod, restart **is** delete;
// internal/actions says so in the sentence it writes back. A bar offering both
// offers one request under two words, with the gentle-sounding one being the
// destructive one.
func TestTheBulkVerbsOnALiveListAreTheKindsOwn(t *testing.T) {
	for _, want := range []struct {
		kind  string
		verbs []string
		none  bool
	}{
		{kind: "pods", verbs: []string{"evict", "delete"}},
		// Delete is offered; drain stays the page's, it is staged.
		{kind: "nodes", verbs: []string{"cordon", "uncordon", "delete"}},
		{kind: "cronjobs", verbs: []string{"suspend", "resume", "delete"}},
		{kind: "secrets", verbs: []string{"delete"}},
		// A namespace deletes in bulk as anything else does, behind the typed
		// count.
		{kind: "namespaces", verbs: []string{"delete"}},
	} {
		affordance := bulkAffordance("prod", want.kind, false)
		if want.none {
			if affordance != nil {
				t.Errorf("%s offers %+v; nil is what keeps the checkboxes off the table",
					want.kind, affordance.Verbs)
			}
			continue
		}
		if affordance == nil {
			t.Errorf("%s offers no bulk verb at all", want.kind)
			continue
		}
		got := make([]string, 0, len(affordance.Verbs))
		for _, verb := range affordance.Verbs {
			got = append(got, verb.Verb)
		}
		if len(got) != len(want.verbs) {
			t.Errorf("%s offers %v, want %v", want.kind, got, want.verbs)
			continue
		}
		for i := range got {
			if got[i] != want.verbs[i] {
				t.Errorf("%s offers %v, want %v", want.kind, got, want.verbs)
				break
			}
		}
	}

	// And the fence answers per verb rather than by withholding the bar, so the
	// person sees why rather than seeing nothing.
	fenced := bulkAffordance("prod", "pods", true)
	if fenced == nil {
		t.Fatal("a read-only cluster withheld the bar entirely")
	}
	for _, verb := range fenced.Verbs {
		if verb.Permitted || verb.Reason == nil {
			t.Errorf("%s is offered as permitted on a read-only cluster", verb.Verb)
		}
	}
}

// listFixtureTable is a table shaped like a descriptor's: a name, a namespace, a
// gauged column, a copyable one and a secondary one, with one row measured and
// one not.
func listFixtureTable() resources.Table {
	return resources.Table{
		Kind: "pods", Title: "Pods", Namespaced: true,
		Columns: []resources.Column{
			{Title: "Name"},
			{Title: "Namespace"},
			{Title: "CPU", Numeric: true, Gauged: true},
			{Title: "Address", Copyable: true},
			{Title: "Controlled by", Secondary: true},
		},
		Rows: []resources.Row{
			{
				Name: "api-1", Namespace: "default", Rank: 1,
				Cells: []resources.Cell{
					{Text: "api-1", Rank: 1, Hint: "2 taints"},
					{Text: "default"},
					{Text: "1163m", Value: 1163, Gauge: &resources.Gauge{Percent: 72, Class: "warn"}},
					{
						Text: "a.example.com, b.example.com +2 more",
						Copy: "a.example.com,b.example.com,c.example.com,d.example.com",
						Addresses: []resources.Address{
							{Text: "a.example.com", Href: "https://a.example.com"},
							{Text: ", b.example.com +2 more"},
						},
					},
					{Text: "ReplicaSet/api-7d9"},
				},
			},
			{
				Name: "api-2", Namespace: "default",
				Cells: []resources.Cell{
					{Text: "api-2"},
					{Text: "default"},
					// The kubelet did not answer. No gauge, and therefore no
					// track — see the assertion above.
					{Text: "—"},
					{Text: "—"},
					{Text: "—"},
				},
			},
		},
	}
}

// An Ingresses list, from the descriptor to the wire and back. The resources
// test pins that the Ingress Address column is Secondary; this pins what that
// makes of the page a browser receives, over the real descriptor rather than
// the fixture above: a fresh Ingresses list arrives with Address off and marked
// off-by-default (so the columns menu shows it unchecked and offers no Reset),
// still copyable; ticking it on turns it on; and the tick survives a restart,
// because the store keeps "shown on purpose" as `false` rather than as absent.
func TestAFreshIngressesListArrivesWithoutItsAddress(t *testing.T) {
	dir := t.TempDir()
	table := resources.Table{
		Kind: "ingresses", Title: "Ingresses", Namespaced: true,
		Columns: resources.ColumnsOf("ingresses"),
	}
	pageOf := func(server *Server) (api.ListPage, map[string]api.Column) {
		t.Helper()
		page := apiReads{server}.pageOf(context.Background(),
			api.ListQuery{Cluster: "prod", Kind: "ingresses"},
			listKind{slug: "ingresses", title: "Ingresses", refresh: 5 * time.Second}, table)
		byLabel := map[string]api.Column{}
		for _, column := range page.Columns {
			byLabel[column.Label] = column
		}
		return page, byLabel
	}

	server := sharedPrefsServer(t, Options{Columns: columns.New(dir, nil)})
	page, byLabel := pageOf(server)
	address, ok := byLabel["Address"]
	if !ok {
		t.Fatalf("the Ingresses page has no Address column: %+v", page.Columns)
	}
	if address.Visible || !address.Secondary {
		t.Errorf("fresh: Address is visible=%v secondary=%v, want hidden and off by default",
			address.Visible, address.Secondary)
	}
	if !address.Copyable {
		t.Error("fresh: Address arrived not copyable; shown on request it could not be copied")
	}
	if !byLabel["Hosts"].Visible {
		t.Error("fresh: Hosts is hidden too; only Address is hidden by default")
	}
	if page.ColumnsCustomised {
		t.Error("fresh: the page says the columns were arranged; a default would offer Reset")
	}

	if err := (apiPrefs{server}).SetColumnVisible("ingresses", address.Key, true); err != nil {
		t.Fatalf("ticking Address on: %v", err)
	}
	if _, byLabel = pageOf(server); !byLabel["Address"].Visible {
		t.Error("ticked on: Address is still hidden")
	}

	// A restart: a fresh store over the same file.
	restarted := sharedPrefsServer(t, Options{Columns: columns.New(dir, nil)})
	if _, byLabel = pageOf(restarted); !byLabel["Address"].Visible {
		t.Error("after a restart: Address is hidden again; shown on purpose was stored as absent")
	}
}

// listPageFixture is a server with one cluster, fenced read-only, whose
// namespace list is seeded so that pageOf answers the unknown-namespace question
// from the hold rather than by asking a cluster this test does not have.
func listPageFixture(t *testing.T) *Server {
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
	server, err := New(Options{
		Clusters: manager,
		ReadOnly: func(name string) bool { return name == "prod" },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := time.Now()
	server.namespaces.mu.Lock()
	server.namespaces.held["prod"] = namespaceHold{
		names: []string{"default", "kube-system"}, at: now, readAt: now,
	}
	server.namespaces.mu.Unlock()
	return server
}
