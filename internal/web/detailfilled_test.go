package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/resources"
)

// moment is a fixed instant, because a fixture built from time.Now() makes an
// expiry grade depend on when the test runs.
var moment = time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)

// The other half of `TestTheDetailPageCarriesEveryMemberTheContractDeclares`,
// which lives in internal/api and cannot reach this package — internal/web
// imports internal/api, so the dependency only runs one way.
//
// That test reads `api/openapi.yaml` and proves the member set on the wire is the
// member set the contract declares. It proves it over a **fake** backend, so on
// its own it would pass against this adapter filling none of them. This is the
// mapper, driven with an object that carries everything internal/detail can
// produce.
//
// A mapper that read only `Facts` and `Data` (as sections) would drop `Labels`,
// `Containers`, `Certificates`, `Event`, `Events`, `Relations`, `RelationsNote`,
// `Ports`, `URLs`, `ServiceType`, `ClusterIP`, `Controller`, `EnvNote` and
// `EventsNote`, and the drawer would show a fact table and nothing of the
// relations graph, the certificate parser, the envFrom expander or the taint
// grader.
//
// **This is a mapping test and not a behavior one.** What it pins is that the
// mapping does not drop them.
func TestTheDetailPageIsFilledFromTheProducersThatHoldIt(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "api-1"},
		detailFixtureObject(),
	)

	// An empty answer, a failed answer, and a page this test walked over without
	// looking at anything are three different things.
	if len(page.Sections) == 0 || len(page.Relations) == 0 {
		t.Fatalf("%d sections and %d relations; there is nothing to measure: %+v",
			len(page.Sections), len(page.Relations), page)
	}

	// --- the identity ------------------------------------------------------

	if page.Kind != "pods" || page.Name != "api-1" || page.Namespace != "shop-prod" || page.Title != "Pod" {
		t.Errorf("identity did not arrive: %+v", page)
	}

	// --- the events, which ride with the object ----------------------------

	// They have no operation of their own: the read that filled the facts above
	// already fetched them, so dropping the mapping loses the panel entirely and
	// nothing fails.
	if len(page.Events) != 2 {
		t.Fatalf("%d events arrived, want 2: %+v", len(page.Events), page.Events)
	}
	first := page.Events[0]
	if first.Reason != "BackOff" || first.Count != 340 || first.From != "kubelet on node-3" {
		t.Errorf("an event arrived without what makes it readable: %+v", first)
	}
	// Never clipped. The list clips to 140 characters because a table cell that
	// holds a paragraph is a column nobody can read; this is the panel where the
	// whole sentence exists.
	if !strings.Contains(first.Message, "Back-off restarting failed container api") {
		t.Errorf("the message did not arrive whole: %q", first.Message)
	}
	if first.First == nil || first.Last == nil || !first.First.Before(*first.Last) {
		t.Errorf("the span did not arrive: %+v", first)
	}
	// And an event with no instants carries none, rather than `0001-01-01`.
	if page.Events[1].First != nil || page.Events[1].Last != nil {
		t.Errorf("an event with no times arrived with some: %+v", page.Events[1])
	}
	// The note travels beside the list rather than instead of it: "nothing
	// recently" and "nothing ever" are the sentence this exists to tell apart.
	if !strings.Contains(page.EventsNote, "about an hour") {
		t.Errorf("the events note did not arrive: %q", page.EventsNote)
	}

	// --- the labels, as a map ----------------------------------------

	// A map and not a section of fields. The second is expressible today and
	// loses the chip layout, which is the part that makes a set scannable.
	if page.Labels["app"] != "api" {
		t.Errorf("labels did not arrive: %v", page.Labels)
	}
	// A label with no value is an ordinary label, not an absent one.
	value, ok := page.Labels["standalone"]
	if !ok || value != "" {
		t.Errorf("a label with no value did not arrive as an empty string: %q, present=%v", value, ok)
	}

	// --- the annotations, beside the labels and not instead of them ---

	// A map beside `labels`, drawn as the same chips, produced under a
	// different rule. A mapper that carried one and dropped the other is the
	// defect.
	if page.Annotations["meta.helm.sh/release-name"] != "shop" ||
		page.Annotations["deployment.kubernetes.io/revision"] != "7" {
		t.Errorf("annotations did not arrive: %v", page.Annotations)
	}

	// --- a fact's three renderings and its wrap ------------------------

	facts := map[string]api.DetailField{}
	for _, section := range page.Sections {
		if section.Key != "summary" {
			continue
		}
		for _, field := range section.Fields {
			facts[field.Label] = field
		}
	}
	if len(facts) == 0 {
		t.Fatal("the summary section carries no fields")
	}

	// A badge, colored text and plain text are three renderings. Collapsing the
	// first two makes "Running" and a node's name look like the same class of
	// thing.
	if got := facts["Status"]; !got.Pill || got.Tone != "ok" {
		t.Errorf("Status arrived pill=%v tone=%q, want a badge in the ok tone", got.Pill, got.Tone)
	}
	if got := facts["Node"]; got.Pill || got.Tone != "" {
		t.Errorf("Node arrived pill=%v tone=%q; the flags are per fact and not per table", got.Pill, got.Tone)
	}

	// A 260-character `PodScheduled` message drawn on one line in a 396px cell
	// would put 876px of the sentence outside a 1600px window. `nowrap` inherits into every cell of every fact table, so
	// only the cell can lift it and only this flag says which cell that is.
	if !facts["PodScheduled"].Prose {
		t.Error("a condition message did not arrive as prose; it will be clipped to one line in the one place it matters")
	}
	if facts["Status"].Prose {
		t.Error("a status arrived as prose; every fact wrapping is the defect the other way round")
	}

	// --- the graph --------------------------------------------

	byRole := map[string]api.Relation{}
	for _, relation := range page.Relations {
		byRole[relation.Role] = relation
	}
	if len(byRole) != len(page.Relations) {
		t.Fatalf("the fixture repeats a role; this test indexes by it: %+v", page.Relations)
	}

	// A relation with an object behind it.
	controller := byRole["Controlled by"]
	if !controller.Linkable || controller.Kind != "replicasets" || controller.Name != "api-7c9" {
		t.Errorf("the controller relation did not arrive addressable: %+v", controller)
	}
	if controller.Namespace != "shop-prod" {
		t.Errorf("the controller relation lost its namespace: %+v", controller)
	}

	// the paths a mounted object is mounted at, cap included. "Which secret
	// is at /etc/creds" is the question being asked, and the `+N more` element
	// travels with the list or the list becomes a lie that does not admit it.
	secret := byRole["Secret"]
	if len(secret.Mounts) != 2 || secret.Mounts[0] != "/etc/creds" || secret.Mounts[1] != "+2 more" {
		t.Errorf("the mount paths did not arrive whole: %+v", secret.Mounts)
	}

	// a relation with nothing behind it is carried and marked inert. A
	// drawer that lists only linkable relations says a pod has three volumes when
	// it has five; one that links all of them gives two dead links.
	emptyDir := byRole["Volume"]
	if emptyDir.Label != "cache" {
		t.Fatalf("the emptyDir relation was dropped: %+v", page.Relations)
	}
	if emptyDir.Linkable {
		t.Error("an emptyDir arrived linkable; it would draw as a link that fails")
	}

	// A selector rather than one object: carried, inert, and the selector itself
	// travels so a client can open a filtered list.
	selects := byRole["Selects"]
	if selects.Linkable || selects.Selector != "app=api" {
		t.Errorf("the selector relation arrived linkable=%v selector=%q", selects.Linkable, selects.Selector)
	}

	// **Linkable is the server's answer.** It is asserted against
	// `detail.Relation.Linkable()` rather than against a copy of its rule, so a
	// third condition added there — a kind this cluster does not serve — reaches
	// the wire without this test having to be taught about it.
	for i, relation := range detailFixtureObject().Relations {
		if page.Relations[i].Linkable != relation.Linkable() {
			t.Errorf("relation %d: linkable=%v, the producer says %v",
				i, page.Relations[i].Linkable, relation.Linkable())
		}
	}

	// the capped list admits it.
	if page.RelationsNote == "" {
		t.Error("the relations note did not arrive; a capped list that does not admit it is a lie the operator acts on")
	}

	// --- what a pod is made of --------------------------------

	if len(page.Containers) != 2 {
		t.Fatalf("%d containers, want 2: %+v", len(page.Containers), page.Containers)
	}

	// Init first, and marked. The order is the order they run in, and the flag is
	// what stops an init container that will not start from reading as a crashing
	// app container - they look identical otherwise, and it is the reason the pod
	// is stuck.
	if !page.Containers[0].Init || page.Containers[0].Name != "wait-for-db" {
		t.Errorf("the init container is not first or not marked: %+v", page.Containers)
	}
	if page.Containers[1].Init {
		t.Errorf("the app container arrived marked init: %+v", page.Containers[1])
	}

	// The argv element by element, as the spec gives it.
	// Joined, `--name` `a b` and `--name a` `b` would arrive alike.
	waits := page.Containers[0]
	if len(waits.Command) != 2 || waits.Command[0] != "sh" || waits.Command[1] != "-c" {
		t.Errorf("the init container's command did not arrive element by element: %q", waits.Command)
	}
	if len(waits.Args) != 1 || !strings.Contains(waits.Args[0], "\n  sleep 2\n") {
		t.Errorf("the init container's script did not arrive whole, newlines and all: %q", waits.Args)
	}
	if got := page.Containers[1].Command; len(got) != 4 || got[3] != "a b" {
		t.Errorf("the app container's command lost an argument boundary: %q", got)
	}
	if page.Containers[1].Args != nil {
		t.Errorf("args arrived on a container that sets none: %q", page.Containers[1].Args)
	}

	app := page.Containers[1]
	if app.Image != "shop/api:1.4.2" || app.PullPolicy != "Always" {
		t.Errorf("image or pull policy did not arrive: %+v", app)
	}
	if len(app.Requests) != 1 || app.Requests[0].Name != "cpu" || app.Requests[0].Value != "100m" {
		t.Errorf("requests did not arrive: %+v", app.Requests)
	}
	if len(app.Limits) != 1 || app.Limits[0].Value != "512Mi" {
		t.Errorf("limits did not arrive: %+v", app.Limits)
	}

	// `env` and `imported` stay apart. Interleaving them makes what was
	// learned by reading another object look as knowable as what is written in
	// the pod spec, which is the one thing the producer says not to do.
	if len(app.Env) == 0 || len(app.Imported) == 0 {
		t.Fatalf("env=%d imported=%d; the two are separate members and both must arrive",
			len(app.Env), len(app.Imported))
	}
	if app.Imported[0].Name != "APP_TIMEOUT" {
		t.Errorf("the imported row lost its prefixed name: %+v", app.Imported[0])
	}

	// --- whether the containers are healthy --------------------------------

	// Three entries, always all three, in the drawn order - and the mapper does
	// not filter the absent ones out on the way. "No readiness probe" is a
	// finding, because a Service can be sending traffic to a container nothing
	// has ever asked whether it is ready, and a filtered list draws an empty area
	// there instead of that sentence.
	if len(app.Probes) != 3 {
		t.Fatalf("%d probes on the app container, want 3: %+v", len(app.Probes), app.Probes)
	}
	for at, want := range []string{"liveness", "readiness", "startup"} {
		if app.Probes[at].Kind != want {
			t.Errorf("probe %d arrived as %q, want %q; the order is what puts startup beside the other two",
				at, app.Probes[at].Kind, want)
		}
	}
	if got := app.Probes[0]; got.State != api.ProbeDeclared || got.Handler != "httpGet" ||
		got.Target != "/healthz:8080" {
		t.Errorf("the liveness declaration did not arrive: %+v", got)
	}
	// All four timings, because periodSeconds and failureThreshold are together
	// how long a container has to recover and neither means much alone.
	if got := app.Probes[0]; got.InitialDelaySeconds != 15 || got.PeriodSeconds != 10 ||
		got.TimeoutSeconds != 1 || got.FailureThreshold != 3 {
		t.Errorf("the liveness timings did not arrive: %+v", got)
	}
	// A declared probe and an absent one on the same container, which is what
	// makes the distinction measurable rather than theoretical.
	if app.Probes[2].State != api.ProbeAbsent || app.Probes[2].Problem != "" {
		t.Errorf("the absent startup probe arrived as %+v; not declared is not a failure to read", app.Probes[2])
	}

	// And the third state, on the init container: something is declared and this
	// console cannot say what. It must not arrive looking like the `absent` above.
	init := page.Containers[0]
	if len(init.Probes) != 3 {
		t.Fatalf("%d probes on the init container, want 3: %+v", len(init.Probes), init.Probes)
	}
	if init.Probes[2].State != api.ProbeUnreadable || init.Probes[2].Problem == "" {
		t.Errorf("an unreadable declaration arrived as %+v; with no problem nothing on the row tells it from an absence",
			init.Probes[2])
	}

	// The current state, with the word an operator searches for and the sentence
	// beside it rather than instead of it.
	if app.Status == nil {
		t.Fatal("the app container arrived with no status; a container nobody reported carries `unreported`, never nil")
	}
	if app.Status.State != api.ContainerWaiting || app.Status.Reason != "CrashLoopBackOff" ||
		app.Status.Restarts != 340 || app.Status.Ready {
		t.Errorf("the reported state did not arrive: %+v", app.Status)
	}
	if app.Status.Message == "" {
		t.Error("the kubelet's message was dropped on the way")
	}
	// Nothing reported is not "not ready". The two containers carry the two
	// answers, so a mapper that collapsed them would fail here rather than
	// producing a page where a Pending pod reads as a failing one.
	if init.Status == nil || init.Status.State != api.ContainerUnreported || init.Status.Problem == "" {
		t.Errorf("an unreported container arrived as %+v", init.Status)
	}

	env := map[string]api.EnvVar{}
	for _, row := range app.Env {
		env[row.Name] = row
	}

	// a ConfigMap's value is on the row already, a Secret's is not.
	//
	// "It is not a credential store... and a control that has to be pressed to
	// see a log level is one that gets pressed without reading the row." The two
	// halves are asserted together because the design is the pair: put every
	// value behind a control and the control stops being read; put none behind
	// one and a production credential is in the DOM of a window that gets
	// screenshotted.
	if config := env["NGINX_CONF"]; !config.Held || config.Resolved == "" || !config.Clipped {
		t.Errorf("a ConfigMap value did not arrive resolved: %+v", config)
	}
	// Named apart from the `Secret` relation above: two things in this test are a
	// secret and only one of them is an environment row.
	password := env["DB_PASSWORD"]
	if !password.Secret {
		t.Errorf("a Secret-backed row did not arrive marked: %+v", password)
	}
	if password.Resolved != "" || password.Held {
		t.Errorf("a Secret's value is on the wire: %+v", password)
	}
	// And the address the reveal control needs, structured rather than a
	// sentence: building it out of a rendered phrase would mean parsing English.
	if password.Source == nil || password.Source.Key != "password" || password.Source.Name != "db-credentials" {
		t.Errorf("the reveal address did not arrive: %+v", password.Source)
	}

	// four absences, and the panel must be able to draw them apart.
	//
	// The pair that matters is `unread` against `expected`. `unread` keeps a
	// control because asking again may answer differently; `expected` is a
	// reference marked `optional: true` whose key is not set, which is the pod
	// working as written - a healthy Argo CD server pod carries thirty of them,
	// and a warning color on those is a color the operator stops seeing.
	if got := env["MISSING"].Absence; got != "unread" {
		t.Errorf("an unread absence arrived as %q", got)
	}
	if got := env["OPTIONAL"].Absence; got != "expected" {
		t.Errorf("an optional-and-unset absence arrived as %q", got)
	}
	// `optional` decides what the sentence means, so it travels with the source.
	// Without it the fragment would tell an operator who pressed Show on an
	// `optional: true` reference that their pod would not start.
	if env["OPTIONAL"].Source == nil || !env["OPTIONAL"].Source.Optional {
		t.Errorf("the reference's own optional flag did not arrive: %+v", env["OPTIONAL"].Source)
	}
	// A row with a value has no absence at all, which is what makes the four
	// meaningful.
	if got := env["LOG_LEVEL"].Absence; got != "" {
		t.Errorf("a row with a literal value arrived with absence %q", got)
	}

	// A source that cannot be followed is neither a value nor an absence: it is a
	// third thing, and it says what decides the value instead.
	if env["NODE_NAME"].From == "" {
		t.Errorf("a field reference lost its description: %+v", env["NODE_NAME"])
	}

	// `envFrom`: three answers look identical on a panel that only lists rows -
	// an object with no keys, an object this console may not read, and an object
	// nobody looked at. `keys` and `problem` are what tell them apart.
	if len(app.EnvFrom) != 2 {
		t.Fatalf("%d imports, want 2: %+v", len(app.EnvFrom), app.EnvFrom)
	}
	if app.EnvFrom[0].Keys != 4 || app.EnvFrom[0].Prefix != "APP_" {
		t.Errorf("a read import lost its count or its prefix: %+v", app.EnvFrom[0])
	}
	if app.EnvFrom[1].Problem == "" || app.EnvFrom[1].Keys != 0 {
		t.Errorf("a refused import did not arrive as a refusal: %+v", app.EnvFrom[1])
	}
	// And the kind of the refusal with it: without it the panel cannot tell a
	// refused import from an optional one whose object does not exist, and draws
	// both alike.
	if app.EnvFrom[1].Absence != "unread" {
		t.Errorf("a refused import arrived with absence %q, want unread", app.EnvFrom[1].Absence)
	}

	// And the cap admits itself, for the same reason RelationsNote does.
	if page.EnvNote == "" {
		t.Error("the env note did not arrive; expanding an import costs a round trip and the number of them is capped")
	}

	// --- what the TLS material says ------------------------------

	certificates := page.Certificates
	if certificates == nil || len(certificates.Bundles) != 2 {
		t.Fatalf("the certificate set did not arrive: %+v", certificates)
	}

	// One card per certificate and not per key. `tls.crt` routinely holds a leaf
	// plus its intermediates, and rendering two certificates as one is a wrong
	// answer that looks right.
	leaf := certificates.Bundles[0]
	if leaf.Key != "tls.crt" || len(leaf.Certificates) != 2 {
		t.Fatalf("the bundle did not arrive whole: %+v", leaf)
	}
	if leaf.Certificates[0].Position != 1 || leaf.Certificates[1].Position != 2 {
		t.Errorf("the positions did not arrive: %+v", leaf.Certificates)
	}
	// Role is read off the certificate and not off its position: the second block
	// here is a CA that did not sign itself, so it is an intermediate whatever
	// order it is stored in.
	if leaf.Certificates[0].Role != "leaf" || leaf.Certificates[1].Role != "intermediate" {
		t.Errorf("the roles did not arrive: %+v", leaf.Certificates)
	}
	// Graded against the certificate's own lifetime by the producer, and carried
	// rather than recomputed: 30 days left is healthy on a 90-day certificate and
	// three months late on a one-year one, and a client with only notAfter cannot
	// tell those apart.
	if leaf.Certificates[0].ExpiryTone != "ok" || leaf.Certificates[0].Expiry == "" {
		t.Errorf("the expiry grade did not arrive: %+v", leaf.Certificates[0])
	}
	// The list an operator matches against the Ingress host they are debugging,
	// and the note that says it was cut.
	if len(leaf.Certificates[0].SANs) != 2 || leaf.Certificates[0].SANsNote == "" {
		t.Errorf("the SANs did not arrive whole: %+v", leaf.Certificates[0])
	}

	// a bundle that could not be read says so **beside** the one that
	// could, never instead of it. `certdrawer_test.go:189` pins the same rule one
	// level down - a bundle whose second block did not parse says so beside the
	// first.
	broken := certificates.Bundles[1]
	if broken.Note == "" || len(broken.Certificates) != 0 {
		t.Errorf("the unreadable bundle did not arrive as a refusal: %+v", broken)
	}
	if len(certificates.Bundles[0].Certificates) == 0 {
		t.Error("the readable bundle was lost with the unreadable one")
	}

	// Names and never values. It is the difference between a console that cannot
	// read the private key and one that chose not to.
	if len(certificates.PrivateKeys) != 1 || certificates.PrivateKeys[0] != "tls.key" {
		t.Errorf("the private-key provenance did not arrive: %+v", certificates.PrivateKeys)
	}

	// --- the addresses -------------------------------------------------

	if len(page.URLs) != 1 {
		t.Fatalf("%d addresses, want 1: %+v", len(page.URLs), page.URLs)
	}
	address := page.URLs[0]
	// Nothing is composed here: `address` is what the producer decided, scheme
	// included, and `tls` is the only reason it ever says https.
	if address.Address != "https://shop.example/api" || !address.TLS {
		t.Errorf("the address did not arrive as the producer built it: %+v", address)
	}
	// The path exactly as the object writes it, pattern and all, with a note
	// saying that only its literal beginning is opened.
	if address.Path == "" || address.Note == "" {
		t.Errorf("the path or its qualification was dropped: %+v", address)
	}
	if page.URLsNote == "" {
		t.Error("the addresses note did not arrive")
	}

	// --- what is deliberately absent ----------------------------------------

	// `actions` is not guessed: whether a verb is permitted is
	// one internal/edit Locate per verb, and answering it from the kind alone
	// would put "permitted: true" on a drawer for a cluster the read-only guard
	// refuses. Asserted rather than left unmentioned, so the day somebody fills
	// it this test is updated.
	if page.Actions != nil {
		t.Errorf("actions arrived (%+v); a wrong verb is a button that fails after the click", page.Actions)
	}
}

