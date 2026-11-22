// Package resources is the listing layer: one table of descriptors covering
// every kind the console shows. A kind is a row in that table — its slug, its
// columns, and the function turning one object into cells — and fetching,
// filtering and sorting are written once.
//
// Cells carry no Kubernetes: a string, an optional class (ok/warn/bad) and an
// optional rank, so the UI draws a pill without knowing what a CrashLoopBackOff
// is, and the sort puts broken things first without asking each kind.
package resources

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// Health classes. They are the class the UI puts on a pill, so they are
// lower-case strings rather than an enum the UI would have to map.
const (
	ClassNone = ""
	ClassOK   = "ok"
	ClassWarn = "warn"
	ClassBad  = "bad"
)

// Sort ranks. Lower sorts first: a list is read to find what is wrong.
// Zero is not a rank: it means the cell has no opinion on health, so a cell
// that never set it cannot claim to be the worst thing on the page.
const (
	RankBad  = 1
	RankWarn = 2
	RankOK   = 3
)

// rankNone is the rank of a row no cell had an opinion about — after everything
// graded, since a graded "ok" is a stronger statement than silence.
const rankNone = 4

// Cell is one value in the table, already decided.
type Cell struct {
	// What to print. Never empty: a missing value is an em dash, so a column
	// cannot silently collapse.
	Text string
	// "ok", "warn", "bad", or empty for a cell that makes no claim.
	Class string
	// How urgently the row wants reading; see the Rank constants. Zero means
	// this cell does not say.
	Rank int
	// Hint is what hovering says, for a cell that states a number the operator
	// would otherwise have to look up (a node's taint count). Empty on almost
	// every cell: it is not a general tooltip.
	Hint string
	// What a Numeric column compares this cell by: 1200 for "1200m",
	// 2147483648 for "2 GiB", 3600 for "1h". Carried beside the text rather
	// than parsed back out of it, which would need a second copy of every
	// formatting rule.
	Value float64
	// Gauge is a small bar beside the reading, when the reading is a fraction
	// of a known whole. Structured, never markup: Text is what the filter
	// searches, the sort compares and customresources fills from printer
	// columns, so it stays the plain reading.
	//
	// Nil means no measurement, and draws no bar: an empty track would read as
	// 0%, an idle machine, when nothing could be measured.
	Gauge *Gauge
	// Addresses is the reading broken into clickable pieces, for columns that
	// hold somewhere reachable (an Ingress's hosts). Structured for Gauge's
	// reason; Text stays the whole reading. A piece with no Href is drawn as
	// plain text: `*` and a wildcard host are not addresses.
	//
	// The pieces are concatenated verbatim and the separators are pieces, so
	// the rendering equals Text exactly — JoinCapped's " +2 more" follows a
	// space, not ", ", and a renderer inserting separators would drift from it.
	Addresses []Address
	// Copy is what a copy control puts on the clipboard, or empty for a cell
	// with no such control. Separate from Text because they differ: Text holds
	// `JoinCapped(hosts, 3)`, and the hosts it left out are the ones somebody
	// needs to copy.
	Copy string
}

// Address is one clickable piece of a cell's reading. The scheme is decided
// from the object (ingressHosts reads `spec.tls`), never guessed; a Service's
// scheme is internal/forward's harder question, which is why the Services list
// offers no link (see externalAddress).
type Address struct {
	// Text is the piece as printed: the host or address, never the URL.
	Text string
	// Href is where it goes, or empty for a piece that is not somewhere.
	Href string
}

// Gauge is one bar: a proportion, and how alarming that proportion is.
type Gauge struct {
	// Percent is the fill, 0 to 100, already clamped by gauge(): the UI puts
	// it straight into a width.
	Percent float64
	// Class is "ok", "warn" or "bad", deliberately separate from Cell.Class:
	// that one turns the reading into a pill and feeds worstRank, and a node at
	// 91% memory deserves a colored bar, not a row promoted as an incident.
	Class string
}

