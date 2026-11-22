package kubecron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
)

// Reading a KubeCron collector: the rung that has what the cluster forgot.
//
// A CronJob's Jobs are garbage-collected minutes after they finish, taking
// their duration, exit code and logs with them. A collector
// (github.com/schmitech-fr/kubecron, run with `mode=server`) watches those Jobs
// while they exist and keeps what it saw, so a console that was not running at
// the time can still ask. Its contract is that project's
// docs/COLLECTOR-API.md; this file reads `/api/v1` and nothing unversioned.
//
// # Found with nothing configured
//
// The collector's chart labels and annotates its Service, and writes its token
// into a Secret named after the release:
//
//	kubecron.io/collector: "true"        (label)
//	kubecron.io/api-path: /api/v1
//	kubecron.io/auth: bearer | none
//	Secret <release>-api, key api-token  (kubecron-api for the release kubecron)
//
// So turning the extra on is enough: the collector is **the one Service
// labeled as one**, and its token is the key `api-token` of the Secret
// **`kubecron-api` in that Service's namespace**. Each of the three can be
// named instead, and the first answer wins:
//
//  1. the environment - KT_KUBECRON_COLLECTOR, KT_KUBECRON_TOKEN_SECRET,
//     KT_KUBECRON_TOKEN_KEY - the deployment's, one value for every cluster;
//  2. the cluster's own setting, edited on the Settings page (Settings, read
//     through Host.Setting on every request, so a change needs no restart);
//  3. the defaults above.
//
// **On a console several people share** (the server profile,
// extension.SharedConsole) only the first: the collector is where the
// environment - the chart's values - names it, and nowhere else. It is not
// looked for by its label, and a Service the environment names that the
// cluster lacks is absent, not a reason to look further.
//
// The page and the Settings tile say which of the three each value came from.
//
// # Where a token goes
//
// **A token read in namespace N is sent only to a Service of namespace N**
//. The Secret is read in the collector's own namespace and in no
// other - there is no setting for its namespace - and the header is added only
// when the two match, which openCollector and forwardGetter both check, so a
// later change to how the Secret is found cannot send it elsewhere. The
// Service's annotations never choose a Secret: an
// annotation would let whoever may annotate a Service pick which Secret the
// console reads.
//
// **Several labeled Services: none is chosen.** The page names them and asks
// for one to be set. Taking the first would let any tenant who may create a
// labeled Service in their own namespace stand in for the collector.
// With exactly one, a tenant's Service is read only where it is the only
// collector in the cluster, and then with its own namespace's Secret, which
// it could read anyway; the page says it was found by its label.
//
// # Whose rights read what
//
//   - **A collector the configuration names** (the environment, or the
//     cluster's setting) is read with the console's own credentials, under the
//     server profile its ServiceAccount's, through Host.ConfiguredService; its
//     token, the Secret the configuration names in that Service's namespace,
//     through Host.ConfiguredSecret, which records the read. Both are held by
//     the core to what Configured below names, and never to a name read from
//     the cluster.
//   - **A collector found by its label** is listed through Host.List, for the
//     person: one who may not list Services where it runs is not told it
//     exists. Its namespace then comes from the cluster, so its token is not a
//     configured Secret: it is read through Host.Reveal, reviewed for the
//     person as `get secrets/<name>` and recorded, or not at all.
//
// The token never leaves this process: it is a header on a request this extra
// makes, never a field of an answer. What the person is shown out of the
// collector is joined onto the CronJobs they may list (scheduled.go), which is
// the review that applies to them.
//
// # Through a port-forward, never the API server's proxy
//
// The API server authenticates the request it proxies and then drops its
// Authorization header, so a bearer token for the collector cannot travel
// through `services/proxy`. So it is reached through a port-forward instead,
// `Host.Forward`.
//
// # Every failure is a sentence, never an empty column
//
// Each step is a check the page draws: the collector (found, not found,
// several, refused), the Secret (found, not found, refused with the right that
// is missing), the collector reached or not, its token accepted or refused. No
// collector is the ordinary case and not an error.

// collectorLabel is what a collector's Service is marked with.
const collectorLabel = "kubecron.io/collector=true"

const (
	annAPIPath = "kubecron.io/api-path"
	annAuth    = "kubecron.io/auth"
)

// CollectorEnv names the collector's Service, as `namespace/service`.
const CollectorEnv = "KT_KUBECRON_COLLECTOR"

// TokenSecretEnv names the Secret holding the collector's token, in the
// collector's namespace.
const TokenSecretEnv = "KT_KUBECRON_TOKEN_SECRET"

// TokenKeyEnv names the key of that Secret holding the token; TokenKey unset.
const TokenKeyEnv = "KT_KUBECRON_TOKEN_KEY"

// TokenKey is the key holding the token as the chart writes it.
const TokenKey = "api-token"