// detailFixtureObject is what internal/detail produces, with one instance of
// every member this mapper reads.
//
// Spread across several facts and several relations deliberately: `pill` belongs
// to a status, `prose` to a condition message, `selector` to a relation that is
// not one object, and a single fact or relation carrying all of them at once is
// one that cannot exist.
func detailFixtureObject() detail.Object {
	return detail.Object{
		Cluster: "prod", Kind: "pods", Title: "Pod", Name: "api-1",
		Namespace: "shop-prod", Namespaced: true,
		Facts: []detail.Fact{
			{Label: "Status", Value: "Running", Class: "ok", Pill: true},
			{Label: "Node", Value: "node-3"},
			{
				Label:    "PodScheduled",
				Value:    "0/6 nodes are available: 6 Insufficient cpu. preemption: 0/6 nodes are available.",
				Sentence: true,
			},
		},
		Labels: map[string]string{"app": "api", "standalone": ""},
		// **Already treated**, which is the only shape internal/detail ever hands
		// over: `last-applied-configuration` is gone before this struct exists and
		// the note says so. A fixture carrying the raw map would ask this mapper to
		// do a job it must not do — the treatment is the producer's, so that a
		// second client cannot get it wrong.
		Annotations: map[string]string{
			"deployment.kubernetes.io/revision": "7",
			"meta.helm.sh/release-name":         "shop",
		},
		// What the cluster still remembers, and the sentence beside it. Both,
		// because a list can be short *and* capped by Kubernetes' own hour, and a
		// mapping that carried one of the two would have to choose which half to
		// drop.
		Events: []detail.Event{{
			Type: "Warning", Reason: "BackOff", From: "kubelet on node-3",
			Message: "Back-off restarting failed container api in pod api-1_shop-prod(8f2c)",
			Count:   340,
			First:   time.Date(2026, 9, 12, 9, 12, 0, 0, time.UTC),
			Last:    time.Date(2026, 9, 12, 11, 48, 0, 0, time.UTC),
		}, {
			// One with no instants at all: the source gave none, and a zero
			// time.Time on the wire is a date a client would draw.
			Type: "Normal", Reason: "Scheduled", Message: "Successfully assigned to node-3",
		}},
		EventsNote: "Kubernetes keeps about an hour of these; older ones are gone from the cluster.",
		Containers: []detail.Container{
			// Nothing declared and nothing reported, which is what every container
			// of a Pending pod looks like. The three probe entries exist and say
			// `absent`; the status says `unreported`. Both are absences the mapper
			// has to carry rather than tidy away, and both sit on this container
			// so that the one below can carry the opposite of each.
			{
				Name: "wait-for-db", Image: "busybox:1.36", Init: true,
				// A script after `sh -c`, newlines and quotes in it: one
				// element, and it must arrive as one.
				Command: []string{"sh", "-c"},
				Args:    []string{"until nc -z db 5432; do\n  echo \"waiting for db\"\n  sleep 2\ndone"},
				Probes: []detail.Probe{
					{Kind: "liveness", State: detail.ProbeAbsent},
					{Kind: "readiness", State: detail.ProbeAbsent},
					{Kind: "startup", State: detail.ProbeUnreadable,
						Problem: "this startup probe uses a handler this console does not recognize"},
				},
				Status: &detail.ContainerStatus{
					State:   detail.ContainerUnreported,
					Problem: "the kubelet has reported nothing about this container yet",
				},
			},
			{
				Name: "api", Image: "shop/api:1.4.2", PullPolicy: "Always",
				Command:  []string{"/app/api", "--listen=:8080", "--name", "a b"},
				Requests: []detail.Amount{{Name: "cpu", Value: "100m"}},
				Limits:   []detail.Amount{{Name: "memory", Value: "512Mi"}},
				Env: []detail.EnvVar{
					{Name: "LOG_LEVEL", Value: "info"},
					{Name: "NODE_NAME", From: "field spec.nodeName"},
					{Name: "DB_PASSWORD", Secret: true,
						Source: &detail.EnvSource{Kind: "Secret", Name: "db-credentials", Key: "password"}},
					{Name: "NGINX_CONF", Held: true, Clipped: true, Resolved: "worker_processes auto;",
						Source: &detail.EnvSource{Kind: "ConfigMap", Name: "nginx", Key: "nginx.conf"}},
					{Name: "MISSING", Problem: "not read within this panel's budget",
						Absence: detail.AbsenceUnread,
						Source:  &detail.EnvSource{Kind: "ConfigMap", Name: "extras", Key: "missing"}},
					{Name: "OPTIONAL", Problem: "the ConfigMap does not carry that key, and the reference is optional",
						Absence: detail.AbsenceExpected,
						Source:  &detail.EnvSource{Kind: "ConfigMap", Name: "params", Key: "tuning", Optional: true}},
				},
				EnvFrom: []detail.Whole{
					{Kind: "ConfigMap", Name: "app-settings", Prefix: "APP_", Keys: 4},
					{Kind: "Secret", Name: "unreadable", Optional: true,
						Problem: "this account may not read that Secret", Absence: detail.AbsenceUnread},
				},
				Imported: []detail.EnvVar{{Name: "APP_TIMEOUT", Held: true, Resolved: "30s",
					Source: &detail.EnvSource{Kind: "ConfigMap", Name: "app-settings", Key: "TIMEOUT"}}},
				Probes: []detail.Probe{
					{
						Kind: "liveness", State: detail.ProbeDeclared, Handler: "httpGet",
						Target: "/healthz:8080", InitialDelaySeconds: 15, PeriodSeconds: 10,
						TimeoutSeconds: 1, FailureThreshold: 3,
					},
					{
						Kind: "readiness", State: detail.ProbeDeclared, Handler: "tcpSocket",
						Target: ":8080", PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 2,
					},
					{Kind: "startup", State: detail.ProbeAbsent},
				},
				Status: &detail.ContainerStatus{
					State: detail.ContainerWaiting, Ready: false, Reason: "CrashLoopBackOff",
					Message: "back-off 5m0s restarting failed container api", Restarts: 340,
				},
			},
		},
		EnvNote: "2 of 5 envFrom imports were not read: this panel stops after 3.",
		Certificates: &detail.CertificateSet{
			Bundles: []detail.CertificateBundle{
				{Key: "tls.crt", Certificates: []detail.Certificate{
					{
						Position: 1, Role: "leaf", Subject: "CN=shop.example", Issuer: "CN=R3",
						SANs: []string{"shop.example", "www.shop.example"}, SANsNote: "3 more",
						NotBefore: moment, NotAfter: moment.Add(61 * 24 * time.Hour),
						Expiry: "expires in 61d4h", ExpiryClass: "ok",
						Serial: "03:9a:1f", SignatureAlgorithm: "SHA256-RSA", PublicKey: "RSA 2048",
					},
					{Position: 2, Role: "intermediate", Subject: "CN=R3", Issuer: "CN=ISRG Root X1",
						IsCA: true, NotBefore: moment, NotAfter: moment.Add(365 * 24 * time.Hour),
						Expiry: "expires in 365d", ExpiryClass: "ok"},
				}},
				{Key: "ca.crt", Note: "the second PEM block is a certificate request, not a certificate"},
			},
			PrivateKeys: []string{"tls.key"},
		},
		URLs: []detail.URL{{
			Role: "Ingress", Address: "https://shop.example/api", Host: "shop.example",
			Path: "/api(/|$)(.*)", TLS: true, Service: "api",
			Note: "the path is a pattern; only its literal beginning is opened",
		}},
		URLsNote: "1 rule points at a resource backend and has no address.",
		Relations: []detail.Relation{
			{Role: "Controlled by", Kind: "replicasets", Namespace: "shop-prod", Name: "api-7c9",
				Label: "api-7c9", Detail: "Running on node-3"},
			{Role: "Secret", Kind: "secrets", Namespace: "shop-prod", Name: "db-credentials",
				Label: "db-credentials", Mounts: []string{"/etc/creds", "+2 more"}},
			// No object behind it. Linkable() is false and nothing here says so.
			{Role: "Volume", Label: "cache", Detail: "emptyDir"},
			// A selector: a kind and no name.
			{Role: "Selects", Kind: "pods", Selector: "app=api", Label: "app=api"},
		},
		RelationsNote: "12 of 40 volumes shown.",
	}
}

