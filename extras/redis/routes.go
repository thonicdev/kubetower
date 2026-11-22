package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/svcgroup"
)

// The JSON the pages read. web/app/extras/redis/data.ts declares the same
// shapes, and every failure is a problem with a code the page branches on,
// never a sentence it parses.

// --- GET / : the inventory, which dials nothing -------------------------------

type inventoryAnswer struct {
	// Scope is the `?ns=` the inventory was read for, empty for the cluster.
	Scope string `json:"scope"`
	// Complete is false when the Services listing stopped before its end: a
	// narrowed answer, and Note says why. An empty list with Complete false is
	// not "no Redis here".
	Complete   bool   `json:"complete"`
	Note       string `json:"note,omitempty"`
	SlicesNote string `json:"slicesNote,omitempty"`
	// PodsNotes say which namespaces were grouped without their pods.
	PodsNotes []string `json:"podsNotes,omitempty"`
	// Redises counts deployments, Members the servers or pod sets inside
	// them, Services the doors; most charts make the three differ.
	Services   int             `json:"services"`
	Members    int             `json:"members"`
	Redises    int             `json:"redises"`
	Namespaces []namespaceWire `json:"namespaces"`
}

type namespaceWire struct {
	Name        string           `json:"name"`
	Deployments []deploymentWire `json:"deployments"`
}

// deploymentWire is one installation of a Redis or a Valkey: its members, and
// what they were folded on (group.go).
type deploymentWire struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Evidence is what the members were folded on, most trusted first:
	// `workload`, `pods`, `release`, `name`. Empty for one member. A fold on
	// `name` alone is a hint, and the page says so.
	Evidence  []string     `json:"evidence"`
	Release   string       `json:"release,omitempty"`
	Chart     string       `json:"chart,omitempty"`
	Workloads []string     `json:"workloads"`
	Members   []memberWire `json:"members"`
}

// memberWire is one backend inside a deployment, with its role.
type memberWire struct {
	backendWire
	// Role is primary, replica, headless, sentinel or empty; RoleFrom is
	// labels, pods, spec, name or port. The page puts the server's own
	// answer in its place once the member is probed.
	Role     string `json:"role,omitempty"`
	RoleFrom string `json:"roleFrom,omitempty"`
	// Sentinel marks a member reached on a Sentinel port: drawn, never
	// opened or probed.
	Sentinel  bool     `json:"sentinel,omitempty"`
	Workloads []string `json:"workloads"`
}

// backendWire is one actual Redis and every Service that reaches it.
type backendWire struct {
	Key string `json:"key"`
	// Pods is what is running behind it, and it is the row's real identity -
	// the Services are the doors.
	Pods []string `json:"pods"`
	// Behind is the sentence: a pod, "N pods: ...", nothing, or not read.
	Behind string `json:"behind"`
	Ready  int    `json:"ready"`
	// Reachable is false when nothing is behind, or when what is behind could
	// not be read; Endpointless says which.
	Reachable    bool           `json:"reachable"`
	Endpointless bool           `json:"endpointless"`
	Doors        []instanceWire `json:"doors"`
}

type instanceWire struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
	PortName  string `json:"portName,omitempty"`
	// Why is what made this look like a Redis, carried to every page it is on.
	Why string `json:"why"`
	// Role is what this Service says of the pods behind it, and RoleFrom on
	// what evidence; inventory only.
	Role     string `json:"role,omitempty"`
	RoleFrom string `json:"roleFrom,omitempty"`
}

func wireBackend(backend svcgroup.Backend) backendWire {
	row := backendWire{
		Key: backend.Key, Pods: backend.Pods, Behind: svcgroup.Behind(backend), Ready: backend.Ready,
		Reachable: len(backend.Pods) > 0, Endpointless: backend.Endpointless, Doors: []instanceWire{},
	}
	if row.Pods == nil {
		row.Pods = []string{}
	}
	for _, door := range backend.Services {
		row.Doors = append(row.Doors, wireInstance(door))
	}
	return row
}

func wireInstance(i svcgroup.Instance) instanceWire {
	return instanceWire{Namespace: i.Namespace, Service: i.Service, Port: i.Port, PortName: i.PortName, Why: i.Why}
}

