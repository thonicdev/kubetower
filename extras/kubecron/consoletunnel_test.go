package kubecron

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
)

// The collector is reached through the console's own tunnel
// (Host.ForwardAsConsole; the test host's Forward refuses), so a person needs
// no port-forward into its namespace, and what they are shown of it is held to
// their own reviewed reads. These pin that a person those reads refuse never
// makes the console open the tunnel.

// A person with no port-forward anywhere gets the history of the CronJobs they
// may list, through the console's tunnel to the collector - and only of those.
func TestThePersonsHistoryComesThroughTheConsolesTunnel(t *testing.T) {
	h, _ := hostWithCollector(t)
	rec := serve(t, testExtra().list(h), "/x/docker-desktop/kubecron/", nil)
	var out listAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.Collector.State != "ok" || len(out.CronJobs) != 1 || out.CronJobs[0].History == nil {
		t.Fatalf("answer %+v, want the visible CronJob's history", out)
	}
	if !slices.Equal(h.forwardedTo, []string{"kubecron/kubecron"}) || h.stopped != 1 {
		t.Errorf("tunnels %v, stopped %d: want the console's one tunnel to the collector, closed", h.forwardedTo, h.stopped)
	}
}

// A person whose CronJob list is refused is answered before the collector is
// looked for, on every route that reads it: no tunnel is opened for them.
func TestARefusedListOpensNoTunnel(t *testing.T) {
	for _, route := range []struct {
		name    string
		handler func(KubeCron, *fakeHost) http.Handler
		path    string
		values  map[string]string
	}{
		{"list", func(k KubeCron, h *fakeHost) http.Handler { return k.list(h) }, "/x/docker-desktop/kubecron/", nil},
		{"detail", func(k KubeCron, h *fakeHost) http.Handler { return k.detail(h) }, "/x/docker-desktop/kubecron/cronjobs/ops/nightly",
			map[string]string{"namespace": "ops", "name": "nightly"}},
		{"run", func(k KubeCron, h *fakeHost) http.Handler { return k.run(h) }, "/x/docker-desktop/kubecron/runs/r1",
			map[string]string{"id": "r1"}},
		{"daily", func(k KubeCron, h *fakeHost) http.Handler { return k.heatmap(h) }, "/x/docker-desktop/kubecron/daily", nil},
		{"day", func(k KubeCron, h *fakeHost) http.Handler { return k.day(h) }, "/x/docker-desktop/kubecron/days/2026-10-20",
			map[string]string{"day": "2026-10-20"}},
	} {
		t.Run(route.name, func(t *testing.T) {
			h, fc := hostWithCollector(t)
			h.listErr = errors.New("cronjobs.batch is forbidden")
			rec := serve(t, route.handler(testExtra(), h), route.path, route.values)
			if rec.Code == http.StatusOK {
				t.Errorf("a refused list answered 200: %s", rec.Body)
			}
			fc.mu.Lock()
			asked := len(fc.queries)
			fc.mu.Unlock()
			if h.forwards != 0 || asked != 0 {
				t.Errorf("%d tunnels opened and the collector asked %d times for a person whose list was refused", h.forwards, asked)
			}
		})
	}
}

// A run names its CronJob only once the collector is asked, so a person who
// may list no CronJob at all is refused before: the console does not dig a
// tunnel to look up a run for somebody who can be shown none.
func TestARunForAPersonWhoMayListNoCronJobOpensNoTunnel(t *testing.T) {
	h, fc := hostWithCollector(t)
	h.hiddenNames["ops/nightly"] = true // now neither CronJob is theirs
	rec := serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r1", map[string]string{"id": "r1"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("%d %s, want 404", rec.Code, rec.Body)
	}
	fc.mu.Lock()
	asked := len(fc.queries)
	fc.mu.Unlock()
	if h.forwards != 0 || asked != 0 {
		t.Errorf("%d tunnels opened and the collector asked %d times for a person who may list no CronJob", h.forwards, asked)
	}

	// The person who may list one gets the run, through the console's tunnel.
	delete(h.hiddenNames, "ops/nightly")
	rec = serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r1", map[string]string{"id": "r1"})
	if rec.Code != http.StatusOK || h.forwards != 1 {
		t.Errorf("%d %s with %d tunnels, want the run through one", rec.Code, rec.Body, h.forwards)
	}
}
