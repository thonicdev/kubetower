// Package extension is the only door into this console for an extra: an
// additive page that reaches the rest of the console through `Host` and the
// public packages, and never through `internal/`.
//
// # Why it exists at all
//
// Everything this console is made of is under `internal/`: the cluster manager,
// the list readers, the port forwarder. An extra may reach none of it. Every
// extra is a package of this module (`extras/`), where Go itself would allow
// the import, so the fence is `extras/boundary_test.go`, which fails when an
// extra imports an `internal/` package. This package is what an extra reaches instead, and
// `Host` is the whole of it.
//
// That is a constraint and also the design: **every method on Host is API
// surface the extras compile against.** A method earns its place by a call
// site, and nothing is added against a future need — an unused method is a promise nobody asked for that cannot be
// withdrawn without changing every extra that relies on it.
//
// # What is deliberately absent
//
//   - **No cache handle.** The console keeps no object cache to hand over:
//     List reads the cluster for each request and reviews the answer for the
//     person on its way out, and a page reaching past that review would, in
//     server mode, read another user's objects.
//   - **No gate query.** The core mounts an extension's routes itself, behind
//     the per-cluster gate, so an extension cannot forget to check it — and
//     therefore never needs to ask. A page that could ask is a page that can get
//     the answer wrong and contradict the gate that mounted it.
//   - **No template or rendering surface.** An extension renders its own
//     responses.
//   - **No write door beyond ConfirmToken and RefreshApplication.** Writes go through the core's single
//     guarded path, and a second one is the thing that path exists to prevent.
//   - **No unreviewed read.** The clients Client hands out answer discovery
//     and nothing else, at run time: an object read or a change sent through
//     them is refused before it leaves the console.
package extension

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
)

// Extension is one additive page, as the console needs to know it.
//
// The console learns six things about a page and nothing else: what to call it,
// what it needs, what it was called before, and what to mount. It never learns
// what the page *does*, which is what keeps them removable one at a time.
type Extension interface {
	// Key is stable and is three things at once: the last segment of the page's
	// URL, the key its per-cluster switch is persisted under, and the name
	// KT_EXTRAS uses. A bookmark and a stored state file both carry it, so it is
	// not free to change — see Legacy for what to do when it changes anyway.
	Key() string
	// Label is what the menu and the settings page call it.
	Label() string
	// Summary is one sentence saying what turning it on adds, because a checkbox
	// reading "argocd" tells an operator who has not used it nothing.
	Summary() string
	// Needs names what the cluster must be running for the page to say anything
	// useful, drawn beside the switch. Being told why an empty page is empty
	// before the click is cheaper than after.
	Needs() string
	// Legacy is every key this page was published under before, so a stored
	// state file, a bookmark and a KT_EXTRAS line written against the old
	// spelling keep working. One-way: nothing ever writes an old key back.
	//
	// Dropping one is a silent failure, which is why it is on the contract
	// rather than left to a migration somewhere: an operator who had the page on
	// gets it back off after an upgrade, with no error and nothing saying why,
	// and that reads as the feature having been removed.
	Legacy() []string
	// Routes is what to mount. The console mounts them itself, behind the
	// per-cluster gate, so a route that is off answers 404 whether or not the
	// extension remembered to ask.
	//
	// **Routes must be pure: it lists, and does nothing else.** It may be
	// called more than once, and must return the same routes each time: first
	// at registration, with a Host that answers every call with an error, so
	// that a route the router cannot mount is refused at start-up rather than
	// panicking in it; then when the console mounts the routes, with the real
	// Host. It must not use h while listing — build handlers that use it when
	// a request arrives — nor start anything that outlives the call, and it
	// must build its handlers from the h of each call rather than keep the
	// first ones: handlers kept from registration answer every request with
	// the registration Host's error.
	Routes(h Host) []Route
}

