package access

import (
	"net/http"
	"testing"
)

// passThrough answers every request without a network, so a benchmark of the
// guard measures the guard.
type passThrough struct{ resp *http.Response }

func (p passThrough) RoundTrip(*http.Request) (*http.Response, error) { return p.resp, nil }

// BenchmarkGuardRoundTripPlainGet is the guard on the read path it sits on most:
// a GET of one object, with no stamp, under no prefix and under one.
func BenchmarkGuardRoundTripPlainGet(b *testing.B) {
	for _, bc := range []struct{ name, root, path string }{
		{"root", "", "/api/v1/namespaces/shop/pods/api-1"},
		{"grouped", "", "/apis/apps/v1/namespaces/shop/deployments/api"},
		{"prefixed", "/k8s/clusters/c-1", "/k8s/clusters/c-1/api/v1/namespaces/shop/pods/api-1"},
	} {
		b.Run(bc.name, func(b *testing.B) {
			g := &guard{next: passThrough{resp: &http.Response{StatusCode: http.StatusOK}}, cluster: "test", root: bc.root}
			req, err := http.NewRequest(http.MethodGet, "https://cluster"+bc.path, nil)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := g.RoundTrip(req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
