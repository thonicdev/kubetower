package web

import (
	"net/url"
	"sync"
)

// viewMemory is what a person last did to a list, per cluster and kind, so a
// menu link (a bare `/c/{cluster}/pods`) returns to it instead of the default
// order. In memory only: how a list was arranged ten minutes ago is about what
// someone was looking for then, and should not survive a restart.
type viewMemory struct {
	mu   sync.Mutex
	last map[string]viewState
}

// The filter is deliberately not remembered: the filter box's request races the
// navigation, so the server cannot tell which the person saw last. The sort is a
// single click rendered before anything else can be clicked, so it has no race.
// A filter still survives a link that carries it.

// viewState is the whole of it: how the table was ordered.
type viewState struct {
	order listOrder
}

func newViewMemory() *viewMemory { return &viewMemory{last: map[string]viewState{}} }

func viewKey(cluster, kind string) string { return cluster + "\x00" + kind }

// recall is what a menu link should carry, or the zero value for a kind the
// person has not arranged in this session.
func (v *viewMemory) recall(cluster, kind string) viewState {
	if v == nil {
		return viewState{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.last[viewKey(cluster, kind)]
}

// remembered appends what was last used to a kind's bare path, through `encode`
// so names and escaping match what the page reads back. The namespace is not
// carried: the browser (`web/ui/src/shell/namespaceMemory.ts`) writes it into the
// link itself.
func (v *viewMemory) remembered(path, cluster, kind string) string {
	state := v.recall(cluster, kind)
	if state.order.sort == "" {
		return path
	}
	// url.Values{} and not nil: `encode` calls Set on it, which panics on a nil map.
	if encoded := encode(url.Values{}, "", "", state.order); encoded != "" {
		return path + "?" + encoded
	}
	return path
}