// Route is one registration.
//
// Method and Pattern are `net/http`'s own, so a pattern may carry wildcards and
// the `{$}` end-of-path marker, and the console does not reinterpret either.
//
// **Every route is held to the console's own CSRF rules** before its handler
// runs: a request whose Origin is not the console is refused whatever its
// method, and any method but GET, HEAD and OPTIONS must carry the header
// `X-KubeTower-Request: 1`, as every mutation under /api/ does. An extension's
// front end that posts sends it; a WebSocket handshake is a GET and needs only
// the Origin.
//
// **A GET route must change nothing.** A cross-site top-level navigation is a
// GET that carries the session cookie and no Origin, so neither check can stop
// it: anything a GET route changes, any page on the web can make a signed-in
// person change by linking to it. Use POST (or PUT, PATCH, DELETE) for every
// change; the WebSocket handshake is the one GET route that may start
// something, and it is held to the Origin.
type Route struct {
	// Method is "GET", "POST" and so on, or empty for a route that answers any
	// method — which is what a WebSocket upgrade needs, an upgrade not being a
	// verb.
	Method string
	// Pattern is relative to the extension's own prefix and must begin with a
	// slash. The console prepends the prefix, so an extension cannot mount
	// itself anywhere but under its own key.
	Pattern string
	Handler http.Handler
}

