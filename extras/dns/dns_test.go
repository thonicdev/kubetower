package dns

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// fakeHost is extension.Host with DNSWindow made real; any other door panics
// on the nil embedded interface, which fails the test that reached it.
type fakeHost struct {
	extension.Host
	answer extension.DNS
	err    error
	calls  atomic.Int64
	spans  []extension.DNSSpan
	// corefile is the text of the coredns ConfigMap; listErr is what reading it
	// answers instead.
	corefile string
	listErr  error
	listed   []extension.ListQuery
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.listed = append(h.listed, q)
	if h.listErr != nil {
		return extension.Page{}, h.listErr
	}
	if h.corefile == "" {
		return extension.Page{Complete: true}, nil
	}
	cm := unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "coredns", "namespace": q.Namespace},
		"data":     map[string]any{"Corefile": h.corefile}}}
	return extension.Page{Items: []unstructured.Unstructured{cm}, Complete: true}, nil
}

func (h *fakeHost) DNSWindow(_ *http.Request, _ string, span extension.DNSSpan) (extension.DNS, error) {
	h.calls.Add(1)
	h.spans = append(h.spans, span)
	return h.answer, h.err
}

func (h *fakeHost) VolumeFill(*http.Request, string, []extension.ClaimRef) (extension.VolumeFill, error) {
	return extension.VolumeFill{State: extension.WindowNoPrometheus}, nil
}

func get(t *testing.T, h extension.Host, query string) (*httptest.ResponseRecorder, View) {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/dns"+route.Pattern, route.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x/c1/dns/stats"+query, nil))
	var v View
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("answer is not JSON: %v", err)
		}
	}
	return rec, v
}

func read() extension.DNS {
	return extension.DNS{State: extension.WindowRead, Namespace: "kube-system", Version: "1.11.1", ReadAt: time.Unix(100, 0),
		Now: extension.DNSFigures{QPS: 120, NXDomain: 0.1, ServFail: 0.44, P50: 0.002, P99: 0.25, CacheHit: 0.8,
			HasShares: true, HasLatency: true, HasCache: true},
		Series: extension.DNSSeries{Step: 15, QPS: []extension.Sample{{T: 1, V: 2}, {T: 2, V: math.NaN()}},
			ByRcode: map[string][]extension.Sample{"SERVFAIL": {{T: 1, V: 1}}}},
		Pods: []extension.DNSPod{
			{Pod: "coredns-b", Scraped: false},
			{Pod: "coredns-a", Node: "n1", Scraped: true, Figures: extension.DNSFigures{QPS: 60}},
		},
		Upstreams: []extension.DNSUpstream{
			{To: "8.8.8.8:53", QPS: 5, P99: 0.1, HasLatency: true, Answers: true},
			{To: "10.0.0.1:53", HealthFailPS: 3},
		}}
}

func TestStatsAnswersFiguresAbsentAsNull(t *testing.T) {
	h := &fakeHost{answer: read()}
	rec, v := get(t, h, "?span=6h")
	if rec.Code != 200 || v.State != "read" || v.Span != "6h" {
		t.Fatalf("got %d %+v", rec.Code, v)
	}
	if h.spans[0] != extension.DNSSpan6h {
		t.Fatalf("span = %v", h.spans)
	}
	if v.Now == nil || v.Now.ServFail == nil || *v.Now.ServFail != 0.44 {
		t.Fatalf("now = %+v", v.Now)
	}
	if len(v.Series.QPS) != 1 {
		t.Fatalf("a NaN point must be left out: %v", v.Series.QPS)
	}
	if v.Pods[0].Pod != "coredns-a" || v.Pods[0].Figures == nil || v.Pods[1].Figures != nil {
		t.Fatalf("pods = %+v (sorted; an unscraped pod has no figures)", v.Pods)
	}
	if v.Pods[0].Figures.P99 != nil || v.Pods[0].Figures.NXDomain != nil {
		t.Fatalf("a figure the window did not hold must be null, not 0: %+v", v.Pods[0].Figures)
	}
	if u := v.Upstreams[0]; u.To != "10.0.0.1:53" || u.Answers || u.P99 != nil || u.HealthPS != 3 {
		t.Fatalf("a dead upstream = %+v", u)
	}
}

func TestStatsStatesCarryNoFigures(t *testing.T) {
	for _, state := range []extension.WindowState{extension.WindowNoPrometheus, extension.WindowUnreadable, extension.WindowEmpty} {
		h := &fakeHost{answer: extension.DNS{State: state, Detail: "why"}}
		_, v := get(t, h, "")
		if v.State != string(state) || v.Detail != "why" || v.Now != nil || v.Series != nil || len(v.Pods) != 0 {
			t.Errorf("%s: %+v", state, v)
		}
		if h.spans[0] != DefaultSpan {
			t.Errorf("default span = %v", h.spans)
		}
	}
}

func TestStatsRefusals(t *testing.T) {
	h := &fakeHost{}
	if rec, _ := get(t, h, "?span=2h"); rec.Code != 400 || h.calls.Load() != 0 {
		t.Fatalf("a span that is not one: %d, reads %d", rec.Code, h.calls.Load())
	}
	cases := map[string]struct {
		err  error
		code int
	}{
		"denied":       {fmt.Errorf("x: %w", extension.ErrDenied), 403},
		"unreviewable": {fmt.Errorf("x: %w", extension.ErrUnreviewable), 503},
		"unknown":      {fmt.Errorf("x: %w", extension.ErrUnknownCluster), 404},
		"failed":       {errors.New("boom"), 502},
	}
	for name, c := range cases {
		if rec, _ := get(t, &fakeHost{err: c.err}, ""); rec.Code != c.code {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, c.code, rec.Body)
		}
	}
}

func TestSuggest(t *testing.T) {
	d := New()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if s, err := d.Suggest(r, &fakeHost{answer: read()}, "c1"); err != nil || !strings.Contains(s, "CoreDNS") {
		t.Fatalf("suggest = %q, %v", s, err)
	}
	for _, state := range []extension.WindowState{extension.WindowNoPrometheus, extension.WindowEmpty, extension.WindowUnreadable} {
		if s, err := d.Suggest(r, &fakeHost{answer: extension.DNS{State: state}}, "c1"); s != "" || err != nil {
			t.Errorf("%s: %q, %v", state, s, err)
		}
	}
	if _, err := d.Suggest(r, &fakeHost{err: extension.ErrDenied}, "c1"); err == nil {
		t.Error("a refusal is an error, not a suggestion")
	}
}
