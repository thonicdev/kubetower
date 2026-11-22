package web

import "sync"

// visits remembers, per cluster, the kind a person last looked at, so the rail
// moves between the same job on different clusters instead of starting over at
// the overview. In memory only: it changes on every navigation, and its value
// lasts one sitting. The kind only, not the filter or sort: an old search is
// worse to come back to than the whole list.
type visits struct {
	mu   sync.RWMutex
	last map[string]string
}

func newVisits() *visits {
	return &visits{last: map[string]string{}}
}

// Remember records that this cluster was last looked at through this kind.
//
// The resolved context name is the key, never the URL's spelling: one cluster
// reaches this process spelt more than one way.
func (v *visits) Remember(cluster, kind string) {
	if v == nil || cluster == "" || kind == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.last[cluster] = kind
}

// Last reports the kind this cluster was last looked at through, or "" for a
// cluster that has not been visited in this run.
func (v *visits) Last(cluster string) string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.last[cluster]
}

// Forget drops a cluster, so a kind that stopped existing is not remembered into
// a 404.
func (v *visits) Forget(cluster string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.last, cluster)
}