// The one state `CertificateSet.note` exists for, which no page carrying a
// bundle can be in.
//
// The producer is explicit that the two are exclusive - the note is "why there
// is no bundle at all", and "empty when Bundles is filled" - so the member-set
// test in internal/api excepts it and points here. A Secret that declares
// `type: kubernetes.io/tls` and carries no `tls.crt` is the case: it is not an
// object with no certificates, it is an object that promised one.
func TestACertificateSetWithNoBundleSaysWhy(t *testing.T) {
	object := detailFixtureObject()
	object.Certificates = &detail.CertificateSet{
		Note: "this Secret is typed kubernetes.io/tls and carries no tls.crt",
	}

	page := detailPageOf(api.ObjectRef{Cluster: "prod", Kind: "secrets", Name: "tls"}, object)

	if page.Certificates == nil {
		t.Fatal("the certificate set was dropped; an object that promised a certificate and has none is not an object with no certificates")
	}
	if page.Certificates.Note == "" {
		t.Errorf("the note did not arrive: %+v", page.Certificates)
	}
	if len(page.Certificates.Bundles) != 0 {
		t.Errorf("a bundle arrived beside the note: %+v", page.Certificates)
	}
}

// The ports an object exposes, and the judgment the client must not make.
//
// `detail.Object.Ports` is filled by `internal/detail` for a pod and a Service
// and must be mapped for both.
//
// `Forwardable` is the load-bearing member. Port-forwarding is TCP; deriving
// that in the browser would be a second copy of a rule this server enforces,
// and the copy that offers a button the server then refuses.
func TestThePortsCarryWhetherEachCanBeForwarded(t *testing.T) {
	object := detailFixtureObject()
	object.Ports = []detail.Port{
		{Name: "http", Port: 8080, Protocol: "TCP", Container: "api"},
		{Name: "metrics", Port: 9125, Protocol: "UDP", Container: "api"},
		// An unstated protocol is TCP, which is what Kubernetes does: the field
		// defaults, and an object that omits it exposes a TCP port.
		{Port: 9000},
	}
	object.PortsNote = "Only the ports the object declares are listed."

	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "api-1"},
		object,
	)
	if len(page.Ports) != 3 {
		t.Fatalf("%d ports arrived, want 3", len(page.Ports))
	}
	if !page.Ports[0].Forwardable || page.Ports[0].Container != "api" {
		t.Errorf("a TCP port did not arrive forwardable: %+v", page.Ports[0])
	}
	if page.Ports[1].Forwardable {
		t.Error("a UDP port arrived forwardable; the button would fail after the click")
	}
	if page.Ports[1].Reason == nil || !strings.Contains(page.Ports[1].Reason.Message, "TCP") {
		t.Errorf("a refused port carries no reason: %+v", page.Ports[1])
	}
	if !page.Ports[2].Forwardable {
		t.Error("an unstated protocol was not read as TCP, which is the API's own default")
	}
	if page.PortsNote == "" {
		t.Error("the note did not arrive")
	}
}