func wireDeployment(d deployment) deploymentWire {
	out := deploymentWire{Name: d.Name, Evidence: d.Evidence, Release: d.Release, Chart: d.Chart,
		Workloads: d.Workloads, Members: []memberWire{}}
	if out.Evidence == nil {
		out.Evidence = []string{}
	}
	if out.Workloads == nil {
		out.Workloads = []string{}
	}
	var keys []string
	for _, m := range d.Members {
		row := memberWire{backendWire: wireBackend(m.Backend), Role: m.Role, RoleFrom: m.RoleFrom,
			Sentinel: m.Sentinel, Workloads: m.Workloads}
		if row.Workloads == nil {
			row.Workloads = []string{}
		}
		for i := range row.Doors {
			if i < len(m.Doors) {
				row.Doors[i].Role, row.Doors[i].RoleFrom = m.Doors[i].Role, m.Doors[i].From
			}
		}
		out.Members = append(out.Members, row)
		keys = append(keys, m.Backend.Key)
	}
	// The key is the members' keys: stable for one cluster, and different the
	// moment the fold changes.
	out.Key = d.Namespace + "\x00deployment=" + strings.Join(keys, "|")
	return out
}

// deployments is the fold the inventory draws: discovery's backends, the pods
// behind them, and group.go.
func deployments(r *http.Request, h extension.Host, cluster string, found discovery) ([]deployment, []string) {
	pods := readPods(r, h, cluster, found)
	return assemble(groupInput{
		services:  found.Services,
		pods:      pods.Pods,
		redis:     svcgroup.Group(found.Instances),
		sentinels: svcgroup.Group(found.Sentinels),
	}), pods.Notes
}