// Column is a table heading.
type Column struct {
	Title string
	// Right-aligned in the UI. Counts and ages read as columns of digits.
	Numeric bool
	// Secondary marks a column worth having but not worth the width by
	// default: drawn only if the operator asks for it in the columns menu. A
	// pod's controller, for example, is a fact about one pod, better read in
	// the drawer than repeated on every row.
	Secondary bool
	// Copyable says the column's cells carry a copy control when they hold a
	// value meant to be pasted elsewhere (a host, an external address, a
	// cluster IP), not on names or images. Declared on the kind, for Gauged's
	// reason: an Ingress's address first renders as `pending`, and the column
	// width must leave room for the control that arrives with it.
	Copyable bool
	// Gauged says the column's cells draw a bar when there is a measurement.
	// Declared on the kind rather than read off the cells because the first
	// render has none (usage never blocks on a cluster), and the UI must
	// reserve the bar's width before the measurement arrives or the column
	// overflows and later jumps.
	Gauged bool
	// Filterable says the filter control offers this column: its cells hold a
	// category worth choosing among (a node, a status, a QoS class) rather than
	// a name, a sentence or a figure. Never on a Numeric column nor on the
	// namespace (see Matcher; a test holds every descriptor to it), and never
	// on a custom resource's printer columns, which say nothing about which are
	// categories.
	Filterable bool
}

// Row is one object. Cells match Columns one for one — a test pins that for
// every descriptor, because a drifting pair shifts every value one column left
// and still renders perfectly.
type Row struct {
	Name      string
	Namespace string
	Cells     []Cell
	// The worst rank any of its cells claimed.
	Rank int

	// Where clicking this row's name goes, or empty for a kind that has no
	// detail page. Built by whoever renders the table: routes are not this
	// package's to know.
	Href string

	// Switches is the two-state fields a verb flips, by dotted path, as the
	// value in effect: `spec.suspend` on a CronJob, `spec.unschedulable` on a
	// Node, nil on every other kind. Not a cell: a column is for reading and
	// may be hidden or rearranged, and the selection bar needs the state
	// whatever the columns show (`switchesOf`).
	Switches map[string]string

	// Unexported: only the sort of the kind that orders by time reads it.
	created time.Time
}

// Table is the answer to a List.
//
// An empty Rows means the cluster answered and had nothing to show. It never
// means the query failed: that is an error return, and the two must stay
// distinguishable.
type Table struct {
	Kind       string
	Title      string
	Namespaced bool
	Namespace  string
	Query      string
	Columns    []Column
	Rows       []Row

	// Filters is the filters that narrowed Rows, resolved to a column index -
	// only those that took effect, so a page echoes what it actually did.
	Filters []Applied
	// Offered is, per Filterable column index, the values the filter control
	// can offer: those the search and every other column's filters leave.
	Offered map[int][]string

	// The order actually applied, echoed back so the page can draw the arrow on
	// the right heading. Empty means the default order, including for a column
	// this kind does not have.
	Sort       string
	Descending bool

	// Access is what the access review did to the list: Narrowed when the
	// person may not list this kind in every namespace and the rows are those
	// of the namespaces they may. A page says so, because a narrowed list and
	// a complete one look alike and are different answers.
	Access access.Visible
}

// Usage is what one object is using at this moment, as much of it as a table
// column shows. Declared here rather than imported from internal/metrics so
// this package depends on the numbers, not on how they were obtained. A caller
// with no metrics backend passes nothing; see usageOf for why nothing is not
// zero.
type Usage struct {
	CPUMillicores int64
	MemoryBytes   int64
	// From is the backend that measured CPUMillicores and MemoryBytes -
	// metrics.KindMetricsServer or metrics.KindPrometheus - and empty where
	// nothing did. No table reads it; the readers that name their source do.
	From string

	// ComputeUnmeasured marks an entry present for another reading (a node's
	// disk) while the metrics backend said nothing about its CPU and memory,
	// which would otherwise render as "0m". Negative so that the zero value
	// keeps meaning "measured", as presence in the map does.
	ComputeUnmeasured bool

	// Disk is the object's own filesystem, as its kubelet reports it. Only
	// nodes have one, and nil is ordinary: metrics-server knows nothing of
	// disk, and the kubelet summary API needs `nodes/proxy`, which several
	// managed offerings refuse.
	Disk *Disk

	// Volume is how full one PersistentVolumeClaim's filesystem is, from the
	// kubelet that mounted it. Only the two storage kinds have one.
	Volume *Volume
}