// Every declared port, from every container, each row saying whose it is.
//
// `portsFromPodSpec` walks `containers` and `initContainers` and `portsOf`
// passes every row through, container name and protocol included. This test
// pins what the panel relies on -
// two containers on one pod are two rows, told apart by `container`, and a UDP
// port is listed and refused with its reason rather than left out - so that a
// later "tidy" which drops a container's ports fails here, not in a review.
func TestEveryContainerPortReachesThePanel(t *testing.T) {
	object := detailFixtureObject()
	object.Ports = []detail.Port{
		{Name: "http", Port: 8080, Protocol: "TCP", Container: "api"},
		// The mesh sidecar: an init container with `restartPolicy: Always`,
		// which is exactly the thing that listens.
		{Name: "http-envoy-prom", Port: 15090, Protocol: "TCP", Container: "istio-proxy"},
		{Name: "statsd", Port: 9125, Protocol: "UDP", Container: "istio-proxy"},
	}

	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "api-1"},
		object,
	)
	if len(page.Ports) != 3 {
		t.Fatalf("%d rows arrived for 3 declared ports: %+v", len(page.Ports), page.Ports)
	}
	containers := make([]string, 0, len(page.Ports))
	for _, port := range page.Ports {
		containers = append(containers, port.Container)
		if port.Protocol == "" {
			t.Errorf("port %d arrived without its protocol", port.Port)
		}
	}
	if got := strings.Join(containers, ","); got != "api,istio-proxy,istio-proxy" {
		t.Errorf("containers = %s; a row that does not say whose port it is reads as a duplicate", got)
	}
	if !page.Ports[0].Forwardable || !page.Ports[1].Forwardable {
		t.Errorf("a TCP port did not arrive forwardable: %+v", page.Ports[:2])
	}
	udp := page.Ports[2]
	if udp.Forwardable || udp.Reason == nil || !strings.Contains(udp.Reason.Message, "UDP") {
		t.Errorf("the UDP port is listed but not refused with its reason: %+v", udp)
	}
}

