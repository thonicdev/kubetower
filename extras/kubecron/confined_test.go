package kubecron

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/extension"
)

// On a console several people share, the collector is where the environment
// names it and nowhere else: never looked for by its label, whatever the
// cluster holds.
func TestOnASharedConsoleNoCollectorIsLookedForByItsLabel(t *testing.T) {
	clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
	f := discover(context.Background(), viaClientset(clients), resolveShared(collectorConfig{}))
	if f.Ref != nil || f.State != "absent" {
		t.Fatalf("%+v", f)
	}
	if c := lastCheck(f.Checks); c.State != checkMissing || !strings.Contains(c.Detail, CollectorEnv) {
		t.Errorf("check %+v", c)
	}
	if secrets, listed := secretsRead(clients); listed || len(secrets) != 0 {
		t.Errorf("listed Services %v, read Secrets %v", listed, secrets)
	}
}

// The environment names a Service this cluster lacks: absent, and no label
// search behind it, as there is on the desktop.
func TestOnASharedConsoleAMissingNamedCollectorIsNotReplacedByALabelledOne(t *testing.T) {
	clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
	f := discover(context.Background(), viaClientset(clients), resolveShared(collectorConfig{Namespace: "ops", Service: "cron"}))
	if f.Ref != nil || f.State != "absent" {
		t.Fatalf("%+v", f)
	}
	if _, listed := secretsRead(clients); listed {
		t.Error("a Service was listed")
	}
}

// The environment names it: found, through the configuration.
func TestOnASharedConsoleTheNamedCollectorIsRead(t *testing.T) {
	clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
	f := discover(context.Background(), viaClientset(clients), resolveShared(testConfig))
	if f.Ref == nil || f.State != "ok" || f.Via != viaConfiguration {
		t.Fatalf("%+v", f)
	}
	if _, listed := secretsRead(clients); listed {
		t.Error("a Service was listed")
	}
}

// locate reads no cluster setting on a shared console: the setting cannot
// point the console's tunnel elsewhere, and no label search is made.
func TestLocateOnASharedConsoleIgnoresTheClustersSetting(t *testing.T) {
	h := &fakeHost{clients: fake.NewClientset(collectorService("bearer"), tokenSecret()),
		settings: map[string]string{SettingCollector: "kubecron/kubecron"}}
	r := extension.MarkShared(httptest.NewRequest("GET", "/", nil))
	f := KubeCron{}.locate(r, h, "prod")
	if f.Ref != nil || f.State != "absent" {
		t.Fatalf("%+v", f)
	}
	if len(h.listed) != 0 || len(h.consoleReads) != 0 {
		t.Errorf("listed %v, console reads %v", h.listed, h.consoleReads)
	}
	// The same request on the desktop reads the setting.
	desktop := KubeCron{}.locate(httptest.NewRequest("GET", "/", nil), h, "prod")
	if desktop.Ref == nil || desktop.Via != viaConfiguration {
		t.Errorf("on the desktop the setting was not read: %+v", desktop)
	}
}