// Volume is a claim's fullness, or the reason there is no figure for it.
//
// Unlike other unmeasured objects, which are simply absent from the usage map,
// a claim has three distinct ways to have no reading — nothing mounted it, the
// kubelet could not be asked, or the kubelet answered about a shared export
// rather than this claim — so the reason travels with the absence. It is also
// how a list reports partial coverage: a table has no banner, so it goes into
// the hint of the rows whose node did not answer.
type Volume struct {
	// Measured says UsedBytes and CapacityBytes are a reading. False means this
	// entry is a reason only; a zero `Volume{}` therefore draws as unmeasured.
	Measured bool
	// UsedBytes is what the kubelet measured; CapacityBytes is the claim's
	// declared size, the one an operator would grow (it differs from the
	// kubelet's figure exactly when kubeletstats.Trustworthy says so).
	UsedBytes     int64
	CapacityBytes int64
	// Note is the hint when Measured is false: the reason, required. When
	// Measured is true the hint is `6 / 8 GiB` (Fraction) followed by Note,
	// saying who measured it.
	Note string
}

// Disk is a filesystem's fullness, in bytes. A pointer on Usage so that
// absent stays distinguishable from a measured zero.
type Disk struct {
	UsedBytes     int64
	CapacityBytes int64
}

// Query is one request for a table: which kind, narrowed how, ordered how.
type Query struct {
	// The URL segment or an alias of it: "pods", "po", "Deployments".
	Kind string
	// Ignored on a cluster-scoped kind rather than refused: the UI carries the
	// current namespace across a change of kind.
	Namespace string
	// Filter is the search box: what the row shows and its label keys and values,
	// case-insensitively, by substring. See Matcher.
	Filter string
	// Selector is a label selector in kubectl's syntax (`app=web,tier!=db`),
	// handed to the API server as the list's labelSelector. Empty selects
	// everything; one that does not parse is refused with InvalidSelector,
	// never dropped, or every row would look as if it matched.
	Selector string
	// Filters is the filter control's choices, each a column and a value its
	// cell must equal: OR within a column, AND across columns. See Matcher.
	Filters []Filter
	// Within is a selection of several namespaces read from the whole
	// cluster: a row outside it is dropped before matching, so neither the
	// rows nor the offered filter values describe an unchosen namespace. Empty
	// is every namespace. See Matcher.Within.
	Within []string
	// Sort is the title of the column to order by, matched case-insensitively.
	// Empty, or a title this kind does not have, is the default order:
	// unhealthy first, then by name.
	Sort       string
	Descending bool
}

// Descriptor is everything the console knows about one kind.
type Descriptor struct {
	// The URL segment and the name callers pass to List: "persistentvolumeclaims".
	Kind string
	// The navigation section this kind belongs to, following the grouping
	// Headlamp and Lens both use, so an operator of either knows where to look.
	Group string
	// What the page is titled: "Persistent volume claims".
	Title string
	// False for nodes, namespaces and persistent volumes, which have no
	// namespace to be filtered by and must not be given one.
	Namespaced bool
	Columns    []Column

	// Fetches the objects and binds each to its row function.
	list listFn
	// recentFirst orders the default sort newest first, for Events, whose
	// names are `pod-name.17abc…` and whose question is what happened last.
	recentFirst bool

	// refresh is how often a list of this kind is worth re-reading. Read
	// through Refresh, so a zero cannot reach a caller as "never refresh". See
	// kinds.go for the bands.
	refresh time.Duration
}

// DefaultRefresh is what a kind declaring no interval is re-read at: the live
// kinds' interval, so an undeclared kind errs towards too often.
const DefaultRefresh = 2 * time.Second

