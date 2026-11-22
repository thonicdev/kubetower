package netinspect

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The two text settings: the address a flow UI is reached at when it is
// exposed by an Ingress. Read on every request through Host.Setting.
const (
	// SettingHubbleURL is the base address of Hubble UI (Cilium).
	SettingHubbleURL = "hubble-ui-url"
	// SettingWhiskerURL is the base address of Whisker (Calico).
	SettingWhiskerURL = "whisker-url"
)

// Where each UI's Service lives when the network plugin installs it.
const (
	hubbleUINamespace = "kube-system"
	hubbleUIService   = "hubble-ui"
	whiskerNamespace  = "calico-system"
	whiskerService    = "whisker"
)

// Settings is what the Settings page's Extra tab draws in this extra's tile
// (extension.Configurable).
func (NetInspect) Settings() []extension.Setting {
	return []extension.Setting{{
		Key:         SettingHubbleURL,
		Label:       "Hubble UI URL",
		Summary:     "Where Hubble UI is exposed. Set, the Hubble UI button opens this address instead of a tunnel.",
		Text:        true,
		Placeholder: "https://hubble.example.com",
		Check:       checkURL,
	}, {
		Key:         SettingWhiskerURL,
		Label:       "Whisker URL",
		Summary:     "Where Whisker is exposed. Set, the Whisker button opens this address instead of a tunnel.",
		Text:        true,
		Placeholder: "https://whisker.example.com",
		Check:       checkURL,
	}}
}

var _ extension.Configurable = NetInspect{}

// checkURL accepts an empty value (not set) or an http or https address with a
// host and no user information.
func checkURL(value string) error {
	_, err := cleanURL(value)
	return err
}

// cleanURL returns the address without a trailing slash, query or fragment.
func cleanURL(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", errors.New("this is not a valid address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("the address must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return "", errors.New("the address needs a host")
	}
	if u.User != nil {
		return "", errors.New("the address must not carry a user name or password")
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// UITunnel is the Service a UI is served by, for the console's own port-forward.
type UITunnel struct {
	Namespace   string `json:"namespace"`
	Service     string `json:"service"`
	Port        int    `json:"port"`
	PortName    string `json:"portName,omitempty"`
	AppProtocol string `json:"appProtocol,omitempty"`
}

// UI is how one flow UI can be opened: at a configured address, or through a
// tunnel to its Service. A UI that is neither is absent from the answer.
type UI struct {
	URL    string    `json:"url,omitempty"`
	Tunnel *UITunnel `json:"tunnel,omitempty"`
}

// UIs is `GET /uis`: the flow UIs this cluster can open.
type UIs struct {
	Hubble  *UI `json:"hubble,omitempty"`
	Whisker *UI `json:"whisker,omitempty"`
}

// uisHandler is `GET /uis`. The Services are read for the person; one that is
// refused or fails to read is no button and no error, as is one that is not
// there. A configured address wins and is offered without reading anything.
func uisHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		out := UIs{}
		var gone bool
		out.Hubble, gone = findUI(r, h, cluster, SettingHubbleURL, hubbleUINamespace, hubbleUIService)
		if !gone {
			out.Whisker, gone = findUI(r, h, cluster, SettingWhiskerURL, whiskerNamespace, whiskerService)
		}
		if gone {
			writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), nil)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// findUI answers nil for a UI that cannot be opened. The bool is true only for
// an unknown cluster.
func findUI(r *http.Request, h extension.Host, cluster, setting, namespace, service string) (*UI, bool) {
	// A stored value that no longer passes is left out rather than linked.
	if u, _ := cleanURL(h.Setting(r, cluster, setting)); u != "" {
		return &UI{URL: u}, false
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, Namespace: namespace,
		FieldSelector: "metadata.name=" + service, Limit: 1})
	if err != nil {
		return nil, errors.Is(err, extension.ErrUnknownCluster)
	}
	if len(page.Items) == 0 {
		return nil, false
	}
	var svc corev1.Service
	if convert(&page.Items[0], &svc) != nil {
		return nil, false
	}
	port, ok := uiPort(svc.Spec.Ports)
	if !ok {
		return nil, false
	}
	return &UI{Tunnel: &UITunnel{Namespace: namespace, Service: service, Port: int(port.Port),
		PortName: port.Name, AppProtocol: stringOf(port.AppProtocol)}}, false
}

// uiPort is the port a browser should be sent to: the one named http, else the
// first TCP one.
func uiPort(ports []corev1.ServicePort) (corev1.ServicePort, bool) {
	var first *corev1.ServicePort
	for i := range ports {
		p := ports[i]
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			continue
		}
		if p.Name == "http" {
			return p, true
		}
		if first == nil {
			first = &ports[i]
		}
	}
	if first == nil {
		return corev1.ServicePort{}, false
	}
	return *first, true
}

func stringOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