// A kind that is neither a pod nor a Service cannot be forwarded to at all, and
// says which of the two doors it is not.
func TestAPortOnSomethingElseIsNotForwardable(t *testing.T) {
	object := detailFixtureObject()
	object.Ports = []detail.Port{{Port: 8080, Protocol: "TCP"}}

	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "endpoints", Namespace: "shop-prod", Name: "api"},
		object,
	)
	if len(page.Ports) != 1 || page.Ports[0].Forwardable {
		t.Fatalf("ports = %+v", page.Ports)
	}
	if page.Ports[0].Reason == nil || !strings.Contains(page.Ports[0].Reason.Message, "pod or a service") {
		t.Errorf("the reason does not say where a forward can be opened: %+v", page.Ports[0])
	}
}

// What an Event says, on the one kind where the object is a sentence.
//
// `detail.Object.Event` is built and parsed - both API shapes read as one - and
// the mapper must carry it, not only the plural. Otherwise an Event opened in
// the drawer would show its Kind, its API version, its generated name and when
// it was created: everything about the object except the message it exists to
// carry.
func TestAnEventCarriesTheSentenceItExistsFor(t *testing.T) {
	object := detailFixtureObject()
	object.Event = &detail.EventDetail{
		Message: "Back-off pulling image", Reason: "BackOff", Type: "Warning",
		Action: "Pulling", Source: "kubelet", Node: "node-3", Count: 47,
		First:      time.Date(2026, 9, 12, 9, 12, 0, 0, time.UTC),
		Last:       time.Date(2026, 9, 12, 11, 48, 0, 0, time.UTC),
		ObjectKind: "Pod", ObjectName: "api-7d9f",
		ObjectField: "spec.containers{istio-proxy}",
	}

	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "events", Namespace: "shop-prod", Name: "api.17f"},
		object,
	)
	if page.Event == nil {
		t.Fatal("the event did not arrive; the panel then shows everything but the message")
	}
	if page.Event.Message != "Back-off pulling image" {
		t.Errorf("message = %q", page.Event.Message)
	}
	// Composed by the producer, which already handles a reporter that named one
	// half and not the other. A client joining them would be a second spelling.
	if page.Event.Object != "Pod/api-7d9f" {
		t.Errorf("object = %q, want the composed form", page.Event.Object)
	}
	// On a pod with a sidecar this is the difference between "the app failed"
	// and "the mesh failed".
	if page.Event.ObjectField != "spec.containers{istio-proxy}" {
		t.Errorf("the field path did not arrive: %q", page.Event.ObjectField)
	}
	// Both instants: a two-day repeat and a burst of forty-seven in a minute are
	// different facts, and only the pair says which.
	if page.Event.First == nil || page.Event.Last == nil || !page.Event.First.Before(*page.Event.Last) {
		t.Errorf("the span did not arrive: %+v", page.Event)
	}
}