// Refresh is how often this kind's list is worth re-reading. A method so the
// default lives here rather than in every caller.
func (d Descriptor) Refresh() time.Duration {
	if d.refresh <= 0 {
		return DefaultRefresh
	}
	return d.refresh
}

// ErrUnknownKind is returned for a kind that has no descriptor. Callers match
// on it to answer 404 rather than 500.
var ErrUnknownKind = errors.New("unknown kind")

// Kinds lists the descriptors in the order the navigation shows them.
func Kinds() []Descriptor {
	out := make([]Descriptor, len(descriptors))
	copy(out, descriptors)
	return out
}

// ColumnsOf is the columns a kind declares, or nil for a kind this package
// does not know, such as a CRD, whose columns come from the cluster.
func ColumnsOf(kind string) []Column {
	d, ok := Lookup(kind)
	if !ok {
		return nil
	}
	return d.Columns
}

// AliasesOf is every name Lookup answers to for a kind, sorted: its slug and
// the aliases resolving to it ("cm", "configmap", "configmaps"). The menu's
// jump box matches them. Nil for a kind not served here. The result is a copy.
func AliasesOf(kind string) []string {
	names, ok := aliasesByKind[kind]
	if !ok {
		return nil
	}
	return append([]string(nil), names...)
}

// aliasesByKind is `aliases` inverted, built once: the menu asks for every
// kind on every answer.
var aliasesByKind = func() map[string][]string {
	out := make(map[string][]string, len(descriptors))
	for _, d := range descriptors {
		out[d.Kind] = []string{d.Kind}
	}
	for alias, canonical := range aliases {
		if names, ok := out[canonical]; ok {
			out[canonical] = append(names, alias)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}()

// Lookup finds a descriptor by slug, accepting the singular and the kubectl
// abbreviation (`svc`, `deployment`).
func Lookup(kind string) (Descriptor, bool) {
	name := strings.ToLower(strings.TrimSpace(kind))
	if canonical, ok := aliases[name]; ok {
		name = canonical
	}
	for _, d := range descriptors {
		if d.Kind == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// List fetches one kind from one cluster and renders it.
//
// usage is what the rows are using right now, keyed as internal/metrics keys
// it. It is a parameter rather than fetched here because many clusters cannot
// answer the metrics API, and a list must not depend on it; nil draws those
// columns as em dashes.
//
// gate reviews the person behind ctx before the rows reach them: a list they
// may not make is refused, and a list of every namespace they may not make
// whole is narrowed to the namespaces they may (Table.Access says which).
func List(ctx context.Context, gate *access.Gate, client *cluster.Client, q Query, usage map[string]Usage) (Table, error) {
	descriptor, ok := Lookup(q.Kind)
	if !ok {
		// Named, so the reader sees what was asked for.
		return Table{}, fmt.Errorf("%w %q", ErrUnknownKind, q.Kind)
	}
	if client == nil || client.Clients == nil {
		return Table{}, fmt.Errorf("listing %s: no client for this cluster", descriptor.Kind)
	}

	namespace := q.Namespace
	if !descriptor.Namespaced {
		namespace = ""
	}

	selector, err := ParseSelector(q.Selector)
	if err != nil {
		return Table{}, err
	}
	reader := ReaderFor(gate, client)
	reader.Selector = selector
	objects, visible, err := descriptor.list(ctx, reader, namespace)
	if err != nil {
		// Wrapped: callers match Forbidden or NotFound with errors.As.
		return Table{}, fmt.Errorf("listing %s: %w", descriptor.Kind, err)
	}

	table := Table{
		Kind:       descriptor.Kind,
		Title:      descriptor.Title,
		Namespaced: descriptor.Namespaced,
		Namespace:  namespace,
		Query:      q.Filter,
		Columns:    descriptor.Columns,
		Access:     visible,
	}
	match := NewMatcher(q.Filter, descriptor.Columns).WithFilters(q.Filters).Within(q.Within)
	offered := match.Offer()
	table.Rows = build(objects, match, offered, namespace, usage, time.Now())
	table.Filters, table.Offered = match.Applied(), offered.Lists()

	column := sortColumn(descriptor.Columns, q.Sort)
	sortRows(table.Rows, descriptor, column, q.Descending)
	if column >= 0 {
		table.Sort, table.Descending = descriptor.Columns[column].Title, q.Descending
	}
	return table, nil
}

// build filters and renders. Split out of List so tests can drive it with a
// fixed clock.
//
// A row is rendered before it is filtered, because the filter searches what
// the row shows (see Matcher). That bounds filtering at the cost of an
// unfiltered list, which renders every object anyway.
func build(objects []object, match Matcher, offered Offered, namespace string, usage map[string]Usage, now time.Time) []Row {
	rows := make([]Row, 0, len(objects))
	for _, o := range objects {
		name, ns := o.meta.GetName(), o.meta.GetNamespace()
		// Outside the namespaces asked for: not rendered, not matched, and not
		// counted among the values the filter control offers.
		if !match.InScope(ns) {
			continue
		}

		cells := o.row(now, usageOf(usage, namespace, ns, name))
		if !match.Keep(name, cells, o.meta.GetLabels(), offered) {
			continue
		}

		rows = append(rows, Row{
			Name:      name,
			Namespace: ns,
			Cells:     cells,
			Rank:      worstRank(cells),
			Switches:  switchesOf(o.meta),
			created:   o.meta.GetCreationTimestamp().Time,
		})
	}
	return rows
}

// switchesOf is a row's `Switches`, read off the typed object: the same two
// paths and the same effective value (an unset field is false, as the API
// server reads it) as `detail.Object.Switches`, so the row menu, the drawer
// and the selection bar agree on which half of a toggle an object is on.
func switchesOf(object metav1.Object) map[string]string {
	switch typed := object.(type) {
	case *batchv1.CronJob:
		return map[string]string{"spec.suspend": strconv.FormatBool(typed.Spec.Suspend != nil && *typed.Spec.Suspend)}
	case *corev1.Node:
		return map[string]string{"spec.unschedulable": strconv.FormatBool(typed.Spec.Unschedulable)}
	}
	return nil
}

// usageOf finds one row's figures, or nil when there is no backend, the
// backend has not seen the object, or the caller passed nothing. All three
// print an em dash; a zero would be a measurement.
//
// The key must agree with metrics.Key: the name alone when one namespace was
// asked about, `namespace/name` when all were. Disagreeing would put one pod's
// figures beside another pod's name.
func usageOf(usage map[string]Usage, queried, namespace, name string) *Usage {
	if len(usage) == 0 {
		return nil
	}
	key := name
	if queried == "" && namespace != "" {
		key = namespace + "/" + name
	}
	found, ok := usage[key]
	if !ok {
		return nil
	}
	return &found
}

// sortColumn finds the column the caller named, or -1 for the default order.
func sortColumn(columns []Column, title string) int {
	wanted := strings.ToLower(strings.TrimSpace(title))
	if wanted == "" {
		return -1
	}
	for i, c := range columns {
		if strings.ToLower(c.Title) == wanted {
			return i
		}
	}
	return -1
}

// sortRows puts the rows in the order asked for, or in the default one when
// none was.
//
// An explicit sort replaces the health ranking rather than tie-breaking on it:
// whoever asked for the busiest pod did not ask for the busiest broken one.
func sortRows(rows []Row, descriptor Descriptor, column int, descending bool) {
	if column < 0 {
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Rank != rows[j].Rank {
				return rows[i].Rank < rows[j].Rank
			}
			if descriptor.recentFirst && !rows[i].created.Equal(rows[j].created) {
				return rows[i].created.After(rows[j].created)
			}
			return byName(rows[i], rows[j])
		})
		return
	}
	sortByColumn(rows, column, descriptor.Columns[column].Numeric, descending)
}

// OrderBy applies an explicit column order to a table built somewhere other
// than List.
//
// It exists for custom resource kinds, whose rows come from a CRD's printer
// columns, so they sort by the same rules as every other list: em dash last,
// ties by name. A title this table does not have leaves the rows as they are
// and reports no sort.
func (t *Table) OrderBy(title string, descending bool) {
	column := sortColumn(t.Columns, title)
	if column < 0 {
		t.Sort, t.Descending = "", false
		return
	}
	sortByColumn(t.Rows, column, t.Columns[column].Numeric, descending)
	t.Sort, t.Descending = t.Columns[column].Title, descending
}

func sortByColumn(rows []Row, column int, numeric, descending bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := cellAt(rows[i], column), cellAt(rows[j], column)

		// An em dash is the absence of a value, not the smallest one: it goes
		// last whichever way the column points.
		if absent(a) != absent(b) {
			return absent(b)
		}
		if !absent(a) {
			if cmp := compareCells(a, b, numeric); cmp != 0 {
				if descending {
					return cmp > 0
				}
				return cmp < 0
			}
		}
		// Ties break by name, ascending, in both directions: the page redraws
		// every two seconds and equal rows must not swap under the pointer.
		return byName(rows[i], rows[j])
	})
}

