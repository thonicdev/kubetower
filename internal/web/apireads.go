package web

import (
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// apiReads adapts the cluster reads, the writes and the dock onto the JSON
// contract. Each calls the domain function that owns the answer, so no two
// endpoints can report different things about one cluster.

type apiReads struct{ s *Server }

func (r apiReads) client(name string) (*cluster.Client, *api.Failure) {
	client, err := r.s.clusters.Client(name)
	if err != nil {
		return nil, stateOf(err, api.Scope{Cluster: name})
	}
	return client, nil
}
