package netinspect

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

func uiService(namespace, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.ServiceSpec{Ports: ports}}
}

func askUIs(t *testing.T, h *fakeHost) UIs {
	t.Helper()
	w := serve(t, h, "/x/kind/netinspect/uis")
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	return decode[UIs](t, w)
}

func TestUIsAbsentWhereNeitherIsInstalled(t *testing.T) {
	got := askUIs(t, newHost())
	if got.Hubble != nil || got.Whisker != nil {
		t.Errorf("got %+v, want neither", got)
	}
}

func TestUIsFoundThroughTheirServices(t *testing.T) {
	h := newHost()
	h.add(t, "services", uiService("kube-system", "hubble-ui", corev1.ServicePort{Name: "http", Port: 80}))
	h.add(t, "services", uiService("calico-system", "whisker", corev1.ServicePort{Port: 8081}))
	got := askUIs(t, h)
	if got.Hubble == nil || got.Hubble.URL != "" || got.Hubble.Tunnel == nil ||
		*got.Hubble.Tunnel != (UITunnel{Namespace: "kube-system", Service: "hubble-ui", Port: 80, PortName: "http"}) {
		t.Errorf("hubble %+v", got.Hubble)
	}
	if got.Whisker == nil || got.Whisker.Tunnel == nil ||
		*got.Whisker.Tunnel != (UITunnel{Namespace: "calico-system", Service: "whisker", Port: 8081}) {
		t.Errorf("whisker %+v", got.Whisker)
	}
}

// Each Service is read for the person, in its own namespace and by name.
func TestUIServicesAreReadForThePerson(t *testing.T) {
	h := newHost()
	askUIs(t, h)
	seen := map[string]string{}
	for _, q := range h.queries {
		seen[q.Namespace] = q.FieldSelector
	}
	if seen["kube-system"] != "metadata.name=hubble-ui" || seen["calico-system"] != "metadata.name=whisker" {
		t.Errorf("queries %+v", seen)
	}
}

// A Service that cannot be read - refused, or the read failed - is no button
// and no error.
func TestUIsUnreadableAreSilent(t *testing.T) {
	for name, err := range map[string]error{
		"refused": extension.ErrDenied,
		"failed":  errors.New("connection refused"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHost()
			h.add(t, "services", uiService("kube-system", "hubble-ui", corev1.ServicePort{Port: 80}))
			h.listErr["services"] = err
			got := askUIs(t, h)
			if got.Hubble != nil || got.Whisker != nil {
				t.Errorf("got %+v, want neither", got)
			}
		})
	}
}

func TestUIsUnknownCluster(t *testing.T) {
	h := newHost()
	h.listErr["services"] = extension.ErrUnknownCluster
	if w := serve(t, h, "/x/kind/netinspect/uis"); w.Code != http.StatusNotFound {
		t.Errorf("answered %d, want 404", w.Code)
	}
}

// A configured address wins, and is offered even where the Service is not
// there and without reading it.
func TestConfiguredUIWins(t *testing.T) {
	h := newHost()
	h.add(t, "services", uiService("kube-system", "hubble-ui", corev1.ServicePort{Port: 80}))
	h.settings = map[string]string{SettingHubbleURL: " https://hubble.example.com/ui/?x=1#y ", SettingWhiskerURL: "http://w.example.com"}
	got := askUIs(t, h)
	if got.Hubble == nil || got.Hubble.URL != "https://hubble.example.com/ui" || got.Hubble.Tunnel != nil {
		t.Errorf("hubble %+v", got.Hubble)
	}
	if got.Whisker == nil || got.Whisker.URL != "http://w.example.com" || got.Whisker.Tunnel != nil {
		t.Errorf("whisker %+v", got.Whisker)
	}
	if len(h.queries) != 0 {
		t.Errorf("read %d listings, want none", len(h.queries))
	}
}

// A stored value that does not pass is left out: the Service is looked for.
func TestInvalidConfiguredUIFallsBackToTheService(t *testing.T) {
	h := newHost()
	h.add(t, "services", uiService("calico-system", "whisker", corev1.ServicePort{Port: 8081}))
	h.settings = map[string]string{SettingWhiskerURL: "javascript:alert(1)"}
	got := askUIs(t, h)
	if got.Whisker == nil || got.Whisker.URL != "" || got.Whisker.Tunnel == nil {
		t.Errorf("whisker %+v", got.Whisker)
	}
}

func TestUIPort(t *testing.T) {
	udp := corev1.ServicePort{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP}
	web := corev1.ServicePort{Name: "http", Port: 80}
	other := corev1.ServicePort{Name: "x", Port: 9000}
	if p, ok := uiPort([]corev1.ServicePort{udp, other, web}); !ok || p.Port != 80 {
		t.Errorf("named http: %+v %v", p, ok)
	}
	if p, ok := uiPort([]corev1.ServicePort{udp, other}); !ok || p.Port != 9000 {
		t.Errorf("first TCP: %+v %v", p, ok)
	}
	if _, ok := uiPort([]corev1.ServicePort{udp}); ok {
		t.Error("UDP only must be none")
	}
	if _, ok := uiPort(nil); ok {
		t.Error("no port must be none")
	}
}

func TestSettingsCheckURL(t *testing.T) {
	var keys []string
	for _, s := range New().Settings() {
		keys = append(keys, s.Key)
		for value, ok := range map[string]bool{"": true, "https://a.example.com/x": true, "ftp://a": false,
			"javascript:alert(1)": false, "https://u:p@a.example.com": false, "https://": false, "/relative": false} {
			if err := s.Check(value); (err == nil) != ok {
				t.Errorf("%s %q: %v", s.Key, value, err)
			}
		}
	}
	if strings.Join(keys, ",") != "hubble-ui-url,whisker-url" {
		t.Errorf("keys %v", keys)
	}
}