// And every other kind carries nothing, rather than an empty object a panel
// would draw a heading over.
func TestAnObjectThatIsNotAnEventCarriesNoSentence(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "api-1"},
		detailFixtureObject(),
	)
	if page.Event != nil {
		t.Fatalf("a pod carried an event subject: %+v", page.Event)
	}
}

// A table that measures nothing says so, on the kinds that measure.
//
// The collapse it closes is stated in `liveUsage`'s own comment: a cluster with
// no metrics-server, a session that expired an hour ago and a Prometheus that
// will not answer all arrive as a nil map, all render one em dash, and "a table
// cell has no room to say which it was". The page then said nothing at all, so
// a reader had a column of dashes and no thread to pull.
//
// It names no cause, and that is the same comment's other half: the metrics
// diagnostic is where that is answered, because it says which of three
// different people fixes it. The way there is the client's to draw, as a link,
// on `unmeasured` and never on `pending`.
func TestAKindThatMeasuresNothingSaysSo(t *testing.T) {
	reads := apiReads{&Server{}}

	// A kind with gauges and no backend that could ever fill them: pods have no
	// second source, so nothing is asked and the answer is already in.
	state, note := reads.measurement(context.Background(), api.ListQuery{Cluster: "prod"}, "pods")
	if state != api.MeasurementUnmeasured || note == "" {
		t.Fatalf("a kind with empty gauges and nothing to ask says %q / %q; want unmeasured with a sentence", state, note)
	}
	// And it does not invent a cause.
	for _, guess := range []string{"metrics-server", "Prometheus", "expired", "refused it"} {
		if strings.Contains(note, guess) {
			t.Errorf("the note guesses a cause (%q): %q", guess, note)
		}
	}

	// A kind with no gauges has nothing to explain, and a note there would be a
	// sentence about a column that is not on the table.
	if state, note := reads.measurement(context.Background(), api.ListQuery{Cluster: "prod"}, "configmaps"); state != "" || note != "" {
		t.Errorf("a kind with no gauges carries a measurement %q / %q", state, note)
	}
}

