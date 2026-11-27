package web

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// A build with no port forwarder answers a pod tunnel with that reason, not
// a nil tunnel or a panic: a host whose server holds no tunneller, and one
// with no server behind it at all.
func TestAPodTunnelWithNoPortForwarderSaysSo(t *testing.T) {
	s, err := New(Options{Clusters: navFixture(t, "prod").clusters, InCluster: true})
	if err != nil {
		t.Fatal(err)
	}
	pods, ok := s.hostFor("runtime").(extension.PodForwarder)
	if !ok {
		t.Fatal("the Host handed to an extension opens no tunnel to a pod")
	}
	for name, h := range map[string]extension.PodForwarder{"no tunneller": pods, "no server": host{ext: "runtime"}} {
		tunnel, err := h.ForwardPod(podRequest(grace()), "prod", "shop", "api-0", 6060)
		if err == nil || !strings.Contains(err.Error(), "no port forwarder") || tunnel.Stop != nil || tunnel.Addr != "" {
			t.Errorf("%s: tunnel %+v, err %v", name, tunnel, err)
		}
	}
}