// DefaultTokenSecret is the Secret the chart writes the token into for the
// release `kubecron` (`<release>-api`), read where nothing names another.
const DefaultTokenSecret = "kubecron-api"

// SettingCollector and SettingTokenSecret are the per-cluster settings the
// Settings page edits (kubecron.go, Settings), stored under these keys.
const (
	SettingCollector   = "collector"
	SettingTokenSecret = "token-secret"
)

// Where a value came from, in a check's Source.
const (
	fromEnvironment = "environment"
	fromSetting     = "setting"
	fromDefault     = "default"
)

// collectorConfig is what the environment says about the collector.
type collectorConfig struct {
	// Namespace and Service are the collector's Service, empty when unset.
	Namespace, Service string
	// Secret is the token's Secret in the collector's namespace, empty when
	// unset; Key is its key, empty for TokenKey.
	Secret, Key string
	// Invalid is the sentence for a value that cannot be used, said on every
	// request rather than read as unset: an operator who set it meant it.
	Invalid string
}

// where is the configured Service as `namespace/service`.
func (c collectorConfig) where() string { return c.Namespace + "/" + c.Service }

// key is the Secret's key holding the token.
func (c collectorConfig) key() string {
	if c.Key == "" {
		return TokenKey
	}
	return c.Key
}

// parseCollector reads `namespace/service`, or says why it is not one.
func parseCollector(value string) (namespace, service string, err error) {
	ns, svc, ok := strings.Cut(value, "/")
	ns, svc = strings.TrimSpace(ns), strings.TrimSpace(svc)
	if !ok || ns == "" || svc == "" || strings.Contains(svc, "/") {
		return "", "", fmt.Errorf("%q is not <namespace>/<service>", value)
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return "", "", fmt.Errorf("%q names the namespace %q, which is not a Kubernetes name: %s", value, ns, errs[0])
	}
	if errs := validation.IsDNS1035Label(svc); len(errs) > 0 {
		return "", "", fmt.Errorf("%q names the Service %q, which is not a Kubernetes name: %s", value, svc, errs[0])
	}
	return ns, svc, nil
}

// checkSecretName refuses what cannot be a Secret's name alone.
func checkSecretName(value string) error {
	if strings.Contains(value, "/") {
		return fmt.Errorf("%q is not a Secret's name alone: it is read in the collector's own namespace", value)
	}
	if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
		return fmt.Errorf("%q is not a Kubernetes name: %s", value, errs[0])
	}
	return nil
}

// checkCollector is the Settings page's check on the collector it is given.
func checkCollector(value string) error {
	_, _, err := parseCollector(value)
	return err
}

// readConfig reads CollectorEnv, TokenSecretEnv and TokenKeyEnv. Each may be
// set alone: what is not set comes from the cluster's setting or the default.
func readConfig(getenv func(string) string) collectorConfig {
	collector := strings.TrimSpace(getenv(CollectorEnv))
	secret := strings.TrimSpace(getenv(TokenSecretEnv))
	key := strings.TrimSpace(getenv(TokenKeyEnv))
	invalid := func(format string, args ...any) collectorConfig {
		return collectorConfig{Invalid: fmt.Sprintf(format, args...) + ", so no collector is read"}
	}
	var cfg collectorConfig
	if collector != "" {
		ns, svc, err := parseCollector(collector)
		if err != nil {
			return invalid("%s=%v", CollectorEnv, err)
		}
		cfg.Namespace, cfg.Service = ns, svc
	}
	if secret != "" {
		if err := checkSecretName(secret); err != nil {
			return invalid("%s=%v", TokenSecretEnv, err)
		}
		cfg.Secret = secret
	}
	if key != "" {
		if errs := validation.IsConfigMapKey(key); len(errs) > 0 {
			return invalid("%s=%q is not a Secret's key: %s", TokenKeyEnv, key, errs[0])
		}
		cfg.Key = key
	}
	return cfg
}

// wanted is what to look for on one cluster, each value with where it came
// from: the environment, the cluster's setting, or the default.
type wanted struct {
	// Namespace and Service name the collector; Service empty is "the one
	// labeled as one".
	Namespace, Service, CollectorFrom string
	// Secret is the token's Secret, read in the collector's namespace.
	Secret, SecretFrom string
	Key                string
	// Invalid is a value that cannot be used, said instead of reading.
	Invalid string
	// Confined is a console several people share (extension.SharedConsole):
	// the collector is only where the environment, the chart's values, names
	// it, and never looked for by its label.
	Confined bool
}

func (w wanted) where() string { return w.Namespace + "/" + w.Service }

// resolveShared is resolve on a console several people share: the
// environment only. The cluster's setting is not read, and a collector the
// environment does not name is not looked for: by its label, any Service a
// tenant may label would be dug to with the console's own credentials.
func resolveShared(env collectorConfig) wanted {
	w := resolve(env, "", "")
	w.Confined = true
	return w
}

