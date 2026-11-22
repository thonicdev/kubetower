package extras

import (
	"fmt"
	"sort"
	"strings"
)

// EnvExtraSettings is the deployment's pin list for the extras' own settings
// (extension.Configurable), as KT_EXTRAS is for the extras themselves.
//
// # Why it exists
//
// An extra's setting - Argo CD's column in the lists, say - is off by default
// everywhere, and must still be possible to turn on in a cluster. Under the
// server profile the per-cluster setting write is not registered, so the
// deployment needs a knob of its own: this variable, rendered by the chart.
//
// # Generic rather than one variable per setting
//
// `KT_EXTRA_SETTINGS=argocd.list-column=on`, and not `KT_ARGOCD_LIST_COLUMN`:
// every extra may declare settings, and a variable per setting is a chart value
// and a documentation row per setting forever. The grammar is KT_EXTRAS's -
// a comma-separated list, the same on/off spellings, case and spaces ignored -
// with each item naming `<extra>.<setting>`. Neither an extra's key nor a
// setting's (both are KeyPattern, keypattern.go) can hold a dot, so the
// first dot is unambiguous.
//
// # It pins, as KT_EXTRAS pins
//
// A setting named here has that value on **every cluster, on every profile**,
// and the per-cluster write refuses it (ErrSettingPinned) with a sentence
// naming this variable; the listing says `pinned` so the Settings page draws it
// with no control. What a person chose before keeps its place in extras.json
// and is honored again once the variable stops naming it - the same bargain as
// a pinned extra.
//
// Read once, at start-up: restart after changing it.
const EnvExtraSettings = "KT_EXTRA_SETTINGS"

// ParseSettingsEnv reads KT_EXTRA_SETTINGS: what it pins, extra -> setting ->
// value, and the complaints, separately - logged by the caller, never a refusal
// to start, for the reason ParseEnv gives.
//
// Every item needs a value: unlike an extra, a setting has no "on" that a bare
// name could obviously mean, and a deployment that wrote `argocd.list-column`
// meaning "the default" would get the opposite of what it read.
func ParseSettingsEnv(value string) (map[string]map[string]bool, []string) {
	pinned := map[string]map[string]bool{}
	var complaints []string

	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, raw, hasValue := strings.Cut(item, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		key, settingKey, hasDot := strings.Cut(name, ".")
		key = Canonical(strings.TrimSpace(key))
		settingKey = strings.TrimSpace(settingKey)

		if !hasDot || key == "" || settingKey == "" {
			complaints = append(complaints, fmt.Sprintf("%s names %q, which is not <extra>.<setting>; "+
				"write for example argocd.list-column=on", EnvExtraSettings, name))
			continue
		}
		if !Known(key) {
			if keys := Keys(); len(keys) == 0 {
				complaints = append(complaints, fmt.Sprintf("%s names %q, and this build of "+
					"KubeTower carries no extra pages at all", EnvExtraSettings, key))
			} else {
				complaints = append(complaints, fmt.Sprintf("%s names %q, which is not an extra; "+
					"the %d are %s", EnvExtraSettings, key, len(keys), strings.Join(keys, ", ")))
			}
			continue
		}
		declared, ok := setting(key, settingKey)
		if !ok {
			complaints = append(complaints, fmt.Sprintf("%s names %s.%s, which %s does not offer; %s",
				EnvExtraSettings, key, settingKey, key, offered(key)))
			continue
		}
		if declared.Text {
			// A line of text is not on or off: its own variable sets it.
			complaints = append(complaints, fmt.Sprintf("%s names %s.%s, which is a line of text and not on or off, "+
				"so it pins nothing; %s", EnvExtraSettings, key, settingKey, textEnvHint(declared)))
			continue
		}
		if !hasValue {
			complaints = append(complaints, fmt.Sprintf("%s names %s.%s with no value, so it pins nothing; "+
				"write %s.%s=on or %s.%s=off", EnvExtraSettings, key, settingKey, key, settingKey, key, settingKey))
			continue
		}
		on, ok := parseBool(raw)
		if !ok {
			complaints = append(complaints, fmt.Sprintf("%s sets %s.%s=%q, which is neither on nor off, "+
				"so it pins nothing and the per-cluster choice decides. Write %s.%s=on or %s.%s=off "+
				"in %s %s and restart KubeTower", EnvExtraSettings, key, settingKey, strings.TrimSpace(raw),
				key, settingKey, key, settingKey, EnvExtraSettings, WhereSet))
			continue
		}
		if pinned[key] == nil {
			pinned[key] = map[string]bool{}
		}
		pinned[key][settingKey] = on
	}
	return pinned, complaints
}

// textEnvHint says how a text setting is set for every cluster, for a complaint.
func textEnvHint(declared Setting) string {
	if declared.Env == "" {
		return "it is set per cluster on the Settings page"
	}
	return "set " + declared.Env + " instead"
}

// offered is "it offers a, b" or "it offers no settings", for a complaint.
func offered(key string) string {
	e, _ := lookup(key)
	names := make([]string, 0, len(e.Settings))
	for _, one := range e.Settings {
		names = append(names, one.Key)
	}
	if len(names) == 0 {
		return "it offers no settings"
	}
	return "it offers " + strings.Join(names, ", ")
}

// SettingPinned reports whether KT_EXTRA_SETTINGS decides this setting, and so
// whether the Settings page must draw it with no control.
func (s *Store) SettingPinned(key, name string) bool {
	_, ok := s.pinnedSettings[key][name]
	return ok
}

// PinnedSettings is what KT_EXTRA_SETTINGS pinned, as `extra.setting=on|off`
// in a stable order, for the start-up log. Empty when it pins nothing.
func (s *Store) PinnedSettings() []string {
	out := []string{}
	for key, settings := range s.pinnedSettings {
		for name, on := range settings {
			out = append(out, fmt.Sprintf("%s.%s=%s", key, name, map[bool]string{true: "on", false: "off"}[on]))
		}
	}
	sort.Strings(out)
	return out
}

// ErrSettingPinned is what SetSetting answers for a setting the deployment
// decided. A refusal of the request, as ErrPinned is.
var ErrSettingPinned = fmt.Errorf("this setting is set by %s %s", EnvExtraSettings, WhereSet)
