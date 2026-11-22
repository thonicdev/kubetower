package web

// The cluster dashboard's wiring and words. Everything on it comes out of
// `internal/overview`; this file builds every URL in Go, so escaping is got right
// in one place.

import (
	"net/url"
	"strings"

	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/internal/resources"
)

// Say is the same destination under different words: the dashboard names one
// list in several places, and one address behind all of them is one to get right.
func (l link) Say(text string) link {
	l.Text = text
	return l
}

// history hands the overview package the Prometheus side of the metrics layer,
// or a true nil: an interface holding a nil pointer is not nil, and `overview`
// checks against nil to say why there is no history.
func (s *Server) history() overview.Metrics {
	if s.series == nil {
		return nil
	}
	return s.series
}

// Every link on this page comes from one of the three builders below, and the
// kind decides there whether there is a namespace: `resources.Lookup` is asked,
// and a kind that is not namespaced has the namespace dropped before a URL is
// built. A cluster-scoped name taken for a namespace would pass every validator
// and come back "not found". The kind is passed as Kubernetes spells it ("Node");
// Lookup's alias table accepts the singular.

// objectLink is where one named object leads: its kind's list, narrowed to it,
// with `open=` so the list opens its drawer on arrival. An unknown kind yields
// text and no link rather than a guessed URL that would 404.
func objectLink(cluster, kind, namespace, name, text, hint string) link {
	descriptor, ok := resources.Lookup(kind)
	if !ok || name == "" {
		return link{Text: text}
	}
	if !descriptor.Namespaced {
		namespace = ""
	}
	return link{
		Text: text,
		Href: objectPageURL(cluster, descriptor.Kind, namespace, name),
		Hint: hint,
	}
}

// objectPageURL is a kind's list narrowed to one object, asked to open it. The
// separator is worked out rather than assumed, so a URL whose query is part of
// the path cannot be built.
func objectPageURL(cluster, kind, namespace, name string) string {
	base := pageURL(cluster, kind, namespace, name, listOrder{})
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + separator + "open=" + url.QueryEscape(name)
}

// listLink is where a count leads: a whole list, or one namespace of it. A
// cluster-scoped kind is given no namespace, so `?namespace=` never appears on a
// list that would ignore it.
func listLink(cluster, kind, namespace, text, hint string) link {
	descriptor, ok := resources.Lookup(kind)
	if !ok {
		return link{Text: text}
	}
	if !descriptor.Namespaced {
		namespace = ""
	}
	return link{
		Text: text,
		Href: pageURL(cluster, descriptor.Kind, namespace, "", listOrder{}),
		Hint: hint,
	}
}

// scopeLink is the namespace cell of a table: the same kind, narrowed to that
// namespace (what else of this is in there, not the Namespace object). An empty
// namespace leads nowhere.
func scopeLink(cluster, kind, namespace string) link {
	if namespace == "" {
		return link{Text: resources.Missing}
	}
	descriptor, ok := resources.Lookup(kind)
	if !ok {
		return link{Text: namespace}
	}
	if !descriptor.Namespaced {
		return link{Text: namespace}
	}
	return listLink(cluster, kind, namespace, namespace, descriptor.Title+" in "+namespace)
}

// link is one place the dashboard can send the reader: a list, or one named
// object as its list narrowed to it with `open=<name>`. Text empty means nothing
// to draw; Href empty means nowhere honest to go, and the text is drawn unlinked.
type link struct {
	Text string
	Href string
	// Hint is the title attribute, what the click will do: a link to a list and to
	// one object look alike and land in different places.
	Hint string
}
