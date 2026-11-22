package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// awkwardClusterNames are what a context is really called on a managed
// cluster, and the characters an unescaped path cannot carry.
var awkwardClusterNames = []string{
	"arn:aws:eks:eu-west-1:123456789012:cluster/prod",
	"team#1?prod%",
	"prod one",
}

// arnClusters knows the fixture's clusters and, beside them, the awkward names.
type arnClusters struct{ fakeClusters }

func (c arnClusters) Known(name string) bool {
	for _, awkward := range awkwardClusterNames {
		if name == awkward {
			return true
		}
	}
	return c.fakeClusters.Known(name)
}

// The preview's `channel` names the cluster escaped, so a name holding `/`, `#`
// or `%` reaches the shell it was agreed for.
//
// The address was `"/api/c/" + cluster + "/ws/nodeshell"`,
// so an EKS ARN's `/` added a segment no route matches, `#` turned the rest into
// a fragment and `%` made it unparseable. Asserted by spending the plan on the
// address handed back and reading which cluster the backend was asked for.
func TestTheNodeShellChannelCarriesAClusterNameHoldingReservedCharacters(t *testing.T) {
	for _, name := range awkwardClusterNames {
		channels := allChannels()
		mux := routerWithClusters(channels, arnClusters{})

		plan := previewNodeShellPlan(t, mux, url.PathEscape(name), "node-3")
		request, err := http.NewRequest(http.MethodGet, "http://console"+plan.Channel+"?token="+plan.Token, nil)
		if err != nil {
			t.Errorf("%q: the preview handed back %q, which is not an address: %v", name, plan.Channel, err)
			continue
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusSwitchingProtocols {
			t.Errorf("%q: the address the preview handed back, %q, answered %d: %s",
				name, plan.Channel, recorder.Code, recorder.Body.String())
			continue
		}
		if len(channels.clusters) != 1 || channels.clusters[0] != name {
			t.Errorf("%q: the shell opened on %q", name, channels.clusters)
		}
	}
}
