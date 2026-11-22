package kubecron

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Whose rights find the collector: the console's for a collector the
// configuration names, the person's for one found by its label.

func statusOf(t *testing.T, k KubeCron, h *fakeHost) collectorState {
	t.Helper()
	rec := serve(t, k.status(h), "/x/docker-desktop/kubecron/status", nil)
	var out statusAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return out.Collector
}

// Two people, one refused `list services` in the collector's namespace: found
// by its label, the collector is found for the first and not for the second,
// who is not told it exists and causes no tunnel.
func TestACollectorFoundByItsLabelIsFoundOnlyForAPersonWhoMayListIt(t *testing.T) {
	k := testExtra()
	k.collector = collectorConfig{}

	h, _ := hostWithCollector(t)
	if got := statusOf(t, k, h); got.State != "ok" || got.Via != viaLabel || h.forwards != 1 {
		t.Fatalf("for the person who may list it: %+v, %d tunnels", got, h.forwards)
	}

	h, _ = hostWithCollector(t)
	h.hiddenNamespaces = map[string]bool{"kubecron": true}
	got := statusOf(t, k, h)
	if got.State != "absent" || got.Via != "" || strings.Contains(got.Detail, "kubecron/kubecron") ||
		!strings.Contains(got.Detail, "no Service you may list") {
		t.Errorf("for the person refused in its namespace: %+v", got)
	}
	if h.forwards != 0 || len(h.revealed) != 0 || len(h.consoleReads) != 0 {
		t.Errorf("tunnels %d, reveals %v, console reads %v: a collector the person may not list was reached",
			h.forwards, h.revealed, h.consoleReads)
	}
	for _, q := range h.listed {
		if q.GVR.Resource == "services" && q.LabelSelector != collectorLabel {
			t.Errorf("Services listed with %+v; the collector is looked for by its label alone", q)
		}
	}
}

// Found by its label, the token is read for the person (Reveal), never as the
// console: its namespace came from the cluster.
func TestALabelledCollectorsTokenIsReadForThePerson(t *testing.T) {
	k := testExtra()
	k.collector = collectorConfig{}
	h, _ := hostWithCollector(t)
	if got := statusOf(t, k, h); got.State != "ok" {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(h.revealed, []string{"kubecron/kubecron-api#api-token"}) {
		t.Errorf("revealed %v, want the token read for the person", h.revealed)
	}
	if len(h.consoleReads) != 0 {
		t.Errorf("console reads %v for a collector nothing configured", h.consoleReads)
	}
}

// Named by the configuration, the Service and its token are read as the
// console - only those two - and no Service is listed.
func TestAConfiguredCollectorIsReadAsTheConsoleAndNothingIsListed(t *testing.T) {
	h, _ := hostWithCollector(t)
	if got := statusOf(t, testExtra(), h); got.State != "ok" || got.Via != viaConfiguration {
		t.Fatalf("%+v", got)
	}
	want := []string{"service kubecron/kubecron", "secret kubecron/kubecron-api#api-token"}
	if !slices.Equal(h.consoleReads, want) {
		t.Errorf("console reads %v, want %v", h.consoleReads, want)
	}
	if len(h.revealed) != 0 {
		t.Errorf("revealed %v; a configured token is the console's to read", h.revealed)
	}
	for _, q := range h.listed {
		if q.GVR.Resource == "services" {
			t.Errorf("Services were listed (%+v) for a collector the configuration names", q)
		}
	}
	// What Configured names is exactly what was read.
	cfg := testExtra().Configured("docker-desktop", func(string) string { return "" })
	if len(cfg.Services) != 1 || cfg.Services[0].Namespace != "kubecron" || cfg.Services[0].Name != "kubecron" ||
		len(cfg.Secrets) != 1 || cfg.Secrets[0].Namespace != "kubecron" || cfg.Secrets[0].Name != "kubecron-api" {
		t.Errorf("Configured names %+v", cfg)
	}
	// Found by its label, nothing is configured: the namespace is the cluster's.
	unset := testExtra()
	unset.collector = collectorConfig{}
	if cfg := unset.Configured("docker-desktop", func(string) string { return "" }); len(cfg.Secrets)+len(cfg.Services) != 0 {
		t.Errorf("with nothing configured, Configured names %+v", cfg)
	}
	// The cluster's setting names it as the environment does.
	if cfg := unset.Configured("docker-desktop", func(key string) string {
		return map[string]string{SettingCollector: "ops/cron", SettingTokenSecret: "cron-token"}[key]
	}); len(cfg.Services) != 1 || cfg.Services[0].Name != "cron" || cfg.Secrets[0].Namespace != "ops" || cfg.Secrets[0].Name != "cron-token" {
		t.Errorf("a cluster's setting names %+v", cfg)
	}
}

// The token reaches no answer, on any route, whichever way it was read.
func TestTheCollectorsTokenIsInNoAnswer(t *testing.T) {
	for name, configured := range map[string]bool{"configured": true, "found by its label": false} {
		t.Run(name, func(t *testing.T) {
			h, _ := hostWithCollector(t)
			k := testExtra()
			if !configured {
				k.collector = collectorConfig{}
			}
			for _, route := range k.Routes(h) {
				path := strings.NewReplacer("{$}", "", "{namespace}", "ops", "{name}", "nightly",
					"{id}", "r1", "{day}", "2026-10-19").Replace(route.Pattern)
				rec := serve(t, route.Handler, "/x/docker-desktop/kubecron"+path, map[string]string{
					"namespace": "ops", "name": "nightly", "id": "r1", "day": "2026-10-19"})
				if strings.Contains(rec.Body.String(), testToken) {
					t.Errorf("%s answered the collector's token: %s", route.Pattern, rec.Body)
				}
			}
			if h.forwards == 0 {
				t.Error("no route reached the collector, so nothing was checked")
			}
		})
	}
}
