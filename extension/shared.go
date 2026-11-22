package extension

import (
	"context"
	"net/http"
)

// sharedKey marks a request served by a console several people share.
type sharedKey struct{}

// SharedConsole reports whether r is served by a console several people share:
// the server profile (`--incluster`). The core marks every request it hands an
// extension under that profile - its routes and its Suggester - so an
// extension can confine what it looks for there to what the deployment names,
// rather than to what it finds in the cluster.
//
// False on the desktop and in a container, and for a request the core did not
// hand over (a test that does not mark it).
func SharedConsole(r *http.Request) bool {
	if r == nil {
		return false
	}
	shared, _ := r.Context().Value(sharedKey{}).(bool)
	return shared
}

// MarkShared is r marked as served by a shared console (SharedConsole). The
// core calls it; an extension that calls it only confines itself further.
func MarkShared(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sharedKey{}, true))
}
