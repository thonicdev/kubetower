package extension

import "errors"

// Configured is an Extension that talks, on the person's behalf, to a backend
// the console's configuration names - Argo CD's server, the KubeCron collector -
// with a credential that configuration names too.
//
// **Optional, and an interface of its own** for Configurable's reason: an
// extension that reads nothing with the console's credentials compiles
// unchanged.
//
// It is the list Host.ConfiguredSecret and Host.ConfiguredService hold every
// request to: those two read with the console's own credentials, and only an
// object this answer names. So what it names must come **from the configuration
// alone** - the environment, the cluster's own settings (setting, which is
// Host.Setting for that cluster) and the extension's defaults - and **never
// from anything read from the cluster**: a name found in a label, an
// annotation or a list is written by whoever may write that object, and the
// console would then read, with its own rights, whatever they pointed it at.
type Configured interface {
	Extension
	// Configured names the Secrets and Services this extension may read with
	// the console's credentials on cluster. Asked on every read through those
	// two doors, so a setting changed on the Settings page applies to the next
	// request. An empty answer refuses every such read.
	Configured(cluster string, setting func(key string) string) Configuration
}

// Configuration is what Configured names: Secrets and Services, each one
// namespace and one name.
type Configuration struct {
	Secrets  []ObjectName
	Services []ObjectName
}

// ObjectName is one object of a namespaced kind, by namespace and name.
type ObjectName struct {
	Namespace, Name string
}

// ErrNotConfigured is a read through ConfiguredSecret or ConfiguredService of an
// object the extension's Configured does not name on that cluster - or by an
// extension that does not implement Configured. Nothing was read. Ask with
// errors.Is: it is a defect in the page or a misconfiguration, never the
// cluster's answer.
var ErrNotConfigured = errors.New("not named by this console's configuration")

// ErrNoSuchService is ConfiguredService's answer for a Service the
// configuration names and the cluster does not hold.
var ErrNoSuchService = errors.New("no such Service")
