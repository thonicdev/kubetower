package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/resources"
)

// Namespaces is the selector every list carries, read through the per-cluster
// hold so every caller sees one answer (namespaceAnswer.state keeps the four
// cases). `pending` cannot come out of it, since namespaces are fetched on
// demand; and a 403 arrives as `unreadable`, not `forbidden`, because the hold
// keeps only whether the fetch failed.
func (r apiReads) Namespaces(ctx context.Context, cluster string) (api.NamespaceReading, *api.Failure) {
	scope := api.Scope{Cluster: cluster, Resource: "namespaces", Verb: "list"}
	if r.s.namespaces == nil {
		// A build without the namespace cache: `absent`, there is no reader.
		return api.NamespaceReading{}, api.Absent("this build has no namespace reader").Within(scope)
	}
	// The hold is the console's, shared by everybody; this person sees it only with
	// their own `list namespaces`. Namespaces are cluster-scoped, so there is nothing
	// to narrow to: a person who may not list them types the namespace instead.
	if err := r.s.mayListNamespaces(ctx, cluster); err != nil {
		return api.NamespaceReading{}, stateOf(err, scope)
	}
	answer := r.s.namespaceAnswer(ctx, cluster)
	switch answer.state() {
	case namespacesStale:
		return api.NamespaceReading{
			Names:  answer.names,
			ReadAt: answer.readAt,
			Stale:  true,
			Why:    api.Unreachable("this cluster has stopped answering").Within(scope),
		}, nil
	case namespacesUnknown:
		return api.NamespaceReading{}, api.Unreadable(
			"this cluster's namespaces could not be read").Within(scope).Retryable()
	default:
		// Listed and none: the envelope tells them apart from the slice.
		return api.NamespaceReading{Names: answer.names, ReadAt: answer.readAt}, nil
	}
}

// ListResources is one endpoint for a list, whether it fills a page or a panel.
func (r apiReads) ListResources(ctx context.Context, q api.ListQuery) (api.ListPage, *api.Failure) {
	scope := api.Scope{Cluster: q.Cluster, Resource: q.Kind, Namespace: q.Namespace, Verb: "list"}
	// A selector that does not parse is the request's fault, whatever the cluster
	// would answer.
	if _, err := resources.ParseSelector(q.Selector); err != nil {
		return api.ListPage{}, api.InvalidSelector(err.Error()).Within(scope)
	}
	client, fail := r.client(q.Cluster)
	if fail != nil {
		return api.ListPage{}, fail
	}
	kind, err := r.s.resolveKind(ctx, q.Cluster, q.Kind)
	if err != nil {
		return api.ListPage{}, stateOf(err, scope)
	}
	if kind.fetch == nil {
		// A kind this cluster does not serve: `absent`, not `empty`.
		return api.ListPage{}, api.Fail(api.StateAbsent, api.CodeNoSuchKind, kind.reason).Within(scope)
	}
	// One namespace is asked of the API server; several are asked of the whole
	// cluster and filtered here. client-go lists one namespace or all, and several
	// concurrent listings would have several ways to half-fail into a table that
	// looks complete. `q.Namespace` is never handed to the fetch verbatim: two names
	// would arrive as one namespace, `"argocd,longhorn-system"`, and come back
	// falsely empty. The filter runs before anything else is built, so the sort, the
	// widths, the count and the selection bar describe the same rows.
	selection := parseNamespaces(q.Namespace)
	asked := ""
	if len(selection) == 1 {
		asked = selection[0]
	}
	// Several namespaces also go to the fetch, so a built-in kind narrows its rows
	// before matching and the filter control offers only their values. The narrowing
	// below stays for the kinds whose fetch does not take the selection.
	var within []string
	if len(selection) > 1 {
		within = selection
	}
	table, err := kind.fetch(ctx, client, listAsk{namespace: asked, within: within, selector: q.Selector,
		search: q.Search, filters: parseFilters(q.Filters)})
	if err != nil {
		return api.ListPage{}, stateOf(err, scope)
	}
	if len(selection) > 1 {
		table.Rows = rowsInNamespaces(table.Rows, selection)
	}
	var allowed []string
	if len(selection) == 0 && table.Access.Narrowed {
		var refused error
		allowed, refused = r.listableIn(ctx, q.Cluster, client.Context, table.Access, table.Rows)
		if refused != nil {
			return api.ListPage{}, stateOf(refused, scope)
		}
	}
	page := r.pageOf(ctx, q, kind, table)
	page.AccessNote = r.listAccessNote(ctx, q.Cluster, table.Access, selection)
	page.AllowedNamespaces = allowed
	return page, nil
}

