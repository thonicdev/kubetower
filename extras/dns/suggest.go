package dns

import (
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Suggest is whether the cluster runs CoreDNS with its metrics in a
// Prometheus, for the banner that offers to turn the page on
// (extension.Suggester). It asks the door for the shortest span: the core
// holds that read and shares it with the page, so the banner costs one read a
// short while, and a person the door refuses is suggested nothing.
func (*DNS) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	got, err := h.DNSWindow(r, cluster, extension.DNSSpan15m)
	if err != nil {
		return "", err
	}
	if got.State != extension.WindowRead {
		return "", nil
	}
	return fmt.Sprintf("CoreDNS runs here with %d pods and its metrics are in Prometheus.", len(got.Pods)), nil
}