func byName(a, b Row) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.Namespace < b.Namespace
}

func absent(c Cell) bool { return c.Text == Missing }

// cellAt tolerates a row shorter than its headings. A missing cell is a
// descriptor bug, caught by a test, and not worth a panic.
func cellAt(row Row, i int) Cell {
	if i >= len(row.Cells) {
		return Cell{Text: Missing}
	}
	return row.Cells[i]
}

func compareCells(a, b Cell, numeric bool) int {
	if numeric && a.Value != b.Value {
		if a.Value < b.Value {
			return -1
		}
		return 1
	}
	// Case-insensitively: `Web-0` and `api-1` are one alphabet to a reader, and
	// byte order puts every capital ahead of every lower-case letter.
	return strings.Compare(strings.ToLower(a.Text), strings.ToLower(b.Text))
}

// worstRank is the row's rank: the lowest any cell claimed, ignoring the cells
// that claimed nothing.
func worstRank(cells []Cell) int {
	rank := rankNone
	for _, c := range cells {
		if c.Rank != 0 && c.Rank < rank {
			rank = c.Rank
		}
	}
	return rank
}

// --- the descriptor plumbing --------------------------------------------------

// object pairs an object's metadata, which the filter and sort need for every
// kind, with the closure rendering it. The closure takes the live usage
// because some kinds have columns that do not come from the object; nil means
// nobody measured it.
type object struct {
	meta metav1.Object
	row  func(now time.Time, usage *Usage) []Cell
}