// resolve is what to look for on one cluster: the environment, else the
// cluster's settings (collector and secret as stored, possibly empty), else
// the defaults.
func resolve(env collectorConfig, collector, secret string) wanted {
	if env.Invalid != "" {
		return wanted{Invalid: env.Invalid}
	}
	w := wanted{CollectorFrom: fromDefault, Secret: DefaultTokenSecret, SecretFrom: fromDefault, Key: env.key()}
	switch {
	case env.Service != "":
		w.Namespace, w.Service, w.CollectorFrom = env.Namespace, env.Service, fromEnvironment
	case strings.TrimSpace(collector) != "":
		ns, svc, err := parseCollector(strings.TrimSpace(collector))
		if err != nil {
			// The store checks a value before keeping it; one that reaches
			// here was edited by hand.
			return wanted{Invalid: fmt.Sprintf("the collector set for this cluster, %v, so no collector is read", err)}
		}
		w.Namespace, w.Service, w.CollectorFrom = ns, svc, fromSetting
	}
	switch {
	case env.Secret != "":
		w.Secret, w.SecretFrom = env.Secret, fromEnvironment
	case strings.TrimSpace(secret) != "":
		if err := checkSecretName(strings.TrimSpace(secret)); err != nil {
			return wanted{Invalid: fmt.Sprintf("the token's Secret set for this cluster, %v, so no collector is read", err)}
		}
		w.Secret, w.SecretFrom = strings.TrimSpace(secret), fromSetting
	}
	return w
}

// collectorTimeout bounds one read. A collector answers from a local SQLite
// file, so a slow one is a broken one and the page must not hang on it.
const collectorTimeout = 10 * time.Second

// supportedAPIVersion is the contract this build speaks.
const supportedAPIVersion = "v1"

// CollectorRef is a collector that was found, and how to talk to it.
type CollectorRef struct {
	Namespace string
	Service   string
	Port      int
	// APIPath is the contract's prefix, from the Service's own annotation.
	APIPath string
	// Token is the bearer credential, read from the Secret. Empty when none
	// was read.
	Token string
	// TokenNamespace and TokenSecret are where Token was read: it is sent only
	// when TokenNamespace is Namespace, and the Secret is named when
	// the collector refuses it.
	TokenNamespace, TokenSecret string
}

// Where names the collector in a sentence.
func (r CollectorRef) Where() string { return r.Namespace + "/" + r.Service }

// tokenElsewhere says why a token is not sent, or "" when it may be.
func (r CollectorRef) tokenElsewhere() string {
	if r.Token == "" || r.TokenNamespace == r.Namespace {
		return ""
	}
	return fmt.Sprintf("a token read in namespace %q is never sent to the Service %s, in namespace %q",
		r.TokenNamespace, r.Where(), r.Namespace)
}

// getter reads a path under the collector. A function so this file does not
// know how the collector is reached, and a test can answer for it.
//
// **The query is a parameter of its own and never part of path.** A run cursor
// carries a `+` in its UTC offset; concatenated into a URL it arrives as a
// space, the collector fails to parse it and silently re-serves page one.
// url.Values escapes it to %2B.
type getter func(ctx context.Context, path string, query url.Values, headers map[string]string) ([]byte, error)

// How a collector was reached, in collectorState.Via.
const (
	viaConfiguration = "configuration"
	viaLabel         = "label"
)

// The steps a check is about, and what each can say.
const (
	stepCollector = "collector"
	stepSecret    = "secret"
	stepReach     = "reach"
	stepToken     = "token"

	checkOK      = "ok"
	checkMissing = "missing"
	checkSeveral = "several"
	checkDenied  = "denied"
	checkFailed  = "failed"
	checkRefused = "refused"
	checkSkipped = "skipped"
)