// listableIn is, for a list narrowed across every namespace, the namespaces in
// which the person may list the kind, or nil where that set cannot be
// established. A person who may list it in none is refused the list rather than
// shown an empty one. Only a fresh reading of the shared namespace hold is used,
// and the refusal also needs the narrowed read to have drawn no row, so a stale
// hold cannot refuse a list that is showing rows.
func (r apiReads) listableIn(ctx context.Context, cluster, reviewed string, seen access.Visible,
	rows []resources.Row) ([]string, error) {
	held := r.s.namespaceAnswer(ctx, cluster)
	if held.state() != namespacesListed {
		return nil, nil
	}
	allowed, complete, err := r.s.gate.ListableIn(ctx, reviewed, seen.Asked, held.names)
	if err != nil {
		if len(rows) > 0 {
			return nil, nil
		}
		return nil, err
	}
	if !complete || seen.Unreviewed {
		return nil, nil
	}
	for _, row := range rows {
		if row.Namespace != "" && !slices.Contains(allowed, row.Namespace) {
			allowed = append(allowed, row.Namespace)
		}
	}
	slices.Sort(allowed)
	return allowed, nil
}

// listAccessNote is what a list narrowed by the access review says about itself,
// or "" for the cluster's whole answer. Built from the question, never from the
// rows, which would reveal how many namespaces hold hidden objects. For a
// selection of several namespaces it names the selected ones the person may not
// list, and is absent when all were allowed.
func (r apiReads) listAccessNote(ctx context.Context, cluster string, seen access.Visible, selection []string) string {
	if !seen.Filtered() {
		return ""
	}
	what := qualifiedResource(seen.Asked)
	if len(selection) > 1 {
		var denied []string
		unreviewed := false
		for _, namespace := range selection {
			question := seen.Asked
			question.Namespace = namespace
			err := r.s.gate.Check(ctx, cluster, question)
			switch {
			case errors.Is(err, access.ErrDenied):
				denied = append(denied, namespace)
			case err != nil:
				unreviewed = true
			}
		}
		switch {
		case len(denied) > 0:
			return "You may not list " + what + " in " + joinWords(denied) +
				", so the rows of " + pick(len(denied), "that namespace", "those namespaces") + " are not shown."
		case unreviewed:
			return "Whether you may list " + what + " in every selected namespace could not be asked, " +
				"so the rows of a namespace that could not be reviewed are not shown."
		}
		return ""
	}
	switch {
	case seen.Narrowed && seen.Unreviewed:
		return "You may not list " + what + " in every namespace, so this shows only the namespaces where you may - " +
			"and a namespace whose review could not be made is left out too."
	case seen.Narrowed:
		return "You may not list " + what + " in every namespace, so this shows only the namespaces where you may."
	}
	return "A namespace whose access review could not be made is left out of this list."
}

// qualifiedResource names a reviewed resource as `kubectl` would: `pods`,
// `deployments.apps`, `certificates.cert-manager.io`.
func qualifiedResource(attrs access.Attributes) string {
	if attrs.Group == "" {
		return attrs.Resource
	}
	return attrs.Resource + "." + attrs.Group
}

