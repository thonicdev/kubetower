package argocd

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/thonicdev/kubetower/extension"
)

// SettingListColumn is the key of the setting that decides whether the core's
// lists draw its Argo CD column. The front end's `LIST_COLUMN` in
// `web/app/extras/argocd/extension.tsx` is the same string,
// and it is stored per cluster under it, so it is not free to change.
const SettingListColumn = "list-column"

// The three text settings. Read on every request through Host.Setting, so a
// change applies with no restart; the front end reads them through `/settings`.
const (
	// SettingTool says which tools the page looks for: auto, argocd, flux or both.
	SettingTool = "tool"
	// SettingArgoURL is the base address of the Argo CD UI.
	SettingArgoURL = "argocd-url"
	// SettingFluxURL is the base address of the Flux (Weave GitOps) UI.
	SettingFluxURL = "flux-url"
)

// The values of SettingTool. Auto is also what an empty setting means.
const (
	ToolAuto   = "auto"
	ToolArgoCD = "argocd"
	ToolFlux   = "flux"
	ToolBoth   = "both"
)

// Settings is what the Settings page's Extra tab draws in this extra's tile
// (extension.Configurable).
//
// The column is the browser's to draw, so nothing here reads that value back.
// It is off by default, a stored choice wins over the default, and
// `KT_EXTRA_SETTINGS=argocd.list-column=on` pins it on every cluster.
func (Delivery) Settings() []extension.Setting {
	return []extension.Setting{{
		Key:     SettingListColumn,
		Label:   "Show the Argo CD column in the lists",
		Summary: "On, the console's lists draw a Synced or OutOfSync column; the Argo CD tab on an object is there either way.",
		Default: false,
	}, {
		Key:         SettingTool,
		Label:       "Tool",
		Summary:     "Auto-detect draws what is installed.",
		Text:        true,
		Placeholder: "Auto-detect",
		Choices: []extension.Choice{
			{Value: ToolAuto, Label: "Auto-detect"},
			{Value: ToolArgoCD, Label: "Argo CD"},
			{Value: ToolFlux, Label: "Flux"},
			{Value: ToolBoth, Label: "Both"},
		},
	}, {
		Key:         SettingArgoURL,
		Label:       "Argo CD URL",
		Summary:     "Where Argo CD's own UI is. Set, each Application gets an Open in Argo CD link.",
		Text:        true,
		Placeholder: "https://argocd.example.com",
		Check:       checkURL,
	}, {
		Key:         SettingFluxURL,
		Label:       "Flux (Weave GitOps) URL",
		Summary:     "Where the Weave GitOps UI is. Set, each Kustomization and HelmRelease gets an Open in Flux link.",
		Text:        true,
		Placeholder: "https://gitops.example.com",
		Check:       checkURL,
	}}
}

// toolOf is the tool a setting names; anything unknown reads as auto.
func toolOf(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case ToolArgoCD, ToolFlux, ToolBoth:
		return v
	}
	return ToolAuto
}

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

// SettingsAnswer is what `/settings` sends: the page's own configuration.
type SettingsAnswer struct {
	Tool    string `json:"tool"`
	ArgoURL string `json:"argoUrl,omitempty"`
	FluxURL string `json:"fluxUrl,omitempty"`
}

func (d Delivery) settings(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		out := SettingsAnswer{Tool: toolOf(h.Setting(r, cluster, SettingTool))}
		// A stored value that no longer passes is left out rather than linked.
		out.ArgoURL, _ = cleanURL(h.Setting(r, cluster, SettingArgoURL))
		out.FluxURL, _ = cleanURL(h.Setting(r, cluster, SettingFluxURL))
		writeJSON(w, http.StatusOK, out)
	})
}

// Asserted at compile time, so a signature that drifts from the contract fails
// this package's build.
var _ extension.Configurable = Delivery{}
