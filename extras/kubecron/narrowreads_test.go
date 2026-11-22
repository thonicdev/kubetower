package kubecron

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The run route only needs to know whether the person may list any CronJob
// before the collector is asked: it asks for one row, never the whole list.
// The CronJob the run names is then read by name.
func TestTheRunRouteListsNoWholeCronJobList(t *testing.T) {
	h, _ := hostWithCollector(t)
	rec := serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r1", map[string]string{"id": "r1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(h.listed) == 0 || h.listed[0].GVR != cronJobsGVR || h.listed[0].Limit != 1 {
		t.Fatalf("the first read was %+v, want the CronJobs with a limit of one", h.listed)
	}
	for _, q := range h.listed {
		if q.GVR == cronJobsGVR && q.Limit == 0 && q.FieldSelector == "" {
			t.Errorf("a whole CronJob list was read: %+v", q)
		}
	}
}

// A person with no CronJob to show is answered empty on the list, the heatmap
// and a day, without a tunnel to the collector: there is nothing to ask it.
// The collector is still looked for, so the page says where it stands.
func TestAnEmptyCronJobListOpensNoTunnel(t *testing.T) {
	for _, route := range []struct {
		name    string
		handler func(KubeCron, *fakeHost) http.Handler
		path    string
		values  map[string]string
	}{
		{"list", func(k KubeCron, h *fakeHost) http.Handler { return k.list(h) }, "/x/docker-desktop/kubecron/", nil},
		{"daily", func(k KubeCron, h *fakeHost) http.Handler { return k.heatmap(h) }, "/x/docker-desktop/kubecron/daily", nil},
		{"day", func(k KubeCron, h *fakeHost) http.Handler { return k.day(h) }, "/x/docker-desktop/kubecron/days/2026-10-20",
			map[string]string{"day": "2026-10-20"}},
	} {
		t.Run(route.name, func(t *testing.T) {
			h, fc := hostWithCollector(t)
			h.hiddenNames["ops/nightly"] = true // now neither CronJob is theirs
			rec := serve(t, route.handler(testExtra(), h), route.path, route.values)
			var out struct {
				Collector collectorState `json:"collector"`
				CronJobs  []any          `json:"cronJobs"`
				Runs      []any          `json:"runs"`
				History   availability   `json:"history"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if len(out.CronJobs) != 0 || len(out.Runs) != 0 {
				t.Errorf("answer %s, want nothing to show", rec.Body)
			}
			if out.Collector.State != "ok" {
				t.Errorf("collector %+v, want the one found, said as found", out.Collector)
			}
			if route.name == "list" && (out.History.OK || out.History.Detail != nothingToAsk) {
				t.Errorf("history %+v, want %q", out.History, nothingToAsk)
			}
			fc.mu.Lock()
			asked := len(fc.queries)
			fc.mu.Unlock()
			if h.forwards != 0 || asked != 0 {
				t.Errorf("%d tunnels opened and the collector asked %d times with no CronJob to show", h.forwards, asked)
			}
		})
	}
}