// pick chooses one of two words by a count.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// pageOf turns the list view model into the contract's grid, generic over kind.
// The descriptor's per-column facts (Secondary, Copyable, Gauged, and which
// column cannot be hidden) travel with it: a client cannot re-derive them from
// the cells.
func (r apiReads) pageOf(ctx context.Context, q api.ListQuery, kind listKind, table resources.Table) api.ListPage {
	arrangement := r.s.columns.Arrangement(table.Kind, titlesOfTable(table))

	page := api.ListPage{
		Kind:       kind.slug,
		Label:      kind.title,
		Namespaced: table.Namespaced,
		Columns:    arrangedColumns(table.Columns, arrangement, table.Offered, true),
		Rows:       make([]api.Row, 0, len(table.Rows)),
		Search:     q.Search,
		Selector:   q.Selector,
		Namespaces: parseNamespaces(q.Namespace),
		// The kind's own interval: one front-end interval would be wasteful on some
		// kinds and stale on others.
		RefreshSeconds:    refreshSecondsOf(kind.refresh),
		ColumnsCustomised: arrangement.Custom,
	}
	page.MeasurementState, page.MeasurementNote = r.measurement(ctx, q, table.Kind)

	keys := make([]string, 0, len(page.Columns))
	for _, column := range page.Columns {
		keys = append(keys, column.Key)
	}

	for _, row := range table.Rows {
		out := api.Row{
			ID:        rowID(row),
			Name:      row.Name,
			Namespace: row.Namespace,
			Href:      row.Href,
			Cells:     make(map[string]api.Cell, len(row.Cells)),
			Unchanged: unchangedVerbs(table.Kind, row.Switches),
		}
		for i, cell := range row.Cells {
			if i >= len(keys) {
				break
			}
			out.Cells[keys[i]] = cellOf(cell)
		}
		page.Rows = append(page.Rows, out)
	}
	page.Count = len(page.Rows)

	// Only the filters the table says took effect, so no chip appears over a list it
	// did not narrow.
	for _, applied := range table.Filters {
		if applied.Column >= 0 && applied.Column < len(keys) {
			page.Filters = append(page.Filters, api.ListFilter{Column: keys[applied.Column], Value: applied.Value})
		}
	}

	// A selected namespace this cluster does not have: said on the page, even when
	// there are rows, since a selection with one missing gives a short table that
	// looks complete. Namespaced kinds only. The read is the per-cluster hold, so a
	// frequent refresh costs one listing per namespaceTTL.
	if table.Namespaced {
		page.UnknownNamespace = r.s.namespaceUnknown(ctx, q.Cluster, page.Namespaces)
	}

	// Read-only is a property of the cluster, never of the kind.
	readOnly := false
	if writable, note := r.s.writable(q.Cluster); !writable {
		readOnly = true
		page.ReadOnlyNote = note
	}
	// Then whether this person may, verb by verb, in the namespaces the list shows
	// (access.go).
	page.Bulk = r.s.gatedBulk(ctx, q.Cluster, table.Kind, table.Namespaced, page.Namespaces,
		bulkAffordance(q.Cluster, table.Kind, readOnly))
	// And the "+": `create` in the namespace in view (apicreate.go).
	page.Create = r.s.createAffordance(ctx, q.Cluster, table.Kind, table.Namespaced, page.Namespaces)
	return page
}

// arrangedColumns is a list's columns as the contract carries them, arranged
// by the column store. A list filtered in the browser (Helm) passes offerValues
// false: its client collects the values from its rows.
func arrangedColumns(declared []resources.Column, arrangement columns.Layout, offered map[int][]string, offerValues bool) []api.Column {
	out := make([]api.Column, 0, len(declared))

	// The stored arrangement is a permutation of the titles, so it is indexed by
	// Column.At, never positionally.
	stored := make(map[int]columns.Column, len(arrangement.Columns))
	order := make(map[int]int, len(arrangement.Columns))
	for at, column := range arrangement.Columns {
		stored[column.At] = column
		order[column.At] = at
	}

	// The namespace column, so its cells can be controls; -1 where there is none.
	namespaceAt := namespaceColumn(declared)

	for i, column := range declared {
		key := columnKey(column.Title, i)
		one := api.Column{
			Key: key, Label: column.Title, Visible: true, Order: i, Sortable: boolOf(true),
			// Straight off the descriptor: facts about the kind.
			Secondary: column.Secondary,
			Gauged:    column.Gauged,
			Copyable:  column.Copyable,
			// No descriptor declares a prose column, so false is the faithful answer.
			Prose: false,
			Role:  roleOf(i, namespaceAt),
			// What the filter control offers: the table's own answer, collected while the
			// rows were matched.
			Filterable: column.Filterable,
		}
		if column.Filterable && offerValues {
			// Present on every Filterable column, empty where nothing is left, never null.
			values := offered[i]
			if values == nil {
				values = []string{}
			}
			one.FilterValues = &values
		}
		if i == 0 {
			// The descriptor's first column, not whichever was dragged leftmost.
			// internal/columns refuses to hide it, so the menu draws it as a fact.
			one.LockedNote = "always shown — it is what says which object each row is"
		}
		if column.Numeric {
			one.Align = "end"
		}
		// Secondary columns are off by default.
		if column.Secondary {
			one.Visible = false
		}
		if held, ok := stored[i]; ok {
			one.Order = order[i]
			if held.Decided {
				one.Visible = !held.Hidden
			}
			// A share of the table, not pixels (columns.Column.Width): only the client knows
			// how wide the panel is.
			if held.Width > 0 {
				width := held.Width
				one.Width = &width
			}
		}
		out = append(out, one)
	}
	return out
}

// roleOf says what a column draws, for the two cases where a client must style
// or address a cell by what it *is* rather than by where it sits.
//
// The name is the **descriptor's** first column (List.NameAt) and not whichever
// one has been dragged to the left, so a name dragged to the far right is still
// a name and an age dragged to the left is not one.
//
// "age" is in the contract's enum and is never produced here, deliberately: no
// age column is styled, and giving one a role would be adding a capability no
// page draws.
func roleOf(at, namespaceAt int) string {
	// The namespace is tested first: the Events descriptor's first column is
	// Namespace, and it must be the namespace control there. Such a table has no
	// `name` column.
	switch at {
	case namespaceAt:
		return api.RoleNamespace
	case 0:
		return api.RoleName
	}
	return api.RoleOther
}

