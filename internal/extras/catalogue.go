package extras

import (
	"sort"
	"strings"
	"sync"
)

// The catalog is registered at startup, not compiled in.
//
// The additive pages in `extras/` import the console, and only `cmd/kubetower`
// names them, so this package cannot hold a list of them. `app.Run` calls
// Register once with the extensions it was given, before any handler is built.
// An empty catalog is the ordinary state of the console on its own: `Known`
// false, `Store.Enabled` false, no Extra heading in the menu.
//
// The keys are not defined here: a page's key is its own, and this package
// stores and compares it without judging which strings are legitimate.
var (
	mu        sync.RWMutex
	catalogue []Extra
	legacy    = map[string]string{}
)

// Register adds extras to the catalog, which keeps them sorted by label,
// case-insensitively: the menu and Settings > Extra draw the same alphabetical
// order whatever order main lists them in. Keys do not enter into it. It is called once, at
// startup, before the server is built, and is safe to call with nothing.
//
// A duplicate key is kept out rather than overwriting, so a wiring mistake
// shows as a missing page rather than as a page that silently changed
// identity. `app.Run` refuses duplicates outright; this is the backstop.
func Register(list ...Extra) {
	mu.Lock()
	defer mu.Unlock()
	for _, e := range list {
		if e.Key == "" {
			continue
		}
		if indexOf(e.Key) >= 0 {
			continue
		}
		catalogue = append(catalogue, e)
		for _, old := range e.Legacy {
			if old != "" && old != e.Key {
				legacy[old] = e.Key
			}
		}
	}
	sort.SliceStable(catalogue, func(i, j int) bool {
		return strings.ToLower(catalogue[i].Label) < strings.ToLower(catalogue[j].Label)
	})
}

// Reset empties the catalog. Tests only: a running console whose catalog
// changed would draw a menu that disagrees with its own routes.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	catalogue = nil
	legacy = map[string]string{}
}

// indexOf is the position of key in the catalog, or -1. Callers hold mu.
func indexOf(key string) int {
	for i, e := range catalogue {
		if e.Key == key {
			return i
		}
	}
	return -1
}