// A read on its way is not a read that gave nothing.
//
// The Nodes list must not draw "No measurements for this kind right now...
// Health says which backend is missing or refusing" while the first read is in
// flight. `usageFor` never blocks, so the first answer goes out before its
// figures, and that sentence would send the operator to diagnose a backend that
// is answering. Pending is drawn as pending.
func TestAFirstReadOnItsWayIsPendingAndNotUnmeasured(t *testing.T) {
	ctx := context.Background()
	q := api.ListQuery{Cluster: "prod"}

	t.Run("the first render, with the read started behind it, is pending", func(t *testing.T) {
		reads := apiReads{&Server{}}
		reads.s.usageHeld = map[string]*usageEntry{
			// The fetch the rows' own call started, and that has not come back.
			"prod|nodes": {fetching: true},
		}
		state, note := reads.measurement(ctx, q, "nodes")
		if state != api.MeasurementPending {
			t.Fatalf("a first read in flight answers %q (%q); it is pending, and nothing about it is missing", state, note)
		}
		if note == "" {
			t.Error("pending carries no sentence; a column of dashes with nothing beside it is the collapse")
		}
		if strings.Contains(note, "Health") || strings.Contains(note, "missing") || strings.Contains(note, "refusing") {
			t.Errorf("a pending read points at a fault: %q", note)
		}
		// The read runs
		// behind the rows on its own bound and can outlast a tick, so the note
		// promises the figures when the read returns and not on a refresh.
		if strings.Contains(note, "next refresh") || !strings.Contains(note, "when that read returns") {
			t.Errorf("a pending read promises a timing it cannot keep: %q", note)
		}
	})

	t.Run("a read that came back with nothing is unmeasured, even while the next is in flight", func(t *testing.T) {
		reads := apiReads{&Server{}}
		reads.s.usageHeld = map[string]*usageEntry{
			// Tried a moment ago, gave nothing, and is being asked again. The nil
			// is the last attempt's answer, not a read that has not happened.
			"prod|nodes": {fetching: true, tried: time.Now().Add(-2 * usageTTL)},
		}
		if state, note := reads.measurement(ctx, q, "nodes"); state != api.MeasurementUnmeasured {
			t.Fatalf("an answered read with no figures says %q (%q); want unmeasured", state, note)
		}
	})

	t.Run("figures held mean nothing to say", func(t *testing.T) {
		reads := apiReads{&Server{}}
		now := time.Now()
		reads.s.usageHeld = map[string]*usageEntry{
			"prod|nodes": {usage: map[string]resources.Usage{"desktop-control-plane": {CPUMillicores: 230}}, at: now, tried: now},
		}
		if state, note := reads.measurement(ctx, q, "nodes"); state != "" || note != "" {
			t.Errorf("a table with its gauges filled carries %q / %q", state, note)
		}
	})

	t.Run("an empty cache starts the read and says it is pending, then says what came back", func(t *testing.T) {
		// No metrics backend at all: a node's Disk gauge still sends the read out
		// (needsNoMetricsBackend), so the first answer is a read in flight.
		reads := apiReads{&Server{}}
		if state, _ := reads.measurement(ctx, q, "nodes"); state != api.MeasurementPending {
			t.Fatalf("the very first render says %q; the read it started has not come back", state)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			reads.s.usageMu.Lock()
			held := reads.s.usageHeld["prod|nodes"]
			done := held != nil && !held.tried.IsZero()
			reads.s.usageMu.Unlock()
			if done {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the read never came back")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if state, _ := reads.measurement(ctx, q, "nodes"); state != api.MeasurementUnmeasured {
			t.Fatalf("after a read that gave nothing the table says %q; want unmeasured", state)
		}
	})
}

// A Secret's Details tab lists no key. The keys are the Data tab's.
//
// A second section, "Data", built from `object.Data.Entries` - one field per
// key, each a Secret's marked `masked` and drawn as *held back* - would
// duplicate the Data tab, which draws the same keys with Show and Edit beside
// each. `sectionsOf` maps the facts and nothing else; what
// the tab says *about* a Secret — type, key count, immutability — arrives as
// facts from `internal/detail`, which is where `TestASecretsFactsDescribeThe
// ObjectAndNeverListItsKeys` pins it.
func TestASecretsDetailsCarryNoKey(t *testing.T) {
	object := detail.Object{
		Kind: "secrets", Title: "Secret", Name: "db", Namespace: "shop",
		Facts: []detail.Fact{
			{Label: "Kind", Value: "Secret"},
			{Label: "Type", Value: "Opaque"},
			{Label: "Keys", Value: "2"},
		},
		Data: &detail.DataSet{
			Kind: "Secret", Secret: true, Type: "Opaque",
			Entries: []detail.DataEntry{
				{Key: "password", Field: detail.FieldData, Secret: true, Encoded: true},
				{Key: "username", Field: detail.FieldData, Secret: true, Encoded: true},
			},
		},
	}

	page := detailPageOf(api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"}, object)

	if len(page.Sections) != 1 || page.Sections[0].Key != "summary" {
		keys := make([]string, 0, len(page.Sections))
		for _, section := range page.Sections {
			keys = append(keys, section.Key)
		}
		t.Fatalf("sections = %v, want the summary alone: the key list is the Data tab's", keys)
	}
	for _, field := range page.Sections[0].Fields {
		if field.Label == "password" || field.Label == "username" {
			t.Errorf("a key is drawn as a fact on the Details tab: %+v", field)
		}
	}
	// And the facts that are about the object did arrive.
	labels := make([]string, 0, len(page.Sections[0].Fields))
	for _, field := range page.Sections[0].Fields {
		labels = append(labels, field.Label)
	}
	if strings.Join(labels, ",") != "Kind,Type,Keys" {
		t.Errorf("the facts arrived as %v", labels)
	}
}

// --- a Role's rules, and the two negative answers ------------------------------

// The panel's own mapping, driven straight at `ruleSetOf`. Separate from the
// page-wide test above because the fixture there is a pod: a Role is a different
// object and stacking its rules onto a pod's page would make that fixture a
// payload no producer produces.
func TestARolesRulesReachThePageGradedTheWayTheListGradesThem(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "clusterroles", Name: "admin"},
		detail.Object{
			Kind: "clusterroles", Title: "ClusterRole", Name: "admin",
			Rules: &detail.RuleSet{
				Rules: []detail.PolicyRule{
					{
						APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"},
						Wildcards: "api groups, resources, verbs", WildcardClass: "bad",
					},
					{NonResourceURLs: []string{"/healthz"}, Verbs: []string{"get"}},
				},
				Note: "A * is drawn as it is stored.",
			},
		},
	)

	if page.Rules == nil {
		t.Fatal("the page carries no rules; a Role's page is a fact table again")
	}
	if page.Rules.State != api.StateOK {
		t.Errorf("state = %q, want ok", page.Rules.State)
	}
	if len(page.Rules.Rules) != 2 {
		t.Fatalf("%d rules arrived, want 2: %+v", len(page.Rules.Rules), page.Rules.Rules)
	}
	// The grade travels rather than being re-derived by a client, which is what
	// keeps the panel and the Access Control grid from disagreeing about the same
	// Role. Through `toneOf`, so the tone stays inside the contract's enum.
	if page.Rules.Rules[0].WildcardTone != "bad" ||
		page.Rules.Rules[0].Wildcards != "api groups, resources, verbs" {
		t.Errorf("the wildcard grade did not arrive: %+v", page.Rules.Rules[0])
	}
	if page.Rules.Rules[1].WildcardTone != "" {
		t.Errorf("an ungraded rule arrived graded: %+v", page.Rules.Rules[1])
	}
	// A rule carrying only non-resource URLs keeps them and grows no resources
	// list, which is the case `ruleSummary` already handles for the list.
	if len(page.Rules.Rules[1].NonResourceURLs) != 1 || page.Rules.Rules[1].Resources != nil {
		t.Errorf("the non-resource rule was flattened: %+v", page.Rules.Rules[1])
	}
	if !strings.Contains(page.Rules.Note, "drawn as it is stored") {
		t.Errorf("the note did not arrive: %q", page.Rules.Note)
	}
	if page.Rules.Reason != nil {
		t.Errorf("a readable answer carries a reason: %+v", page.Rules.Reason)
	}
}