// check is one step of finding and reading the collector, as the page and the
// Settings tile draw it: a line each.
type check struct {
	Step   string `json:"step"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	// Value is what was looked for - a Service or a Secret as namespace/name -
	// and Source where it came from: environment, setting or default.
	Value  string `json:"value,omitempty"`
	Source string `json:"source,omitempty"`
}

// found is what discover concluded for one cluster: the page's standing, and
// the collector to read when there is one.
type found struct {
	collectorState
	// Ref is set when the collector can be read, and State is then "ok".
	Ref *CollectorRef
}

// failed is a found with no collector to read.
func failed(state, detail string) found {
	return found{collectorState: collectorState{State: state, Detail: detail}}
}

// add records a check, and makes its sentence the page's when it stops the
// read.
func (f *found) add(c check) { f.Checks = append(f.Checks, c) }

// stop ends discovery on a check: the page's state and sentence are its.
func (f found) stop(state string, c check) found {
	f.add(c)
	f.State, f.Detail, f.Ref = state, c.Detail, nil
	return f
}

// sourceWords is how a value's origin is said in a sentence.
func sourceWords(from string) string {
	switch from {
	case fromEnvironment:
		return "set by the environment"
	case fromSetting:
		return "set for this cluster in Settings"
	}
	return "the default"
}

// denied is the right that is missing, in a sentence, when err is a refusal;
// empty otherwise. who is whose right: the console's credentials for a read
// made with them, the person for one reviewed for them.
func denied(err error, who, verb, what, namespace string) string {
	if !errors.Is(err, extension.ErrDenied) && !errors.Is(err, extension.ErrRevealDenied) && !apierrors.IsForbidden(err) {
		return ""
	}
	where := "in namespace " + strconv.Quote(namespace)
	if namespace == "" {
		where = "in every namespace"
	}
	return fmt.Sprintf("access refused (403): %s may not %s %s %s", who, verb, what, where)
}

// Whose right a refusal names.
const (
	theConsole = "the console's credentials"
	thePerson  = "you"
)

// reads is how discover reads the cluster: every read through one of the
// console's doors, none with a client of its own.
type reads interface {
	// service is the collector's Service as the configuration names it, read
	// with the console's credentials (Host.ConfiguredService).
	service(ctx context.Context, namespace, name string) (*corev1.Service, error)
	// labeled is every Service labeled collectorLabel that the person may
	// list (Host.List, every namespace), and the sentence saying the answer
	// was narrowed to their namespaces, "" when it was not.
	labelled(ctx context.Context) ([]corev1.Service, string, error)
	// token is one key of one Secret: with the console's credentials when the
	// configuration names it (Host.ConfiguredSecret), for the person otherwise
	// (Host.Reveal).
	token(ctx context.Context, namespace, name, key string, configured bool) (string, error)
}

// servicesGVR is core Services, as Host.List is asked for them.
var servicesGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}

// hostReads is reads through the Host an extension is handed, for one request.
type hostReads struct {
	r       *http.Request
	h       extension.Host
	cluster string
}

func (hr hostReads) service(_ context.Context, namespace, name string) (*corev1.Service, error) {
	svc, err := hr.h.ConfiguredService(hr.r, hr.cluster, namespace, name)
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

func (hr hostReads) labelled(_ context.Context) ([]corev1.Service, string, error) {
	page, err := hr.h.List(hr.r, hr.cluster, extension.ListQuery{GVR: servicesGVR, LabelSelector: collectorLabel})
	if err != nil {
		return nil, "", err
	}
	out := make([]corev1.Service, 0, len(page.Items))
	for i := range page.Items {
		var svc corev1.Service
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, &svc); err != nil {
			return nil, "", fmt.Errorf("the Service %s/%s could not be read: %w", page.Items[i].GetNamespace(), page.Items[i].GetName(), err)
		}
		out = append(out, svc)
	}
	note := ""
	if !page.Complete {
		note = page.Note
		if note == "" {
			note = "the list of Services was not read to its end"
		}
	}
	return out, note, nil
}

func (hr hostReads) token(_ context.Context, namespace, name, key string, configured bool) (string, error) {
	if configured {
		return hr.h.ConfiguredSecret(hr.r, hr.cluster, namespace, name, key)
	}
	return hr.h.Reveal(hr.r, hr.cluster, namespace, name, key)
}

// configuredObjects is what wanted names from the configuration alone: the
// collector's Service and its token's Secret, when the environment or the
// cluster's setting names the collector. Found by its label, nothing is: the
// namespace is the cluster's.
func configuredObjects(w wanted) extension.Configuration {
	if w.Invalid != "" || w.Service == "" || w.Namespace == "" {
		return extension.Configuration{}
	}
	out := extension.Configuration{Services: []extension.ObjectName{{Namespace: w.Namespace, Name: w.Service}}}
	if w.Secret != "" {
		out.Secrets = []extension.ObjectName{{Namespace: w.Namespace, Name: w.Secret}}
	}
	return out
}

// discover looks for a collector in one cluster and, when it asks for one, its
// token: each step a check.
func discover(ctx context.Context, rd reads, w wanted) found {
	if w.Invalid != "" {
		f := failed("misconfigured", w.Invalid)
		f.add(check{Step: stepCollector, State: checkFailed, Detail: w.Invalid})
		return f
	}
	var f found
	var svc *corev1.Service
	if w.Service != "" {
		f.Via, f.Configured = viaConfiguration, w.where()
		value := w.where()
		got, err := rd.service(ctx, w.Namespace, w.Service)
		missing := errors.Is(err, extension.ErrNoSuchService) || apierrors.IsNotFound(err)
		switch {
		case missing && w.CollectorFrom == fromEnvironment && !w.Confined:
			// One variable for every cluster: where the Service it names is
			// not, the defaults still look here.
			gone := fmt.Sprintf("%s names %s, which does not exist in this cluster", CollectorEnv, value)
			f.add(check{Step: stepCollector, State: checkMissing, Detail: gone + "; looking for the one labeled " + collectorLabel,
				Value: value, Source: fromEnvironment})
			f.Via = ""
			if svc, f = byLabel(ctx, rd, f, "set "+CollectorEnv+" to it"); svc == nil {
				if f.State == "absent" {
					f.Detail = gone + ", and no Service here is labeled " + collectorLabel
				}
				return f
			}
			f.Source = gone + "; " + f.Source
		case missing:
			return f.stop("absent", check{Step: stepCollector, State: checkMissing, Value: value, Source: w.CollectorFrom,
				Detail: fmt.Sprintf("the collector's Service %s, %s, does not exist in this cluster", value, sourceWords(w.CollectorFrom))})
		case err != nil:
			detail := denied(err, theConsole, "get", "services/"+w.Service, w.Namespace)
			state := checkDenied
			if detail == "" {
				detail, state = "its Service "+value+" could not be read: "+shortError(err), checkFailed
			}
			return f.stop("unusable", check{Step: stepCollector, State: state, Value: value, Source: w.CollectorFrom, Detail: detail})
		default:
			svc = got
			f.Source = fmt.Sprintf("the collector at %s, %s", value, sourceWords(w.CollectorFrom))
			f.add(check{Step: stepCollector, State: checkOK, Value: value, Source: w.CollectorFrom,
				Detail: fmt.Sprintf("the collector's Service %s, %s", value, sourceWords(w.CollectorFrom))})
		}
	} else if w.Confined {
		return f.stop("absent", check{Step: stepCollector, State: checkMissing, Source: fromDefault,
			Detail: CollectorEnv + " is not set"})
	} else {
		if svc, f = byLabel(ctx, rd, f, settingsAdvice); svc == nil {
			return f
		}
	}

	ref, why := refOf(svc)
	if ref == nil {
		return f.stop("unusable", check{Step: stepCollector, State: checkFailed, Value: svc.Namespace + "/" + svc.Name, Detail: why})
	}
	// The token's Secret is the configuration's only when the Service is: the
	// one it names, found where it named it.
	configured := f.Via == viaConfiguration
	return withToken(ctx, rd, w, svc, ref, f, configured)
}

// settingsAdvice is where a person names the collector, when the environment
// does not.
const settingsAdvice = "name the collector in Settings › Extra › KubeCron"

// byLabel finds the collector by its Service label: exactly one, or none
// chosen. advice is where to name it instead: the Settings page, or - when the
// environment names a Service this cluster lacks, and so decides the setting
// on every cluster - that variable, since the page would refuse the write.
func byLabel(ctx context.Context, rd reads, f found, advice string) (*corev1.Service, found) {
	f.Via = viaLabel
	items, narrowed, err := rd.labelled(ctx)
	if err != nil {
		detail := denied(err, thePerson, "list", "services", "")
		state := checkDenied
		if detail == "" {
			detail, state = "could not look for a KubeCron collector in this cluster: "+shortError(err), checkFailed
		} else {
			detail = "could not look for the Service labeled " + collectorLabel + ": " + detail +
				"; " + advice
		}
		f.Via = ""
		return nil, f.stop("unusable", check{Step: stepCollector, State: state, Source: fromDefault, Detail: detail})
	}
	switch len(items) {
	case 0:
		f.Via = ""
		if narrowed != "" {
			// What the person may not list is not looked in: a collector there
			// is not theirs to be told of.
			return nil, f.stop("absent", check{Step: stepCollector, State: checkMissing, Source: fromDefault,
				Detail: "no Service you may list is labeled " + collectorLabel + " (" + strings.TrimSuffix(narrowed, ".") +
					"), so runs Kubernetes has already collected cannot be shown; " + advice})
		}
		return nil, f.stop("absent", check{Step: stepCollector, State: checkMissing, Source: fromDefault,
			Detail: "no KubeCron collector runs in this cluster (no Service is labeled " + collectorLabel +
				"), so runs Kubernetes has already collected cannot be shown"})
	case 1:
		svc := &items[0]
		where := svc.Namespace + "/" + svc.Name
		f.Source = fmt.Sprintf("the collector at %s, found by its label %s", where, collectorLabel)
		f.add(check{Step: stepCollector, State: checkOK, Value: where, Source: fromDefault,
			Detail: "the collector's Service " + where + ", the one labeled " + collectorLabel})
		return svc, f
	}
	names := make([]string, 0, len(items))
	for _, s := range items {
		names = append(names, s.Namespace+"/"+s.Name)
	}
	sort.Strings(names)
	f.Candidates = names
	return nil, f.stop("unusable", check{Step: stepCollector, State: checkSeveral, Source: fromDefault,
		Detail: fmt.Sprintf("%d Services are labeled %s (%s), and none is chosen: %s",
			len(names), collectorLabel, strings.Join(names, ", "), advice)})
}

// withToken reads the token for a collector that may want one, from the Secret
// w names **in the collector's own namespace** and no other: with the
// console's credentials when the configuration named the collector
// (configured), for the person when it was found by its label.
func withToken(ctx context.Context, rd reads, w wanted, svc *corev1.Service, ref *CollectorRef, f found, configured bool) found {
	ok := func() found {
		f.State, f.Ref = "ok", ref
		return f
	}
	value := svc.Namespace + "/" + w.Secret
	if svc.Annotations[annAuth] == "none" {
		f.add(check{Step: stepSecret, State: checkSkipped, Value: value, Source: w.SecretFrom,
			Detail: "no token is read: the collector's Service says " + annAuth + ": none"})
		return ok()
	}
	// Bearer said, or nothing said: a collector that says nothing may still
	// want one, and is read without if there is none to send.
	required := svc.Annotations[annAuth] == "bearer"
	fail := func(state, detail string) found {
		c := check{Step: stepSecret, State: state, Value: value, Source: w.SecretFrom, Detail: detail}
		if required {
			return f.stop("unusable", c)
		}
		c.Detail += "; none is sent"
		f.add(c)
		return ok()
	}
	who := theConsole
	if !configured {
		who = thePerson
	}
	token, err := rd.token(ctx, svc.Namespace, w.Secret, w.Key, configured)
	switch {
	case errors.Is(err, extension.ErrNoSuchSecret) || apierrors.IsNotFound(err):
		return fail(checkMissing, fmt.Sprintf("the token's Secret %s, %s, was not found", value, sourceWords(w.SecretFrom)))
	case errors.Is(err, extension.ErrNoSuchKey):
		return fail(checkMissing, fmt.Sprintf("the token's Secret %s carries no %q key", value, w.Key))
	case err != nil:
		if detail := denied(err, who, "get", "secrets/"+w.Secret, svc.Namespace); detail != "" {
			return fail(checkDenied, "the token's Secret "+value+": "+detail)
		}
		return fail(checkFailed, "the token's Secret "+value+" could not be read: "+shortError(err))
	}
	if token == "" {
		return fail(checkMissing, fmt.Sprintf("the token's Secret %s carries no %q key", value, w.Key))
	}
	ref.Token, ref.TokenNamespace, ref.TokenSecret = token, svc.Namespace, value
	f.add(check{Step: stepSecret, State: checkOK, Value: value, Source: w.SecretFrom,
		Detail: fmt.Sprintf("the token's Secret %s, key %s, %s", value, w.Key, sourceWords(w.SecretFrom))})
	return ok()
}

// refOf is where a collector's Service says it answers: its first port, and
// its API path or the contract's own.
func refOf(svc *corev1.Service) (*CollectorRef, string) {
	ref := &CollectorRef{Namespace: svc.Namespace, Service: svc.Name, APIPath: svc.Annotations[annAPIPath]}
	if ref.APIPath == "" {
		ref.APIPath = "/api/v1"
	}
	if len(svc.Spec.Ports) == 0 {
		return nil, fmt.Sprintf("its Service %s exposes no port", ref.Where())
	}
	ref.Port = int(svc.Spec.Ports[0].Port)
	return ref, ""
}

// forwardGetter reads the collector through a local address a port-forward
// opened, adding the bearer header the API server's proxy would have dropped.
func forwardGetter(client *http.Client, addr string, ref CollectorRef) getter {
	return func(ctx context.Context, path string, query url.Values, headers map[string]string) ([]byte, error) {
		u := url.URL{Scheme: "http", Host: addr, Path: path, RawQuery: query.Encode()}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		if _, sends := headers["Authorization"]; sends && ref.tokenElsewhere() != "" {
			// Checked here as well as in openCollector: the last place before
			// the header leaves the process.
			return nil, errors.New(ref.tokenElsewhere())
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &statusError{code: resp.StatusCode, where: ref.Where(), token: ref.Token != "", secret: ref.TokenSecret}
		}
		return body, nil
	}
}

// statusError is a collector that answered with something other than 200.
type statusError struct {
	code  int
	where string
	// token says whether one was sent: a collector refusing a request that
	// carried none wants one that could not be read. secret is where it was
	// read, namespace/name.
	token  bool
	secret string
}

// refused reports a collector that refused the request's credentials.
func (e *statusError) refused() bool {
	return e.code == http.StatusUnauthorized || e.code == http.StatusForbidden
}

func (e *statusError) Error() string {
	switch e.code {
	case http.StatusUnauthorized, http.StatusForbidden:
		if !e.token {
			return fmt.Sprintf("the collector at %s asks for a token (%d), and none could be read to send", e.where, e.code)
		}
		return fmt.Sprintf("the collector at %s refused the token from Secret %s (%d)", e.where, e.secret, e.code)
	case http.StatusNotFound:
		return fmt.Sprintf("the collector at %s does not know this address (404)", e.where)
	}
	return fmt.Sprintf("the collector at %s answered %d", e.where, e.code)
}

// notFound reports a collector 404, which on a run address means it no longer
// holds that run.
func notFound(err error) bool {
	var s *statusError
	return errors.As(err, &s) && s.code == http.StatusNotFound
}

// collectorCluster is one cluster the collector observes.
type collectorCluster struct {
	ID             string    `json:"id"`
	MetricsEnabled bool      `json:"metrics_enabled"`
	ObservedSince  time.Time `json:"observed_since"`
}

// collectorDoc is the discovery document, reduced to what is branched on.
type collectorDoc struct {
	APIVersions []string           `json:"api_versions"`
	Clusters    []collectorCluster `json:"clusters"`
	Retention   struct {
		RunDays int `json:"run_days"`
		LogDays int `json:"log_days"`
	} `json:"retention"`
}

// Collector is an open conversation with one: the discovery document read, the
// contract version agreed, and the cluster this page is about identified.
type Collector struct {
	ref     CollectorRef
	get     getter
	headers map[string]string

	// ClusterID is what the collector calls this cluster: its Helm value, which
	// need not be the kubeconfig context's name.
	ClusterID string
	// ObservedSince is when the collector started watching. Zero when it did not
	// say, and then nothing can be called "not watched".
	ObservedSince time.Time
	// MetricsEnabled separates a cluster that records no samples from a run too
	// short to have been sampled.
	MetricsEnabled bool
	// RunDays and LogDays differ: a run record outlives the log body it points
	// at, and that gap is why an empty log must never read as a silent run.
	RunDays, LogDays int
}

// Where names the collector in a sentence.
func (c *Collector) Where() string { return c.ref.Where() }

// openCollector reads the discovery document and agrees a contract. A collector
// speaking only versions this build does not know is reported and skipped,
// leaving the page on the rung beneath.
//
// err is what the collector answered when it did not agree, so the caller can
// tell a refused token (statusError.refused) from one that did not answer.
func openCollector(ctx context.Context, get getter, ref CollectorRef) (*Collector, Availability, error) {
	headers := map[string]string{}
	if why := ref.tokenElsewhere(); why != "" {
		return nil, Availability{Detail: why}, errors.New(why)
	}
	if ref.Token != "" {
		headers["Authorization"] = "Bearer " + ref.Token
	}
	c := &Collector{ref: ref, get: get, headers: headers}

	var doc collectorDoc
	decodeFailed, err := c.read(ctx, "/collector", nil, &doc)
	switch {
	case decodeFailed:
		return nil, Availability{Detail: fmt.Sprintf("the collector at %s answered with something unreadable", ref.Where())}, err
	case err != nil:
		return nil, Availability{Detail: fmt.Sprintf("the collector at %s did not answer: %s", ref.Where(), shortError(err))}, err
	}
	if !speaksOurVersion(doc.APIVersions) {
		return nil, Availability{Detail: fmt.Sprintf("the collector at %s speaks %v and this build speaks %s, so its history is not read",
			ref.Where(), doc.APIVersions, supportedAPIVersion)}, nil
	}
	// One collector per cluster reports exactly one cluster, whatever its ID.
	// With several, asking for one it does not know would return an empty list
	// that reads as "nothing ran", so that is refused.
	if len(doc.Clusters) != 1 {
		return nil, Availability{Detail: fmt.Sprintf(
			"the collector at %s observes %d clusters, and one per cluster is what this page reads", ref.Where(), len(doc.Clusters))}, nil
	}
	cl := doc.Clusters[0]
	c.ClusterID, c.ObservedSince, c.MetricsEnabled = cl.ID, cl.ObservedSince, cl.MetricsEnabled
	c.RunDays, c.LogDays = doc.Retention.RunDays, doc.Retention.LogDays
	return c, Availability{OK: true, Detail: fmt.Sprintf("a KubeCron collector at %s", ref.Where())}, nil
}

// read fetches one document and decodes it. A collector that did not answer
// and one that answered something unreadable are kept apart, because they are
// fixed by different people.
func (c *Collector) read(ctx context.Context, path string, query url.Values, into any) (decodeFailed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, collectorTimeout)
	defer cancel()
	raw, err := c.get(ctx, c.ref.APIPath+path, query, c.headers)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return true, err
	}
	return false, nil
}

func speaksOurVersion(versions []string) bool {
	for _, v := range versions {
		if v == supportedAPIVersion {
			return true
		}
	}
	return false
}

// Outcome is what the collector knows about one CronJob's runs over its 7-day
// window.
type Outcome struct {
	// Runs is the denominator, and it matters more than the failure count: 4
	// failures out of 4 609 runs is noise and 3 out of 5 is broken.
	Runs, Failed int
	// LastFailure is when the most recent run started, if it failed.
	LastFailure time.Time
	// AvgDuration and MaxDuration are nullable on the wire and stay pointers: a
	// figure nobody recorded is not a run that took no time.
	AvgDuration, MaxDuration *time.Duration
	// Failing is now, not the window: the last FailingRuns finished runs all
	// failed. One success clears it. Set from a page
	// of runs (FailingNow), only for a CronJob whose last run failed.
	Failing bool
}

// FailingRuns is how many finished runs in a row make a CronJob Failing:
// three, so a flaky job does not blink and a broken one shows within three
// periods.
const FailingRuns = 3

// FailingNow says whether the newest FailingRuns finished runs of a page all
// failed. A running run is skipped: it has not answered yet. Fewer than
// FailingRuns finished runs is never Failing.
func FailingNow(page RunPage) bool {
	seen := 0
	for _, r := range page.Runs {
		switch r.Status {
		case "running":
			continue
		case "failed":
			seen++
			if seen == FailingRuns {
				return true
			}
		default:
			return false
		}
	}
	return false
}

// WithFailing reads the newest runs of every CronJob whose last run failed,
// and marks those whose last FailingRuns finished runs all failed. Only
// those: a CronJob whose last run succeeded cannot be Failing, so the board
// asks the collector once per CronJob in trouble and never once per CronJob.
func (r *Report) WithFailing(ctx context.Context, c *Collector) {
	for i := range r.CronJobs {
		h := r.CronJobs[i].History
		if h == nil || h.LastFailure.IsZero() {
			continue
		}
		if page, avail := c.Runs(ctx, r.CronJobs[i].Namespace, r.CronJobs[i].Name, FailingRuns*2, ""); avail.OK {
			h.Failing = FailingNow(page)
		}
	}
}

// History is the outcome figures for every CronJob the collector knows in this
// cluster, keyed by namespace/name.
func (c *Collector) History(ctx context.Context) (map[string]Outcome, Availability) {
	var doc struct {
		CronJobs []struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Stats7d   *struct {
				Total         int    `json:"total"`
				Failed        int    `json:"failed"`
				AvgDurationMS *int64 `json:"avg_duration_ms"`
				MaxDurationMS *int64 `json:"max_duration_ms"`
			} `json:"stats_7d"`
			LastRun *struct {
				Status    string    `json:"status"`
				StartedAt time.Time `json:"started_at"`
			} `json:"last_run"`
		} `json:"cronjobs"`
	}
	decodeFailed, err := c.read(ctx, "/clusters/"+url.PathEscape(c.ClusterID)+"/cronjobs", nil, &doc)
	switch {
	case decodeFailed:
		return nil, Availability{Detail: fmt.Sprintf("the collector at %s answered its CronJob listing with something unreadable", c.Where())}
	case err != nil:
		return nil, Availability{Detail: fmt.Sprintf("the collector at %s answered its discovery document but not its CronJobs: %s", c.Where(), shortError(err))}
	}
	out := make(map[string]Outcome, len(doc.CronJobs))
	for _, cj := range doc.CronJobs {
		if cj.Stats7d == nil {
			// No recorded run: no entry, so the page draws an empty cell and not
			// "0 of 0".
			continue
		}
		o := Outcome{Runs: cj.Stats7d.Total, Failed: cj.Stats7d.Failed,
			AvgDuration: millis(cj.Stats7d.AvgDurationMS), MaxDuration: millis(cj.Stats7d.MaxDurationMS)}
		if cj.LastRun != nil && cj.LastRun.Status == "failed" {
			o.LastFailure = cj.LastRun.StartedAt
		}
		out[cj.Namespace+"/"+cj.Name] = o
	}
	return out, Availability{OK: true, Detail: fmt.Sprintf(
		"a KubeCron collector at %s, watching since %s — outcomes over its 7-day window, %d days of run records and %d days of log bodies behind them",
		c.Where(), c.ObservedSince.Format("2 Jan 2006"), c.RunDays, c.LogDays)}
}

// WithHistory joins the collector's outcomes onto the CronJobs already judged.
// Only those: a CronJob the person may not list is not in the report, so its
// outcome has nowhere to go.
func (r *Report) WithHistory(outcomes map[string]Outcome, avail Availability) {
	r.History = avail
	if !avail.OK {
		return
	}
	for i := range r.CronJobs {
		cj := &r.CronJobs[i]
		if o, ok := outcomes[cj.Namespace+"/"+cj.Name]; ok {
			out := o
			cj.History = &out
			if o.Failed > 0 {
				r.Failing++
			}
		}
	}
	sortCronJobs(r.CronJobs)
}

// millis turns a nullable millisecond count into a duration. A negative value
// is dropped rather than clamped: "-2s" is worse than nothing at all.
func millis(ms *int64) *time.Duration {
	if ms == nil || *ms < 0 {
		return nil
	}
	d := time.Duration(*ms) * time.Millisecond
	return &d
}