// cellOf carries a cell's own state: a cell may be unreadable while its row is
// fine, and must not draw as 0.
func cellOf(cell resources.Cell) api.Cell {
	out := api.Cell{
		Text: cell.Text,
		Tone: toneOf(cell.Class),
		Rank: cell.Rank,
		// What hovering says, on its own member, so the address Ref below cannot
		// overwrite it.
		Hint: cell.Hint,
		// What the copy control takes: shown is not obtainable (`+2 more`).
		Copy: cell.Copy,
	}
	if cell.Value != 0 {
		out.Sort = cell.Value
	}
	if len(cell.Addresses) > 0 {
		// The pieces concatenate to exactly Text, separators included, so a client
		// never joins them itself.
		out.Parts = make([]api.CellPart, 0, len(cell.Addresses))
		for _, address := range cell.Addresses {
			out.Parts = append(out.Parts, api.CellPart{Text: address.Text, Href: address.Href})
		}
	}
	if len(cell.Addresses) > 0 && cell.Addresses[0].Href != "" {
		out.Ref = &api.Ref{Text: cell.Addresses[0].Text, Href: cell.Addresses[0].Href}
	}
	if cell.Gauge != nil {
		// The bar, with its own value, since the cell's text is the reading ("1163m")
		// and not the percentage. Nil stays nil: an unanswered source draws a dash and
		// no track, since an empty track reads as an idle machine.
		out.Gauge = &api.Gauge{Percent: cell.Gauge.Percent, Tone: gaugeToneOf(cell.Gauge.Class)}
	}
	return out
}

// gaugeToneOf keeps the bar's tone inside the contract's enum: the same
// vocabulary as a cell's, a different decision (a node at 91% memory is well
// packed, not an incident).
func gaugeToneOf(class string) string {
	switch class {
	case "ok", "warn", "bad":
		return class
	}
	return ""
}

func toneOf(class string) string {
	switch class {
	case "ok", "warn", "bad":
		return class
	}
	return ""
}

func rowID(row resources.Row) string {
	if row.Namespace == "" {
		return row.Name
	}
	return row.Namespace + "/" + row.Name
}

// columnKey is stable within a list and derived from the title, with the index
// appended: a CRD's printer columns may share a title.
func columnKey(title string, index int) string {
	slug := strings.ToLower(strings.TrimSpace(title))
	slug = strings.ReplaceAll(slug, " ", "-")
	if slug == "" {
		slug = "column"
	}
	return fmt.Sprintf("%s-%d", slug, index)
}

// parseFilters reads the repeatable `filter` parameter. A value that is not
// `<column>:<value>` is dropped here, one naming a column that cannot be filtered
// further down; neither narrows nor is echoed.
func parseFilters(raw []string) []resources.Filter {
	var filters []resources.Filter
	for _, one := range raw {
		if filter, ok := resources.ParseFilter(one); ok {
			filters = append(filters, filter)
		}
	}
	return filters
}

func titlesOfTable(table resources.Table) []string {
	titles := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		titles = append(titles, column.Title)
	}
	return titles
}

// bulkAffordance says which bulk verbs this list offers and whether they are
// permitted. The verb set is the kind's (verbsFor), in verbsFor's order, which is
// what puts Evict before Delete. A pod selection is not offered Restart: on a pod
// it is a delete, and the bar would offer one request under two words. A kind
// with no bulk verb answers nil, which keeps the checkboxes off. Scope
// (cluster|kind) is the server's to name.
func bulkAffordance(cluster, kind string, readOnly bool) *api.BulkAffordance {
	out := &api.BulkAffordance{Scope: cluster + "|" + kind}
	for _, verb := range verbsFor(kind) {
		if !actions.IsBulk(verb) {
			continue
		}
		if verb == actions.Restart && isPodKind(kind) {
			continue
		}
		spec, ok := specFor(verb, kind)
		if !ok {
			continue
		}
		entry := api.BulkVerb{Verb: string(verb), Label: spec.Label, Permitted: !readOnly}
		if spec.Danger {
			entry.Destructive = boolOf(true)
		}
		if readOnly {
			reason := api.Because(api.CodeVerbDenied, "this cluster is marked read-only").Retry(false)
			entry.Reason = &reason
		}
		out.Verbs = append(out.Verbs, entry)
	}
	if len(out.Verbs) == 0 {
		return nil
	}
	return out
}