// Host is the only door into internal/.
//
// Every method carries the call site that requires it, never a guess at what a
// page might want.
//
// **Every method takes the *http.Request rather than a context**, except
// Forward. That is not decoration: in server mode the access review of what is
// read is made for the identity on the request, and a method taking a bare
// context would have to be handed that identity separately — which is a
// parameter somebody forgets, and forgetting it in server mode reads another
// user's objects.
//
// **A cluster name that is not in the kubeconfig is answered with
// ErrUnknownCluster, wrapped**, by Client, Definitions, List, Reveal,
// ConfiguredSecret, ConfiguredService, MayReadLogs, Usage, Exec, Forward and
// ForwardAsConsole - ask with errors.Is, and
// answer it as UnknownClusterText says rather than as a failed read.
//
// **A read refused for the person is ErrDenied or ErrUnreviewable, wrapped**,
// from List, Usage, ContainerUsage, UsageWindow, Exec and Forward - ask with
// errors.Is, never by the message or by apierrors.IsForbidden alone, and
// answer it as a refusal rather than as a read that failed. Their own comments
// say what each one means; Reveal and MayReadLogs have sentinels of their own.
type Host interface {
	// Client is the cluster's typed and unstructured clients, **for discovery
	// only**: which groups, versions and kinds the cluster serves.
	//
	// **Anything else sent through them is refused before it leaves the
	// console**, on every profile: a get, a list or a watch of any object,
	// through either client or its raw REST client, and every change - a
	// create, an update, a patch, a delete, an exec or a port-forward. The
	// refusal is an error, never a 403: it is not the cluster's answer about
	// anyone.
	//
	// That is the point. A read through them would be made with the console's
	// own credentials - under the server profile its ServiceAccount's - and
	// shown to whoever reached the page. What a page reads of a cluster's
	// objects goes through the doors that review it for the person behind r:
	// List for what it lists (by name too, with a field selector), Reveal for a
	// Secret's value, and ConfiguredSecret and ConfiguredService for the few
	// objects the console's own configuration names. A write an extension
	// needs is the console's to make, through its one guarded door.
	//
	// The struct and its two fields stay as they were, so an extension that
	// asks discovery through Typed.Discovery() compiles and works unchanged.
	Client(r *http.Request, cluster string) (Clients, error)

	// Definitions is the cluster's custom-resource catalog.
	//
	// Two pages resolve custom resource kinds and must be able to tell "not
	// served" from "unknown" from "ambiguous" — three errors, three sentences.
	// Collapsing them renders an unreadable cluster as an empty one, which is
	// the one failure this console's whole error vocabulary exists to prevent.
	Definitions(r *http.Request, cluster string) (CRDRegistry, error)

	// List reads a list call to its end, following the continue token, from
	// the API server's watch cache rather than a quorum read: the console
	// keeps no object cache of its own.
	//
	// Two pages page and one reads job status. It is a method and not a
	// client, deliberately: what it reads is reviewed for the person behind r
	// on its way out, and a page must not reach past that. A person who may
	// not list the kind in every namespace gets the namespaces where they may,
	// as a Page with Complete false and a Note.
	//
	// **A refusal is classifiable with errors.Is**: ErrDenied when the cluster
	// says the person may not make the list (in the namespace asked, or for a
	// cluster-scoped kind at all), ErrUnreviewable when the console could not
	// ask. ErrUnknownCluster is a cluster that is not in the kubeconfig;
	// anything else is a read that failed, and never an empty Page.
	//
	// **Secrets are refused** (core `v1/secrets`, ErrSecretsNotListed, before
	// anything is read): a list hands over every value of every Secret it
	// returns, reviewed as `list` and recorded nowhere. A value an extension
	// needs comes through Reveal, one key of one named Secret. So is a query
	// whose group, version, resource or namespace is not one path segment.
	//
	// **ListQuery.Columns reads the list as the API server's table**: the
	// kind's printer columns and each object's metadata, in Page.Columns and
	// Page.Rows, with Page.Items empty. Use it for a kind whose objects are
	// large and whose printer columns say what the page needs - a
	// trivy-operator report holds every finding, a few hundred kilobytes,
	// where its row is a few hundred bytes of counts - or when the page needs
	// only the metadata: owner references, labels, creation time. Everything
	// above holds for it unchanged. An API server that answers anything but a
	// table is an error, never objects decoded in its place.
	List(r *http.Request, cluster string, q ListQuery) (Page, error)

	// Reveal reads one key of one named Secret, for the person behind r.
	//
	// The Redis viewer authenticates with the password its pod's own container
	// is started with, and that is a Secret's value. Through Client it would be
	// read with the console's credentials, reviewed for nobody and recorded
	// nowhere; through List the whole Secret would come back. So it is the
	// console's own reveal door, the one its env panel uses:
	//
	//   - **reviewed for the person as `get secrets/<name>`, by name**, so a
	//     Role granting one named Secret holds. Under the server profile the
	//     console's ServiceAccount never reads it for someone the review
	//     refused, and a review that could not be made refuses too. A session
	//     signed in with the shared password is reviewed as the console
	//     itself, so it reads what the console may, as the env panel's does;
	//   - **recorded** as a reveal naming the key and the extension that asked:
	//     the attempt before the value is read - an attempt that cannot be
	//     written refuses the read - and the outcome after;
	//   - **the value is in no record, no log line and no error.** It is the
	//     stored bytes, unchanged, which is the caller's to hold and never to
	//     hand to a browser.
	//
	// Every refusal is classifiable with errors.Is, never by its message:
	// ErrRevealDenied, ErrRevealUnreviewed, ErrNoSuchSecret, ErrNoSuchKey and
	// ErrUnknownCluster. Anything else is a read that failed - the cluster did
	// not answer - and says so.
	Reveal(r *http.Request, cluster, namespace, name, key string) (string, error)

	// ConfiguredSecret reads one key of one Secret **that the console's
	// configuration names**, with the console's own credentials, for a
	// backend the console talks to on the person's behalf: Argo CD's
	// read-only token, the KubeCron collector's token.
	//
	// Reveal is the door for a Secret the person reads; this one is for a
	// credential the operator gave the console, which the person never sees.
	// Reviewed for the person it would be the same as handing it to them -
	// whoever may read the token may use it directly - so it is not:
	//
	//   - **only an object the extension's Configured names** on cluster, from
	//     the environment, the cluster's settings and the extension's
	//     defaults. Any other namespace or name is ErrNotConfigured, and
	//     nothing is read or reviewed: a name taken from cluster data would
	//     let whoever wrote that data choose what the console reads;
	//   - **read as the console** (its ServiceAccount under the server profile,
	//     the kubeconfig's identity on the desktop), reviewed as the console's
	//     own `get secrets/<name>`. Under the server profile a request with
	//     nobody signed in is refused first;
	//   - **recorded** as a reveal by the console on the person's behalf,
	//     naming the key and the extension: the attempt before the value is
	//     read - an attempt that cannot be written refuses the read - and the
	//     outcome after;
	//   - **the value is in no record, no log line and no error**, and the
	//     page never hands it to a browser: it is a header on a request the
	//     page makes, nothing else. What the page shows the person out of that
	//     backend stays reviewed for them by other means - the objects it is
	//     about through List, a log through MayReadLogs.
	//
	// Refusals and absences are classifiable with errors.Is: ErrNotConfigured,
	// ErrRevealDenied (the console may not read it), ErrRevealUnreviewed,
	// ErrNoSuchSecret, ErrNoSuchKey and ErrUnknownCluster. Anything else is a
	// read that failed.
	ConfiguredSecret(r *http.Request, cluster, namespace, name, key string) (string, error)

	// ConfiguredService reads one Service **that the console's configuration
	// names**, with the console's own credentials: where Argo CD's server or
	// the KubeCron collector answers, before ForwardAsConsole digs to it.
	//
	// Held to the extension's Configured as ConfiguredSecret is (anything else
	// is ErrNotConfigured, nothing read), read and reviewed as the console's
	// own `get services/<name>`, and not recorded: a Service carries nothing
	// secret. A Service found by a label is not configured; it is listed
	// through List, for the person.
	//
	// ErrNotConfigured, ErrDenied (the console may not read it),
	// ErrUnreviewable, ErrUnknownCluster; a Service that does not exist is
	// ErrNoSuchService. Anything else is a read that failed.
	ConfiguredService(r *http.Request, cluster, namespace, name string) (corev1.Service, error)

	// Exec runs one command in one container of a pod, for the person behind
	// r, and answers what it wrote and how it exited.
	//
	// The JVM Inspector runs `jcmd` beside the JVM it reads: nothing else in a
	// running Java container answers the heap, the flags or the threads without
	// a redeploy. Through Client the exec is refused (see Client); this is the
	// console's own exec door, the one its shells go through:
	//
	//   - **reviewed for the person as `create pods/exec` on the pod, by name**,
	//     so a person who may not open a shell in the pod may not run this
	//     either. A refusal is ErrDenied or ErrUnreviewable, wrapped, and
	//     nothing reaches the kubelet; on the desktop a review the console
	//     could not make lets the command through, as it does a shell;
	//   - **refused on a cluster marked read-only** (KT_READONLY_CLUSTERS), as
	//     the console's own pod shell is: ErrReadOnly (which is also ErrDenied),
	//     wrapped, before anything is counted, reviewed or sent; only the
	//     refusal is recorded;
	//   - **recorded** as an exec naming the container's pod, **the argv** and
	//     the extension that asked: the attempt before it runs - an attempt that
	//     cannot be written refuses it - and the outcome after. The argv is in
	//     the record, so it must never carry a secret;
	//   - **bounded**: no shell (the argv runs as is), no stdin, no terminal,
	//     ExecOutputLimit bytes of output (ExecResult.Truncated past it), and a
	//     deadline (ExecQuery.Timeout, ErrExecTimedOut past it);
	//   - **counted**: sixteen at once across the console, and under the
	//     server profile four per person, ErrExecBusy past either.
	//
	// **It runs whatever argv it is given.** What a page may run is the page's
	// to hold to a fixed list, as the Redis viewer holds its commands: the
	// review answers whether the person may exec in the pod, which is a shell
	// there, not whether this command is harmless.
	//
	// A program missing from the image is ErrNoSuchCommand; an unknown cluster
	// is ErrUnknownCluster, wrapped. A command that ran and exited non-zero is
	// an ExecResult with its ExitCode, not an error.
	Exec(r *http.Request, cluster string, q ExecQuery) (ExecResult, error)

	// MayReadLogs is the review `get pods/log` in namespace, for the person
	// behind r: nil when they may read the logs of pods there, an error when
	// they may not or when that could not be asked.
	//
	// A collector may keep a run's log after the pod is gone, and serve it to
	// the console with the console's own credentials. Kubernetes asks `get pods/log` before it
	// hands anyone a pod's log; a page that served the stored copy to whoever
	// may list the CronJob would hand it to people `kubectl logs` refuses. So
	// the page asks this before it asks its collector for the log.
	//
	// **Narrow on purpose**: one fixed question, not a general access review.
	// A door that answered any verb on any resource would let a page decide
	// what a person may do from a question of its own, and the console's
	// answer to "may they write" is its guarded write path, never a review an
	// extension reads.
	//
	// A refusal is classifiable with errors.Is: ErrLogsDenied when the cluster
	// said no, ErrLogsUnreviewed when the console could not ask. A session
	// signed in with the shared password is reviewed as the console itself, as
	// List is. An unknown cluster is ErrUnknownCluster, wrapped; a namespace
	// that is empty or not one path segment is an error, and nothing is asked.
	MayReadLogs(r *http.Request, cluster, namespace string) error

	// Usage is node and pod usage as the kubelets reported it.
	//
	// One page, and the honest-empty half is the reason it is on the contract
	// rather than left to each page: usage that cannot be trusted must not be
	// drawn as zero, and a float64 with no companion boolean is a
	// complete-looking API that cannot say so.
	//
	// **Reviewed for the person behind r**: `list nodes` before any kubelet is
	// asked, then each node's pods narrowed to the namespaces where they may
	// list pods (Usage.Complete false, with a Note). A refusal of the nodes is
	// classifiable with errors.Is: ErrDenied when the cluster says no,
	// ErrUnreviewable when the console could not ask.
	Usage(r *http.Request, cluster string, nodes []string) (Usage, error)

	// ContainerUsage is each container's CPU and memory now, from
	// metrics-server, in one namespace or, empty, in every one.
	//
	// For right-sizing: a suggestion is about one container's
	// request, so a pod's total is the wrong number to advise on, and Usage is
	// the kubelets' disk figures. **Metrics-server only, on purpose**: a
	// container's series in Prometheus is a different query whose `container`
	// label is absent on exactly the rows a naive one picks up, and guessing
	// there would hand a pod's whole memory to whichever container sorted first.
	//
	// **Reviewed for the person behind r**, as List is: refused when they may
	// not list pods in the namespace asked, and narrowed - Complete false, with
	// a Note - to the namespaces where they may when every namespace was asked.
	// No metrics-server, a refused read and an unreachable cluster are a
	// *MetricsUnavailable naming which; an empty map is a measurement. An
	// unknown cluster is ErrUnknownCluster, wrapped.
	ContainerUsage(r *http.Request, cluster, namespace string) (LiveUsage, error)

	// UsageWindow is seven days of each container's CPU and memory, at a
	// fifteen-minute step, from the cluster's Prometheus, in one namespace or,
	// empty, in every one.
	//
	// For right-sizing: the burst guard compares a peak with a
	// P95, the trend needs samples, and one scrape has neither. **Narrow and
	// typed, never PromQL**: Prometheus has no RBAC, so a raw query on this
	// contract would read any series for anyone. The core runs fixed queries
	// - a listing of the containers first, narrowed to the namespaces the
	// person behind r may list pods in, as ContainerUsage is, then CPU and
	// memory in batches of WindowBatch containers - and assembles the answers.
	//
	// Every answer about Prometheus is a Window.State, never an error and
	// never an empty map read as no load: no Prometheus, unreadable, empty, or
	// read - whole, or with Window.Partial saying what could not be. The errors are the request's: an unknown cluster
	// (ErrUnknownCluster, wrapped), a namespace that cannot be one, a person
	// who may not list its pods.
	UsageWindow(r *http.Request, cluster, namespace string) (Window, error)

	// DNSWindow is CoreDNS's own metrics over span, from the cluster's
	// Prometheus: queries per second, the NXDOMAIN and SERVFAIL shares, p50 and
	// p99, the cache hit rate, per pod and per upstream.
	//
	// **Narrow and typed, never PromQL**, for the reason UsageWindow gives:
	// Prometheus has no RBAC. The core runs fixed queries, selected by the IPs
	// of the CoreDNS pods it found, and keeps only the series of a live pod.
	//
	// **Reviewed for the person behind r**: `list pods` in the namespace the
	// CoreDNS pods run in, all or nothing. A refusal is ErrDenied or
	// ErrUnreviewable, wrapped, and nothing is read from Prometheus. Every
	// answer about Prometheus is a DNS.State, never an error: no Prometheus,
	// unreadable, empty, or read. The errors are the request's: an unknown
	// cluster (ErrUnknownCluster), a span that is not one, a refusal.
	DNSWindow(r *http.Request, cluster string, span DNSSpan) (DNS, error)

	// VolumeFill is each claim's used and capacity bytes from the cluster's
	// Prometheus - or VictoriaMetrics, which answers the same API (#131) - as
	// the kubelets last reported them (kubelet_volume_stats_*), for the claims
	// asked (#91).
	//
	// **Narrow and typed, never PromQL**, for the reason UsageWindow gives:
	// the core runs two fixed queries, selected by the claims' namespaces and
	// names, and hands back only the claims asked.
	//
	// **Reviewed for the person behind r**: `list persistentvolumeclaims` in
	// each namespace asked, before anything is read. Claims in a namespace
	// they may not list are left out - Complete false, with a Note - and when
	// that is every namespace the refusal is ErrDenied or ErrUnreviewable,
	// wrapped, and nothing is read. Every answer about the backend is a
	// VolumeFill.State, never an error: no Prometheus, unreadable, empty, or
	// read. The errors are the request's: an unknown cluster
	// (ErrUnknownCluster, wrapped), a name that cannot be a claim's, a refusal.
	VolumeFill(r *http.Request, cluster string, claims []ClaimRef) (VolumeFill, error)

	// ForwardAsConsole opens a tunnel as Forward does, but **dug with the
	// console's own credentials** rather than the person's. It is for a backend
	// the console talks to on the person's behalf: argocd-server for Argo CD's
	// diff, the KubeCron collector. The console reaches Prometheus the same
	// way. A person who may not port-forward into that backend's namespace
	// still gets what it answers about the objects they may read, without a
	// socket to every pod there.
	//
	// What it asks of the page, and why the door is safe to hand out:
	//
	//   - **What comes down it is the console's read, reviewed for nobody.**
	//     The page serves from it only what the person's own reviewed reads
	//     allow: an object List returned to them, a log MayReadLogs let them
	//     read. It asks those **before** it opens the tunnel, so a person they
	//     refused never causes one.
	//   - **Reviewed and recorded as the console acting for the person.** The
	//     review is the console's own `create pods/portforward` on the pod it
	//     lands on, with the Service and pod reads behind it, as for every
	//     other tunnel. The audit record names the console and, as on whose
	//     behalf, the person behind r. Under the server profile a request with
	//     nobody signed in is refused before anything is asked.
	//   - **Bounded by the console's own port-forward grant, and by nothing
	//     else.** The host digs to whatever namespace and Service the
	//     extension passes; it does not check where that target came from.
	//     What it can reach is what the console's ServiceAccount may
	//     port-forward into (on the desktop, the kubeconfig's identity), so
	//     choosing the target is the extension's responsibility: Argo CD's
	//     comes from the environment or its default, KubeCron's from the
	//     environment, the cluster's setting or, where neither names one, the
	//     Service carrying its label among those the person may list (List) -
	//     and on a console several people share (SharedConsole) from the
	//     environment alone, never by its label.
	//   - **Counted with Forward's tunnels**, against the same ceiling and the
	//     same per-person share, by the person behind r.
	//   - **Closed when r ends, at the latest**: a tunnel the console dug for
	//     one request does not outlive it. Stop gives the slot back sooner, and
	//     a page calls it when it is done.
	//
	// On the desktop the console's credentials are the kubeconfig's, the
	// person's own, so it reaches what Forward would. An unknown cluster is
	// ErrUnknownCluster, wrapped.
	ForwardAsConsole(r *http.Request, cluster, namespace, service string, port int) (Tunnel, error)

	// Forward opens a local address that reaches something inside a cluster, and
	// hands back the address, the pod it reached and the teardown (Tunnel).
	//
	// Two pages discover a service group and open a tunnel to it. **The
	// tunnels every extension holds open are counted together and capped**,
	// sixteen in the core today, and in a console shared by several people
	// each person has a share of them, four today, counted by the person ctx
	// carries: past either Forward returns an error whose sentence names the
	// limit, and calling stop - or the tunnel ending on its own - gives the
	// slot back, so a page must call stop when it is done. The cap is the
	// extensions' own: the port-forwards a person opens from the console are
	// counted apart.
	//
	// **It says which pod it reached** (Tunnel.Pod), because a Service may
	// front pods that differ - a primary and a replica with different
	// passwords, a selector that also catches something else - and a page that
	// sends a credential down the tunnel must send only the one it read for
	// that pod. Tunnel.PodKnown false is a tunnel that could not say, never a
	// pod with an empty name.
	//
	// **A context and not a request**, alone among these: a tunnel outlives the
	// request that opened it, and taking the request would invite holding it
	// past its cancellation.
	//
	// **Reviewed for the person ctx carries**: reading the Service and its pods,
	// and opening the tunnel to the pod, as `create pods/portforward`. A
	// refusal of any of the three is classifiable with errors.Is: ErrDenied when
	// the cluster says no, ErrUnreviewable when the console could not ask - a
	// context with nobody in it is refused that way under the server profile.
	// An unknown cluster is ErrUnknownCluster, wrapped.
	Forward(ctx context.Context, cluster, namespace, service string, port int) (Tunnel, error)

	// Setting is the value of one of this extension's text settings
	// (Configurable, Setting.Text) on cluster, as it is kept now: empty where
	// nothing is set, or where the key names no text setting it declares.
	//
	// KubeCron: where its collector is, set per cluster on
	// the Settings page and read on every request, so a change applies with no
	// restart. **Only the extension's own settings**: the door is handed to one
	// extension, and the key is looked up under that extension's key. The
	// environment variable a setting names (Setting.Env) is not folded in here;
	// the extension reads it itself, as it decides what wins.
	Setting(r *http.Request, cluster, key string) string

	// ConfirmToken is the word the second step of a guarded write sends back.
	//
	// One symbol, one page. It is on the contract because the alternative is a
	// page inventing its own confirmation word, which is a second write door
	// wearing the first one's clothes.
	ConfirmToken(r *http.Request) string

	// RefreshApplication asks Argo CD to compare one Application again, by
	// setting the annotation `argocd.argoproj.io/refresh=normal` on it.
	//
	// **The one write an extension can ask for, and as narrow as a write can
	// be**: one annotation, on one object of one kind (`applications.argoproj.io`),
	// and nothing else - not a general patch, so it cannot be turned into one.
	// The extension's route holds the second step to ConfirmToken before it
	// calls this.
	//
	// **Reviewed for the person asking**, as `patch` on the Application, and
	// through the console's one guarded write path: the read-only guardrail, a
	// dry run, the audit record and the `write:` line. The object is read first
	// and its uid and resourceVersion ride in the patch, so an Application
	// changed or recreated since is refused as a conflict. A refusal is
	// classifiable with errors.Is: ErrDenied when the cluster says no,
	// ErrUnreviewable when the console could not ask.
	RefreshApplication(r *http.Request, cluster, namespace, name string) error
}
