package kubecron

import (
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/extension"
)

// Suggest is whether the cluster runs a KubeCron collector, for the banner that
// offers to turn the extra on (extension.Suggester).
//
// It finds the collector as the page does - the Service the configuration
// names, else the one labeled collectorLabel (byLabel, unchanged) - and on a
// console several people share only where the environment names it - but stops
// there: no token is read and nothing is dialled. Absent says nothing; a
// lookup that was refused or failed, or a configuration that is invalid, is an
// error, never "absent".
func (k KubeCron) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	w := resolveShared(k.collector)
	if !extension.SharedConsole(r) {
		w = resolve(k.collector, h.Setting(r, cluster, SettingCollector), h.Setting(r, cluster, SettingTokenSecret))
	}
	if w.Invalid != "" {
		return "", errors.New(w.Invalid)
	}
	rd := hostReads{r: r, h: h, cluster: cluster}
	ctx := r.Context()
	if w.Service != "" {
		_, err := rd.service(ctx, w.Namespace, w.Service)
		switch {
		case err == nil:
			return "A KubeCron collector is installed", nil
		case errors.Is(err, extension.ErrNoSuchService) || apierrors.IsNotFound(err):
			if w.CollectorFrom != fromEnvironment || w.Confined {
				return "", nil
			}
			// One variable for every cluster: where the Service it names is
			// not, the label is still looked for, as the page does.
		default:
			return "", err
		}
	}
	if w.Confined {
		// A console several people share looks only where the deployment
		// names the collector, never by its label.
		return "", nil
	}
	svc, f := byLabel(ctx, rd, found{}, settingsAdvice)
	switch {
	case svc != nil:
		return "A KubeCron collector is installed", nil
	case f.State == "absent":
		return "", nil
	}
	return "", errors.New(f.Detail)
}

var _ extension.Suggester = KubeCron{}

// Component names what Suggest looks for (extension.Component).
func (KubeCron) Component() string { return "KubeCron collector" }

var _ extension.Component = KubeCron{}