// The first negative answer, and it is about the object rather than about the
// read: a Role whose rules array is empty grants nothing, which is real and
// consequential. `empty` with no reason — nothing failed.
func TestARoleThatGrantsNothingArrivesEmptyAndNotAsAFailure(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "roles", Namespace: "shop", Name: "nothing"},
		detail.Object{Kind: "roles", Title: "Role", Name: "nothing", Rules: &detail.RuleSet{}},
	)

	if page.Rules == nil {
		t.Fatal("a Role that grants nothing carries no rules member; empty and absent now render the same")
	}
	if page.Rules.State != api.StateEmpty {
		t.Errorf("state = %q, want empty", page.Rules.State)
	}
	if page.Rules.Reason != nil {
		t.Errorf("nothing failed and a reason arrived anyway: %+v", page.Rules.Reason)
	}
	if len(page.Rules.Rules) != 0 {
		t.Errorf("rules arrived from nowhere: %+v", page.Rules.Rules)
	}
}

// The second, and the one `RuleSet.reason` exists for — it is excepted in
// internal/api's member test as exclusive with `rules`, and this is the half
// that can see it.
//
// **On a permissions surface these two must not collapse into each other.** The
// entire reason somebody opens this panel is to conclude *that is all this Role
// can do*, so a fault drawn as "grants nothing" is the worst available answer.
func TestARoleWhoseRulesCannotBeReadRefusesRatherThanSayingItGrantsNothing(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "roles", Namespace: "shop", Name: "broken"},
		detail.Object{
			Kind: "roles", Title: "Role", Name: "broken",
			Rules: &detail.RuleSet{Unreadable: "rule 1 of this object is not a rule"},
		},
	)

	if page.Rules == nil {
		t.Fatal("no rules member at all")
	}
	if page.Rules.State != api.StateUnreadable {
		t.Fatalf("state = %q, want unreadable — an empty answer and a failed answer are different answers",
			page.Rules.State)
	}
	if page.Rules.Reason == nil || page.Rules.Reason.Code != api.CodeUndecodable {
		t.Fatalf("the refusal carries no code a client can branch on: %+v", page.Rules.Reason)
	}
	// The detail is what an operator needs to report it, and it is the producer's
	// sentence rather than one written here.
	if !strings.Contains(page.Rules.Reason.Detail, "not a rule") {
		t.Errorf("the reason says nothing about what went wrong: %+v", page.Rules.Reason)
	}
	if len(page.Rules.Rules) != 0 {
		t.Errorf("a refused read carries rules as well: %+v", page.Rules.Rules)
	}
}

// And nothing of this reaches a kind that has no rules. A pod's page carries no
// rules member, so a client has nothing to draw a heading over.
func TestAKindWithNoRulesCarriesNoRulesMember(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "api-1"},
		detailFixtureObject(),
	)
	if page.Rules != nil {
		t.Errorf("a pod's page carries a rules member: %+v", page.Rules)
	}
}
