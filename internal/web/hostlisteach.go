package web

import (
	"context"
	"errors"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// errEachStopped carries an error from ListQuery.Each out through the reads,
// to be answered as Each's own.
var errEachStopped = errors.New("the walk was stopped by its callback")

// errLimitReached ends the walk once Limit objects have been handed to Each.
var errLimitReached = errors.New("the walk reached its limit")

// listEach is List for a query that asks for Each: every object handed to
// Each in turn, one page at a time, and dropped with its page. The review and
// the narrowing are a list's; the difference is where narrowing happens.
//
// A list of objects narrows after reading: the console reads the whole kind
// and keeps the rows the person may see. Read that way, a walk would hand Each
// objects the person may not see before they could be dropped. So a
// namespaced kind goes through access.ListIn: where the namespaces with
// objects are comes first, from the kind's table (metadata, no body), each is
// reviewed, and only the namespaces the person may list are walked. A
// cluster-scoped kind has nothing to narrow to and is walked whole or refused.
func (h host) listEach(r *http.Request, client *cluster.Client, q extension.ListQuery,
	base metav1.ListOptions, question access.Attributes) (extension.Page, error) {
	ctx := r.Context()
	scoped := q.Namespace != ""
	if !scoped {
		var err error
		if scoped, err = namespacedKind(ctx, client, q.GVR); err != nil {
			return extension.Page{}, err
		}
	}
	w := walk{q: q}
	// Not from the watch cache at version "0", which may ignore a limit and
	// answer the whole list in one response: the page bound is the point.
	opts := base
	opts.Limit, opts.ResourceVersion = extension.EachPageSize, ""
	api := client.Dynamic.Resource(q.GVR)
	var seen access.Visible
	var err error
	if scoped {
		_, seen, err = access.ListIn(ctx, h.s.gate, client.Context, question,
			// Where the objects are, read as the kind's table: one row of
			// metadata an object and no body, paged and bounded as every
			// list is.
			func(ctx context.Context) ([]string, error) {
				all := q
				all.Namespace = ""
				rows, err := resources.Paged(ctx, base,
					func(ctx context.Context, opts metav1.ListOptions) (*tablePage, error) {
						return readTable(ctx, client, all, opts)
					},
					func(p *tablePage) []extension.Row { return p.rows })
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(rows))
				for i := range rows {
					out = append(out, rows[i].Namespace)
				}
				return out, nil
			},
			// Each read is written inside the closure handed to the review,
			// so the read walk (internal/access's choke test) sees it there.
			func(ctx context.Context, ns string) ([]struct{}, error) {
				if ns == "" {
					return nil, w.pages(ctx, opts, api.List)
				}
				return nil, w.pages(ctx, opts, api.Namespace(ns).List)
			})
	} else {
		_, seen, err = access.List(ctx, h.s.gate, client.Context, question, nil,
			func(ctx context.Context) ([]struct{}, error) { return nil, w.pages(ctx, opts, api.List) })
	}
	if errors.Is(err, errEachStopped) {
		return extension.Page{}, w.eachErr
	}
	if err != nil {
		// An error and never an empty page, as for a list of objects.
		return extension.Page{}, err
	}
	if seen.Filtered() {
		return extension.Page{Complete: false,
			Note: "You may not list " + qualifiedResource(question) + " in every namespace, so this holds only the namespaces where you may."}, nil
	}
	if w.more {
		return extension.Page{Complete: false, Note: limitNote(q.Limit, question)}, nil
	}
	return extension.Page{Complete: true}, nil
}

// walk is one Each read: how many objects were handed over, whether Limit cut
// it, and Each's error when it stopped it.
type walk struct {
	q       extension.ListQuery
	count   int64
	more    bool
	eachErr error
}

// pages walks one list call page by page. Once Limit is reached, a later
// namespace is not read at all.
func (w *walk) pages(ctx context.Context, opts metav1.ListOptions,
	list func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error)) error {
	if w.more {
		return nil
	}
	for {
		page, err := list(ctx, opts)
		if err != nil {
			return err
		}
		if err := w.hand(page.Items); err != nil {
			if errors.Is(err, errLimitReached) {
				return nil
			}
			return err
		}
		opts.Continue = page.GetContinue()
		if opts.Continue == "" {
			return nil
		}
	}
}

// hand gives Each the objects of one page, each dropped as soon as it is
// handed over.
func (w *walk) hand(items []unstructured.Unstructured) error {
	for i := range items {
		if w.q.Limit > 0 && w.count >= w.q.Limit {
			w.more = true
			return errLimitReached
		}
		if err := w.q.Each(items[i]); err != nil {
			w.eachErr = err
			return errEachStopped
		}
		w.count++
		items[i] = unstructured.Unstructured{}
	}
	return nil
}
