package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/apiraw"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// asTable is the Accept header that has the API server answer a list as its
// table: the kind's printer columns, as `kubectl get` draws them. Only this
// one, so an API server that cannot draw a table answers 406 instead of
// objects.
const asTable = "application/json;as=Table;g=meta.k8s.io;v=v1"

// listTable is List for a query that asks for Columns: the same review, the
// same narrowing, Limit and paging, over the API server's table of the kind,
// each row carrying its object's metadata (`includeObject=Metadata`) and never
// its body.
func (h host) listTable(r *http.Request, client *cluster.Client, q extension.ListQuery,
	base metav1.ListOptions, question access.Attributes) (extension.Page, error) {
	var columns []string
	namespaced, more := false, false
	rows, seen, err := access.List(r.Context(), h.s.gate, client.Context, question,
		func(row *extension.Row) string { return row.Namespace },
		func(ctx context.Context) ([]extension.Row, error) {
			// read is one request, inside the door so the read walk sees it
			// there. Every page of one list must draw the same columns, or the
			// rows' cells would be aligned on names they were not sent under.
			read := func(ctx context.Context, opts metav1.ListOptions) (*tablePage, error) {
				page, err := readTable(ctx, client, q, opts)
				if err != nil {
					return nil, err
				}
				if columns == nil {
					columns = page.columns
				} else if !slices.Equal(columns, page.columns) {
					return nil, fmt.Errorf("the API server's table of %s changed its columns between two pages",
						qualifiedResource(question))
				}
				return page, nil
			}
			if q.Limit > 0 && !access.Narrowing(ctx) {
				// One request for the rows asked for, not from the watch
				// cache, as for a list of objects.
				opts := base
				opts.Limit, opts.ResourceVersion = q.Limit, ""
				page, err := read(ctx, opts)
				if err != nil {
					return nil, err
				}
				more = page.GetContinue() != ""
				namespaced = anyNamespaced(page.rows)
				return page.rows, nil
			}
			all, err := resources.Paged(ctx, base, read,
				func(p *tablePage) []extension.Row { return p.rows })
			namespaced = anyNamespaced(all)
			return all, err
		})
	if err != nil {
		// An error and never an empty page, as for a list of objects.
		return extension.Page{}, err
	}
	keep, page, err := listOutcome(r.Context(), client, q, question, seen, namespaced, more, len(rows))
	if err != nil {
		return extension.Page{}, err
	}
	page.Columns, page.Rows = columns, rows[:keep]
	return page, nil
}

func anyNamespaced(rows []extension.Row) bool {
	for i := range rows {
		if rows[i].Namespace != "" {
			return true
		}
	}
	return false
}

// tablePage is one page of a table: its list metadata (the continue token
// Paged follows), its column names and its rows.
type tablePage struct {
	metav1.ListMeta
	columns []string
	rows    []extension.Row
}

// wireTable is the part of a meta.k8s.io/v1 Table read here. Cells are decoded
// with UseNumber, so a count is not rounded through a float64.
type wireTable struct {
	Kind              string          `json:"kind"`
	Metadata          metav1.ListMeta `json:"metadata"`
	ColumnDefinitions []struct {
		Name string `json:"name"`
	} `json:"columnDefinitions"`
	Rows []struct {
		Cells  []any           `json:"cells"`
		Object json.RawMessage `json:"object"`
	} `json:"rows"`
}

// readTable sends one table request for q with opts. **An answer that is not
// a Table is an error**, never objects decoded in its place: an API server or
// an aggregated one that ignored the Accept header would otherwise hand the
// bodies this read exists not to hold.
//
// Through the core client's raw REST client with an absolute path, as
// resources.AnyObject reads, so the request shares the cluster's transport,
// its rate limiter and its access guard; the dynamic client cannot ask for a
// table.
func readTable(ctx context.Context, client *cluster.Client, q extension.ListQuery, opts metav1.ListOptions) (*tablePage, error) {
	if client.Clients == nil {
		return nil, fmt.Errorf("no client for %q", client.Context)
	}
	what := qualifiedResource(access.Attributes{Group: q.GVR.Group, Resource: q.GVR.Resource})
	raw, err := apiraw.Do(ctx, client.Clients.CoreV1().RESTClient().Get().
		AbsPath(tablePath(q)...).
		VersionedParams(&opts, scheme.ParameterCodec).
		Param("includeObject", "Metadata").
		SetHeader("Accept", asTable))
	if err != nil {
		return nil, err
	}
	var wire wireTable
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("the table of %s did not parse: %w", what, err)
	}
	if wire.Kind != "Table" {
		return nil, fmt.Errorf("the API server answered a table of %s with %q, not a table", what, wire.Kind)
	}
	page := &tablePage{ListMeta: wire.Metadata, columns: make([]string, len(wire.ColumnDefinitions)),
		rows: make([]extension.Row, 0, len(wire.Rows))}
	for i, column := range wire.ColumnDefinitions {
		page.columns[i] = column.Name
	}
	for i, row := range wire.Rows {
		if len(row.Cells) != len(page.columns) {
			return nil, fmt.Errorf("row %d of the table of %s has %d cells for %d columns",
				i, what, len(row.Cells), len(page.columns))
		}
		if len(bytes.TrimSpace(row.Object)) == 0 || bytes.Equal(bytes.TrimSpace(row.Object), []byte("null")) {
			return nil, fmt.Errorf("row %d of the table of %s carries no object metadata", i, what)
		}
		var object metav1.PartialObjectMetadata
		if err := json.Unmarshal(row.Object, &object); err != nil {
			return nil, fmt.Errorf("the metadata of row %d of the table of %s did not parse: %w", i, what, err)
		}
		if object.Name == "" {
			return nil, fmt.Errorf("row %d of the table of %s names no object", i, what)
		}
		cells := make([]any, len(row.Cells))
		for j, c := range row.Cells {
			cells[j] = tableCell(c)
		}
		page.rows = append(page.rows, extension.Row{
			Namespace:   object.Namespace,
			Name:        object.Name,
			Labels:      object.Labels,
			Annotations: object.Annotations,
			Owners:      object.OwnerReferences,
			Created:     object.CreationTimestamp.Time,
			Cells:       cells,
		})
	}
	return page, nil
}

// tableCell is one cell as the contract promises it: a string, an int64, a
// float64, a bool or nil, and an object or an array as its JSON text.
func tableCell(v any) any {
	switch x := v.(type) {
	case nil, string, bool:
		return x
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		text, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(text)
	}
}

// tablePath is the list's address: `/api/v1` for the core group, else
// `/apis/{group}/{version}`, then the namespace where one is asked, then the
// resource. Every segment was checked to be one path segment (listSegments).
func tablePath(q extension.ListQuery) []string {
	segments := []string{"/apis", q.GVR.Group, q.GVR.Version}
	if q.GVR.Group == "" {
		segments = []string{"/api", q.GVR.Version}
	}
	if q.Namespace != "" {
		segments = append(segments, "namespaces", q.Namespace)
	}
	return append(segments, q.GVR.Resource)
}