type listFn func(ctx context.Context, r Reader, namespace string) ([]object, access.Visible, error)

// pointerTo constrains PT to *T *and* to something carrying ObjectMeta, so the
// compiler checks what would otherwise be an interface assertion per object.
type pointerTo[T any] interface {
	*T
	metav1.Object
}

// renders joins a fetch to a row function, so the descriptor table stays a
// table.
func renders[T any, PT pointerTo[T]](
	fetch func(ctx context.Context, r Reader, namespace string) ([]T, access.Visible, error),
	row func(PT, time.Time) []Cell,
) listFn {
	return rendersLive(fetch, func(item PT, now time.Time, _ *Usage) []Cell { return row(item, now) })
}

// rendersLive is renders for the kinds whose row also states what the object
// is using right now. A second constructor rather than a usage parameter on
// every row function, since most kinds have nothing to say about usage.
func rendersLive[T any, PT pointerTo[T]](
	fetch func(ctx context.Context, r Reader, namespace string) ([]T, access.Visible, error),
	row func(PT, time.Time, *Usage) []Cell,
) listFn {
	return func(ctx context.Context, r Reader, namespace string) ([]object, access.Visible, error) {
		items, visible, err := fetch(ctx, r, namespace)
		if err != nil {
			return nil, visible, err
		}
		out := make([]object, 0, len(items))
		for i := range items {
			// Indexed, not ranged by value: these objects run to kilobytes and
			// the closure below outlives the loop.
			item := PT(&items[i])
			out = append(out, object{
				meta: item,
				row:  func(now time.Time, usage *Usage) []Cell { return row(item, now, usage) },
			})
		}
		return out, visible, nil
	}
}