func (Redis) inventory(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		scope := strings.TrimSpace(r.URL.Query().Get("ns"))

		found, err := discover(r, h, cluster, scope)
		if err != nil {
			writeListFailure(w, cluster, err)
			return
		}
		out := inventoryAnswer{
			Scope: scope, Complete: found.Complete, Note: found.Note, SlicesNote: found.SlicesNote,
			Services: len(found.Instances), Namespaces: []namespaceWire{},
		}
		folded, notes := deployments(r, h, cluster, found)
		out.PodsNotes = notes
		byNamespace := map[string]*namespaceWire{}
		var order []*namespaceWire
		for _, d := range folded {
			out.Redises++
			row := wireDeployment(d)
			for _, m := range row.Members {
				if !m.Sentinel {
					out.Members++
				}
			}
			group := byNamespace[d.Namespace]
			if group == nil {
				group = &namespaceWire{Name: d.Namespace}
				byNamespace[d.Namespace] = group
				order = append(order, group)
			}
			group.Deployments = append(group.Deployments, row)
		}
		sort.Slice(order, func(i, j int) bool { return order[i].Name < order[j].Name })
		for _, group := range order {
			out.Namespaces = append(out.Namespaces, *group)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// --- GET /services/{namespace}/{name} : the panel tab, which dials nothing ----

// serviceAnswer is what the tab on a Service's panel draws: the deployment the
// Service belongs to, every member of it and every door onto each, with the
// reason each was offered and its role.
type serviceAnswer struct {
	Namespace   string           `json:"namespace"`
	Service     string           `json:"service"`
	Deployments []deploymentWire `json:"deployments"`
	// PodsNotes say the namespace was grouped without its pods, as on the
	// inventory.
	PodsNotes []string `json:"podsNotes,omitempty"`
}

// service answers whether a Service is one the inventory would offer, from
// the same discovery and the same guess, and dials nothing. A Service that is
// not is `not_redis`, an absence: the tab's probe draws no tab for it.
func (Redis) service(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		found, err := discover(r, h, cluster, namespace)
		if err != nil {
			writeListFailure(w, cluster, err)
			return
		}
		out := serviceAnswer{Namespace: namespace, Service: name, Deployments: []deploymentWire{}}
		// A Service the inventory would offer is one with a Redis door; one
		// reached on a Sentinel port only is drawn in the inventory as a
		// member and is not a Redis to open, so it gets no tab.
		offered := false
		for _, door := range found.Instances {
			offered = offered || door.Service == name
		}
		if offered {
			folded, notes := deployments(r, h, cluster, found)
			out.PodsNotes = notes
			for _, d := range folded {
				mine := false
				for _, m := range d.Members {
					for _, door := range m.Backend.Services {
						mine = mine || door.Service == name
					}
				}
				if mine {
					out.Deployments = append(out.Deployments, wireDeployment(d))
				}
			}
		}
		if len(out.Deployments) == 0 {
			message := "No port of Service " + namespace + "/" + name + " looks like a Redis or a Valkey: none is " +
				"named redis or valkey, and none is on 6379."
			for _, door := range found.Sentinels {
				if door.Service == name {
					message = "Service " + namespace + "/" + name + " is reached on a Sentinel port only. A Sentinel " +
						"watches Redis servers and holds no keys; this viewer does not open one."
					break
				}
			}
			writeRefusal(w, &refusal{status: http.StatusNotFound, code: CodeNotRedis, kind: kindAbsent, message: message})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// --- the instance routes, which dial ------------------------------------------

type serverWire struct {
	// Engine is "Redis" or "Valkey", as the server reports itself.
	Engine    string `json:"engine"`
	Version   string `json:"version"`
	Mode      string `json:"mode"`
	Databases int    `json:"databases"`
	// Role is the server's own `INFO replication` answer, primary or
	// replica, and Replicas how many replicas a primary has attached now
	// (absent when not said). It replaces the role the labels guessed.
	Role     string `json:"role,omitempty"`
	Replicas *int   `json:"replicas,omitempty"`
	// Note is what the mode means for this page: a Cluster node shows its own
	// slots only, a Sentinel holds no keys.
	Note string `json:"note,omitempty"`
}

func wireServer(c *Conn) serverWire {
	out := serverWire{Engine: c.Engine, Version: c.Version, Mode: c.Mode, Databases: c.Databases, Role: c.Role}
	if c.Role == rolePrimary && c.Replicas >= 0 {
		n := c.Replicas
		out.Replicas = &n
	}
	switch c.Mode {
	case "cluster":
		out.Note = "This node is part of a Redis Cluster. What is shown is the keys in this node's own slots; " +
			"the cluster's other nodes are not read, and this viewer does not follow a redirect to them."
	case "sentinel":
		out.Note = "This port answers as a Sentinel, which watches Redis servers and holds no keys of its own. " +
			"Open the Redis it monitors instead."
	}
	return out
}

type overviewWire struct {
	Keys       int64             `json:"keys"`
	UsedMemory string            `json:"usedMemory,omitempty"`
	Keyspace   []DatabaseSummary `json:"keyspace"`
}

type scanWire struct {
	Match string `json:"match"`
	// Cursor is the one this page was read from, Next the one the server handed
	// back. Strings, because a cursor is an unsigned 64-bit number and a
	// browser's numbers are not.
	Cursor string       `json:"cursor"`
	Next   string       `json:"next,omitempty"`
	Done   bool         `json:"done"`
	Keys   []KeySummary `json:"keys"`
}

type instanceAnswer struct {
	Instance instanceWire `json:"instance"`
	// Pod is the pod the tunnel reached.
	Pod    string     `json:"pod,omitempty"`
	DB     int        `json:"db"`
	Server serverWire `json:"server"`
	// Credential is the provenance of the password used, when one was needed.
	// Secret, key, source and pod - never the value, which is not a field.
	Credential *Credential   `json:"credential,omitempty"`
	Overview   *overviewWire `json:"overview,omitempty"`
	Scan       *scanWire     `json:"scan,omitempty"`
	Key        *KeyDetail    `json:"key,omitempty"`
}

// query is what an instance route reads from its query string. It is an
// allowlist: a `password` (or anything else) added to an address never
// reaches a dial.
type query struct {
	db     int
	match  string
	cursor uint64
	key    string
	hasKey bool
	// page is where the key route starts reading the key: the token the
	// previous page handed back, or the first page.
	page pageToken
}

// The bounds on what a request may ask for.
const (
	maxDatabase = 1 << 16
	maxMatch    = 1024
)

func readQuery(r *http.Request, wantKey bool) (query, error) {
	values := r.URL.Query()
	var q query
	if text := values.Get("db"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 || n >= maxDatabase {
			return q, errors.New("db must be a database number")
		}
		q.db = n
	}
	q.match = strings.TrimSpace(values.Get("match"))
	if len(q.match) > maxMatch {
		return q, errors.New("the pattern is longer than this console sends")
	}
	if text := values.Get("cursor"); text != "" {
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return q, errors.New("cursor must be the number the server handed back")
		}
		q.cursor = n
	}
	if wantKey {
		switch raw, name := values.Get("raw"), values.Get("key"); {
		case raw != "":
			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			if err != nil {
				return q, errors.New("raw must be the key's bytes as the inventory sent them")
			}
			q.key, q.hasKey = string(decoded), true
		case values.Has("key"):
			q.key, q.hasKey = name, true
		default:
			return q, errors.New("no key was named")
		}
		page, err := parsePage(values.Get("page"))
		if err != nil {
			return q, err
		}
		q.page = page
	}
	return q, nil
}

// target reads the instance from the path and finds it through discovery.
//
// Discovery is consulted before dialling, so a bookmark to an uninstalled
// Redis reads as gone rather than as a network failure.
func target(r *http.Request, h extension.Host, cluster string) (svcgroup.Instance, error) {
	namespace, service := r.PathValue("namespace"), r.PathValue("service")
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port <= 0 || port > 65535 || namespace == "" || service == "" {
		return svcgroup.Instance{}, &refusal{status: http.StatusBadRequest, code: CodeBadRequest, kind: kindFailure,
			message: "This address does not name an instance: a namespace, a Service and a port number."}
	}
	found, err := discover(r, h, cluster, namespace)
	if err != nil {
		return svcgroup.Instance{}, listRefusal(cluster, err)
	}
	instance, ok := svcgroup.Find(found.Instances, namespace, service, port)
	if !ok {
		return svcgroup.Instance{}, &refusal{status: http.StatusNotFound, code: CodeInstanceGone, kind: kindAbsent,
			message: "There is no Redis or Valkey at " + namespace + "/" + service + ":" + strconv.Itoa(port) +
				" that this console may list any more. It may have been uninstalled, renamed, or moved to another port."}
	}
	return instance, nil
}

// opened is the common start of the three dialling routes: the query, the
// instance, the session. It writes the refusal itself and answers nil when
// there is nothing more to do.
func opened(w http.ResponseWriter, r *http.Request, h extension.Host, names *keyNames, wantKey bool) (*session, svcgroup.Instance, query) {
	cluster := r.PathValue("cluster")
	q, err := readQuery(r, wantKey)
	if err != nil {
		writeRefusal(w, &refusal{status: http.StatusBadRequest, code: CodeBadRequest, kind: kindFailure, message: err.Error()})
		return nil, svcgroup.Instance{}, q
	}
	instance, err := target(r, h, cluster)
	if err != nil {
		writeRefusal(w, err)
		return nil, instance, q
	}
	s, err := connect(r, h, names, cluster, instance, q.db)
	if err != nil {
		writeRefusal(w, err)
		return nil, instance, q
	}
	return s, instance, q
}

func (x Redis) instance(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, instance, q := opened(w, r, h, x.names, false)
		if s == nil {
			return
		}
		defer s.close()
		out := instanceAnswer{Instance: wireInstance(instance), Pod: s.tunnel.Pod, DB: q.db,
			Server: wireServer(s.conn), Credential: s.credential}
		if s.conn.Mode == "sentinel" {
			writeJSON(w, http.StatusOK, out)
			return
		}

		overview, err := s.conn.Overview(r.Context())
		if err != nil {
			writeRefusal(w, readRefusal(r.Context(), err, s))
			return
		}
		out.Overview = wireOverview(overview)

		page, err := s.conn.Scan(r.Context(), q.cursor, q.match)
		if err != nil {
			writeRefusal(w, readRefusal(r.Context(), err, s))
			return
		}
		scan := &scanWire{Match: q.match, Cursor: strconv.FormatUint(q.cursor, 10), Done: page.Done, Keys: page.Keys}
		if !page.Done {
			scan.Next = strconv.FormatUint(page.Cursor, 10)
		}
		out.Scan = scan
		writeJSON(w, http.StatusOK, out)
	})
}

// probe answers one row's "does this actually answer?": one tunnel, INFO and
// DBSIZE, closed. A Ready pod may hold a Redis that answers nothing.
func (x Redis) probe(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, instance, q := opened(w, r, h, x.names, false)
		if s == nil {
			return
		}
		defer s.close()
		out := instanceAnswer{Instance: wireInstance(instance), Pod: s.tunnel.Pod, DB: q.db,
			Server: wireServer(s.conn), Credential: s.credential}
		if s.conn.Mode != "sentinel" {
			overview, err := s.conn.Overview(r.Context())
			if err != nil {
				writeRefusal(w, readRefusal(r.Context(), err, s))
				return
			}
			out.Overview = wireOverview(overview)
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// key opens one key, bounded and a page at a time: the first 64 KiB of a
// string, one page of anything else from where `page` says, the token of the
// page after it, and a note saying what was cut.
func (x Redis) key(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, instance, q := opened(w, r, h, x.names, true)
		if s == nil {
			return
		}
		defer s.close()
		out := instanceAnswer{Instance: wireInstance(instance), Pod: s.tunnel.Pod, DB: q.db,
			Server: wireServer(s.conn), Credential: s.credential}
		if s.conn.Mode == "sentinel" {
			writeRefusal(w, &refusal{status: http.StatusConflict, code: CodeSentinel, kind: kindRefusal,
				message: out.Server.Note})
			return
		}
		detail, err := s.conn.Key(r.Context(), q.key, q.page)
		if err != nil {
			if errors.Is(err, errWrongPage) {
				writeRefusal(w, &refusal{status: http.StatusBadRequest, code: CodeBadRequest, kind: kindFailure,
					message: "The page asked for was handed out for another kind of key than this one, which " +
						"may have been replaced since. Open the key again from its first page."})
				return
			}
			if errors.Is(err, errNoSuchKey) {
				writeRefusal(w, &refusal{status: http.StatusNotFound, code: CodeKeyGone, kind: kindAbsent,
					message: "There is no such key in database " + strconv.Itoa(q.db) +
						" - it may have expired or been deleted since the list was drawn."})
				return
			}
			writeRefusal(w, readRefusal(r.Context(), err, s))
			return
		}
		out.Key = &detail
		writeJSON(w, http.StatusOK, out)
	})
}

func wireOverview(o Overview) *overviewWire {
	out := &overviewWire{Keys: o.Keys, UsedMemory: o.UsedMemory, Keyspace: o.Keyspace}
	if out.Keyspace == nil {
		out.Keyspace = []DatabaseSummary{}
	}
	return out
}

// readRefusal is a read that failed on an open connection.
func readRefusal(ctx context.Context, err error, s *session) error {
	if ctx.Err() != nil {
		return &refusal{status: http.StatusServiceUnavailable, code: CodeRequestCancelled, kind: kindFailure,
			message: "The request ended before the instance answered."}
	}
	password := ""
	if s.credential != nil {
		password = s.credential.password
	}
	return &refusal{status: http.StatusBadGateway, code: CodeReadFailed, kind: kindFailure,
		message: "Connected, and the instance did not answer a read.", detail: scrubbed(err, password)}
}

// --- failures ----------------------------------------------------------------

// problem is the failure body: Code is what the page branches on, Kind whether
// it is a refusal, a failure or an absence, Message what it prints.
type problem struct {
	Code    string `json:"code"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func listRefusal(cluster string, err error) error {
	if errors.Is(err, extension.ErrUnknownCluster) {
		return &refusal{status: unknownClusterStatus, code: CodeUnknownCluster, kind: kindAbsent,
			message: extension.UnknownClusterText(cluster)}
	}
	if said := personRefusal(err, "The cluster says you may not list the Services here, "+
		"so whether this cluster runs a Redis you may open is not shown."); said != nil {
		return said
	}
	return &refusal{status: http.StatusBadGateway, code: CodeListFailed, kind: kindFailure,
		message: "The Services could not be read, so whether this cluster runs a Redis is unknown.",
		detail:  shortError(err)}
}

// personRefusal is a read the access review refused for the person (403, a
// refusal) or could not make (503, a failure to ask), as this package says it;
// nil for anything else, which is the caller's. Never a 502: the cluster was
// not read.
func personRefusal(err error, denied string) error {
	refused, ok := extension.RefusedRead(err, denied)
	if !ok {
		return nil
	}
	kind := kindRefusal
	if refused.Status != http.StatusForbidden {
		kind = kindFailure
	}
	return &refusal{status: refused.Status, code: refused.Code, kind: kind,
		message: refused.Message, detail: shortError(err)}
}

func writeListFailure(w http.ResponseWriter, cluster string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	writeRefusal(w, listRefusal(cluster, err))
}

func writeRefusal(w http.ResponseWriter, err error) {
	var said *refusal
	if !errors.As(err, &said) {
		said = &refusal{status: http.StatusBadGateway, code: CodeReadFailed, kind: kindFailure,
			message: "The instance could not be read.", detail: shortError(err)}
	}
	writeJSON(w, said.status, problem{Code: said.code, Kind: said.kind, Message: said.message, Detail: said.detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
