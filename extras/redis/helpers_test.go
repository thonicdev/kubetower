package redis

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thonicdev/kubetower/extension"
)

// fakeHost is extension.Host with what this extra uses made real: List
// answers from the objects it holds (namespace and metadata.name filtered, as
// the console's list does), Forward hands the address of a fake server and
// names a pod, and Reveal answers from the Secrets it holds - recording every
// key it was asked for.
type fakeHost struct {
	mu sync.Mutex

	objects map[string][]unstructured.Unstructured // by resource
	listErr map[string]error                       // by resource
	// narrowed answers a listing of that resource as stopped before its end
	// (Page.Complete false), as a page cap or a timeout would.
	narrowed map[string]bool
	// narrowedWide narrows only a listing across namespaces; refusedIn
	// ("resource/namespace") refuses a listing scoped to that namespace.
	narrowedWide map[string]bool
	refusedIn    map[string]error
	listed       []string // "resource/namespace" of every List, in order

	forwardTo  string
	forwardPod string
	podUnknown bool
	forwardErr error
	forwards   int
	stopped    int

	secrets   map[string]map[string]string // "namespace/name" -> key -> value
	revealErr error
	reveals   []string // "namespace/name/key", in order
}

func newHost() *fakeHost {
	return &fakeHost{objects: map[string][]unstructured.Unstructured{}, listErr: map[string]error{},
		secrets: map[string]map[string]string{}, forwardPod: "redis-0"}
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("the Redis viewer must not read through Host.Client: its reads are reviewed for nobody")
}
func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	return nil, errors.New("not used")
}
func (h *fakeHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	return extension.Usage{}, errors.New("not used")
}
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, errors.New("not used")
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this fake runs no command")
}

func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string { return "" }

func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error {
	return errors.New("not used")
}

func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.forwardErr != nil {
		return extension.Tunnel{}, h.forwardErr
	}
	h.forwards++
	t := extension.Tunnel{Addr: h.forwardTo, Pod: h.forwardPod, PodKnown: true, Stop: func() {
		h.mu.Lock()
		h.stopped++
		h.mu.Unlock()
	}}
	if h.podUnknown {
		t.Pod, t.PodKnown = "", false
	}
	return t, nil
}

// ForwardAsConsole is the console's own tunnel, which the Redis viewer never
// opens: it reaches the person's workload, as the person (Forward).
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("the Redis viewer must tunnel as the person, never as the console")
}

func (h *fakeHost) Reveal(_ *http.Request, _ string, namespace, name, key string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reveals = append(h.reveals, namespace+"/"+name+"/"+key)
	if h.revealErr != nil {
		return "", h.revealErr
	}
	secret, ok := h.secrets[namespace+"/"+name]
	if !ok {
		return "", fmt.Errorf("secret %s/%s: %w", namespace, name, extension.ErrNoSuchSecret)
	}
	value, ok := secret[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s key %s: %w", namespace, name, key, extension.ErrNoSuchKey)
	}
	return value, nil
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.listed = append(h.listed, q.GVR.Resource+"/"+q.Namespace)
	if err := h.refusedIn[q.GVR.Resource+"/"+q.Namespace]; err != nil {
		return extension.Page{}, err
	}
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	out := []unstructured.Unstructured{}
	for _, o := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && o.GetName() != name {
			continue
		}
		// The API server's own label grammar, as the suggestion's pod
		// fallback asks it (suggest.go).
		if q.LabelSelector != "" {
			selector, err := labels.Parse(q.LabelSelector)
			if err != nil {
				return extension.Page{}, err
			}
			if !selector.Matches(labels.Set(o.GetLabels())) {
				continue
			}
		}
		out = append(out, o)
	}
	if h.narrowed[q.GVR.Resource] || (q.Namespace == "" && h.narrowedWide[q.GVR.Resource]) {
		return extension.Page{Items: out, Complete: false, Note: "the listing stopped before its end"}, nil
	}
	return extension.Page{Items: out, Complete: true}, nil
}

func (h *fakeHost) add(t *testing.T, resource string, object any) {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	h.objects[resource] = append(h.objects[resource], unstructured.Unstructured{Object: raw})
}

func (h *fakeHost) secret(namespace, name string, data map[string]string) {
	h.secrets[namespace+"/"+name] = data
}

// redisService is a Service with one port, and the EndpointSlice behind it
// naming pods.
func (h *fakeHost) redisService(t *testing.T, namespace, name string, port corev1.ServicePort, pods ...string) {
	t.Helper()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{port}},
	})
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name + "-abc",
			Labels: map[string]string{discoveryv1.LabelServiceName: name}},
	}
	for _, pod := range pods {
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
			Addresses: []string{"10.0.0.1"},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: pod, Namespace: namespace},
		})
	}
	if len(pods) > 0 {
		h.add(t, "endpointslices", slice)
	}
}

// fromSecret is an env var reading one key of one Secret.
func fromSecret(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret},
				Key:                  key,
			},
		},
	}
}

func pod(namespace, name string, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.PodSpec{Containers: containers},
	}
}

// serve answers one request against the extension's routes, mounted as the
// console mounts them (under /x/{cluster}/redis).
func serve(t *testing.T, h extension.Host, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/"+Key+route.Pattern, route.Handler)
	}
	w := httptest.NewRecorder()
	method := http.MethodGet
	if strings.Contains(target, "/instances/") {
		// The routes that dial are POSTs (redis.go, Routes).
		method = http.MethodPost
	}
	mux.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the answer is not JSON (%d): %v\n%s", w.Code, err, w.Body.String())
	}
	return out
}

// --- a fake RESP server --------------------------------------------------------

// value is one key on the fake server.
type value struct {
	kind  string // string, list, hash, set, zset, stream, or a module's name
	s     string
	items []string
	hash  map[string]string
	ttl   int64
	// strlen, when set, is what STRLEN answers instead of len(s): a string
	// too large to hold in a test, said to be that large.
	strlen int64
	// stream is a stream's entries, in ID order.
	stream []streamItem
}

// streamItem is one entry of a stream on the fake server: its ID and its
// fields and values, flat, in order.
type streamItem struct {
	id     string
	fields []string
}

// fakeRedis speaks enough RESP to be a Redis or a Valkey to valkey-go, and
// **records every command that reaches its socket** - which is what the wire
// tests assert on, rather than on what this package says it sends.
type fakeRedis struct {
	t        *testing.T
	ln       net.Listener
	engine   string // "Redis" or "Valkey"
	mode     string // "standalone", "cluster", "sentinel"
	password string
	noHello  bool // answer HELLO as a Redis 5 does
	// role is what INFO replication says: "master" unless a test says
	// otherwise, "slave" for a replica, "" for no such section.
	role string
	// nestedScores answers ZRANGE ... WITHSCORES as RESP3 does, a pair per
	// member with the score a double, rather than RESP2's flat strings.
	nestedScores bool
	// scanStep, when set, is the most elements one HSCAN or SSCAN round
	// answers, whatever COUNT asked: a real server's rounds can come back
	// short, and a page must still be made of them.
	scanStep int
	data     map[int]map[string]value

	serving  sync.Once
	mu       sync.Mutex
	commands [][]string
	// scanned is how many elements HSCAN and SSCAN answers carried, in all.
	scanned int
	conns   int
}

func newFakeRedis(t *testing.T, engine string) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{t: t, ln: ln, engine: engine, mode: "standalone", role: "master", data: map[int]map[string]value{0: {}}}
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

// addr starts serving and says where. A test configures the server (password,
// mode, engine) before asking for its address, and the go statement here is
// what orders those writes before every connection's reads.
func (f *fakeRedis) addr() string {
	f.serving.Do(func() { go f.accept() })
	return f.ln.Addr().String()
}

func (f *fakeRedis) set(db int, key string, v value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data[db] == nil {
		f.data[db] = map[string]value{}
	}
	f.data[db][key] = v
}

// scannedSoFar is how many elements the HSCAN and SSCAN answers carried.
func (f *fakeRedis) scannedSoFar() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scanned
}

// seen is every command received so far, upper-cased name first.
func (f *fakeRedis) seen() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.commands...)
}

// sent says whether any command carried this text anywhere in its arguments.
func (f *fakeRedis) sent(text string) bool {
	for _, cmd := range f.seen() {
		for _, arg := range cmd {
			if strings.Contains(arg, text) {
				return true
			}
		}
	}
	return false
}

func (f *fakeRedis) accept() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		f.mu.Unlock()
		go f.handle(conn)
	}
}

func (f *fakeRedis) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	authed := f.password == ""
	db := 0
	for {
		cmd, err := readCommand(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
		reply := f.answer(cmd, &authed, &db)
		_, _ = w.WriteString(reply)
		if w.Flush() != nil {
			return
		}
	}
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return strings.Fields(line), nil
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for range n {
		head, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimRight(head, "\r\n")[1:])
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf[:size]))
	}
	return out, nil
}

func bulk(s string) string   { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }
func integer(n int64) string { return ":" + strconv.FormatInt(n, 10) + "\r\n" }
func array(items []string) string {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
	for _, item := range items {
		b.WriteString(bulk(item))
	}
	return b.String()
}

func (f *fakeRedis) answer(cmd []string, authed *bool, db *int) string {
	if len(cmd) == 0 {
		return "-ERR empty command\r\n"
	}
	name := strings.ToUpper(cmd[0])
	f.mu.Lock()
	keys := f.data[*db]
	f.mu.Unlock()

	switch name {
	case "HELLO":
		if f.noHello {
			return "-ERR unknown command 'HELLO', with args beginning with: '3'\r\n"
		}
		for i := 1; i+3 <= len(cmd); i++ {
			if strings.EqualFold(cmd[i], "AUTH") {
				if cmd[i+2] != f.password {
					return "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
				}
				*authed = true
			}
		}
		if !*authed {
			return "-NOAUTH HELLO must be called with the client already authenticated, otherwise the HELLO " +
				"<proto> AUTH <user> <pass> option can be used to authenticate the client and select the RESP " +
				"protocol version at the same time\r\n"
		}
		proto := "2"
		if len(cmd) > 1 {
			proto = cmd[1]
		}
		server := strings.ToLower(f.engine)
		return "%3\r\n+server\r\n+" + server + "\r\n+version\r\n+" + f.version() + "\r\n+proto\r\n:" + proto + "\r\n"
	case "AUTH":
		if cmd[len(cmd)-1] != f.password {
			return "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
		}
		*authed = true
		return "+OK\r\n"
	}
	if !*authed {
		return "-NOAUTH Authentication required.\r\n"
	}

	arg := func(i int) string {
		if i < len(cmd) {
			return cmd[i]
		}
		return ""
	}
	key, found := keys[arg(1)]

	switch name {
	case "PING":
		return "+PONG\r\n"
	case "SELECT":
		n, _ := strconv.Atoi(arg(1))
		if f.mode == "cluster" && n != 0 {
			return "-ERR SELECT is not allowed in cluster mode\r\n"
		}
		if n < 0 || n >= 16 {
			return "-ERR DB index is out of range\r\n"
		}
		*db = n
		return "+OK\r\n"
	case "INFO":
		return bulk(f.info(strings.ToLower(arg(1)), keys))
	}
	if f.mode == "sentinel" {
		return "-ERR unknown command '" + cmd[0] + "'\r\n"
	}
	switch name {
	case "DBSIZE":
		return integer(int64(len(keys)))
	case "SCAN":
		return f.scan(cmd, keys)
	case "TYPE":
		if !found {
			return "+none\r\n"
		}
		return "+" + key.kind + "\r\n"
	case "TTL":
		if !found {
			return integer(-2)
		}
		if key.ttl == 0 {
			return integer(-1)
		}
		return integer(key.ttl)
	case "GET":
		if !found {
			return "$-1\r\n"
		}
		return bulk(key.s)
	case "STRLEN":
		if key.strlen > 0 {
			return integer(key.strlen)
		}
		return integer(int64(len(key.s)))
	case "LLEN", "SCARD", "ZCARD":
		return integer(int64(len(key.items)))
	case "XLEN":
		return integer(int64(len(key.stream)))
	case "XRANGE":
		return f.xrange(cmd, key)
	case "HLEN":
		return integer(int64(len(key.hash)))
	case "LRANGE", "ZRANGE":
		start, _ := strconv.Atoi(arg(2))
		stop, _ := strconv.Atoi(arg(3))
		items := key.items
		if stop+1 < len(items) {
			items = items[:stop+1]
		}
		items = items[min(start, len(items)):]
		if name == "ZRANGE" && strings.EqualFold(arg(4), "WITHSCORES") {
			if f.nestedScores {
				var b strings.Builder
				b.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
				for i, item := range items {
					b.WriteString("*2\r\n" + bulk(item) + "," + strconv.Itoa(start+i+1) + ".5\r\n")
				}
				return b.String()
			}
			var flat []string
			for i, item := range items {
				flat = append(flat, item, strconv.Itoa(start+i+1)+".5")
			}
			return array(flat)
		}
		return array(items)
	case "SSCAN", "HSCAN":
		// A cursor that is an offset, and COUNT honored exactly - stricter
		// than a real server, which may answer a small key whole - so a test
		// sees how much crossed. Fields come in name order.
		var flat []string
		if name == "SSCAN" {
			flat = append(flat, key.items...)
		} else {
			fields := make([]string, 0, len(key.hash))
			for field := range key.hash {
				fields = append(fields, field)
			}
			sort.Strings(fields)
			for _, field := range fields {
				flat = append(flat, field, key.hash[field])
			}
		}
		per := 1
		if name == "HSCAN" {
			per = 2
		}
		from, _ := strconv.Atoi(arg(2))
		count := 10
		if strings.EqualFold(arg(3), "COUNT") {
			count, _ = strconv.Atoi(arg(4))
		}
		if f.scanStep > 0 {
			count = min(count, f.scanStep)
		}
		start, end := from*per, (from+count)*per
		if start > len(flat) {
			start = len(flat)
		}
		next := strconv.Itoa(from + count)
		if end >= len(flat) {
			end, next = len(flat), "0"
		}
		f.mu.Lock()
		f.scanned += (end - start) / per
		f.mu.Unlock()
		return "*2\r\n" + bulk(next) + array(flat[start:end])
	case "MEMORY":
		return integer(72)
	case "OBJECT":
		return "+listpack\r\n"
	}
	return "-ERR unknown command '" + cmd[0] + "'\r\n"
}

func (f *fakeRedis) version() string {
	if f.engine == "Valkey" {
		return "8.0.1"
	}
	return "7.2.4"
}

func (f *fakeRedis) info(section string, keys map[string]value) string {
	var b strings.Builder
	if section == "" || section == "server" {
		b.WriteString("# Server\r\n")
		if f.engine == "Valkey" {
			b.WriteString("redis_version:7.2.4\r\nserver_name:valkey\r\nvalkey_version:8.0.1\r\n")
		} else {
			b.WriteString("redis_version:7.2.4\r\n")
		}
		b.WriteString("redis_mode:" + f.mode + "\r\n")
	}
	if section == "" || section == "memory" {
		b.WriteString("# Memory\r\nused_memory:1048576\r\n")
	}
	if section == "" || section == "cluster" {
		enabled := "0"
		if f.mode == "cluster" {
			enabled = "1"
		}
		b.WriteString("# Cluster\r\ncluster_enabled:" + enabled + "\r\n")
	}
	if (section == "" || section == "replication") && f.mode != "sentinel" && f.role != "" {
		b.WriteString("# Replication\r\nrole:" + f.role + "\r\n")
		if f.role == "master" {
			b.WriteString("connected_slaves:2\r\n")
		}
	}
	if section == "" || section == "keyspace" {
		b.WriteString("# Keyspace\r\n")
		f.mu.Lock()
		for db, keys := range f.data {
			if len(keys) > 0 {
				b.WriteString(fmt.Sprintf("db%d:keys=%d,expires=0,avg_ttl=0\r\n", db, len(keys)))
			}
		}
		f.mu.Unlock()
	}
	return b.String()
}

// scan answers in two rounds: the first half of the sorted keys with cursor 1,
// then the rest with cursor 0, each filtered by MATCH after the slice was cut -
// which is how a real SCAN can hand back an empty round that is not the end.
func (f *fakeRedis) scan(cmd []string, keys map[string]value) string {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	half := (len(names) + 1) / 2
	cursor, next := cmd[1], "1"
	slice := names[:half]
	if cursor != "0" {
		slice, next = names[half:], "0"
	}
	if len(names) <= 1 {
		next = "0"
	}
	match := ""
	for i := 2; i+1 < len(cmd); i++ {
		if strings.EqualFold(cmd[i], "MATCH") {
			match = cmd[i+1]
		}
	}
	var out []string
	for _, name := range slice {
		if ok, _ := path.Match(match, name); match == "" || ok {
			out = append(out, name)
		}
	}
	return "*2\r\n" + bulk(next) + array(out)
}

// xrange answers `XRANGE key start + COUNT n` with the inclusive start a
// real server honors, comparing IDs as numbers, and refuses it without a
// COUNT: the viewer must never ask for a whole stream.
func (f *fakeRedis) xrange(cmd []string, key value) string {
	if len(cmd) != 6 || cmd[3] != "+" || !strings.EqualFold(cmd[4], "COUNT") {
		return "-ERR this fake answers XRANGE key start + COUNT n only\r\n"
	}
	count, _ := strconv.Atoi(cmd[5])
	from := streamID{}
	if cmd[2] != "-" {
		id, ok := parseStreamID(cmd[2])
		if !ok {
			return "-ERR Invalid stream ID specified as stream command argument\r\n"
		}
		from = id
	}
	var b strings.Builder
	var out []string
	for _, item := range key.stream {
		id, _ := parseStreamID(item.id)
		if id.ms < from.ms || (id.ms == from.ms && id.seq < from.seq) {
			continue
		}
		if len(out) == count {
			break
		}
		out = append(out, "*2\r\n"+bulk(item.id)+array(item.fields))
	}
	b.WriteString("*" + strconv.Itoa(len(out)) + "\r\n")
	for _, entry := range out {
		b.WriteString(entry)
	}
	return b.String()
}

// ConfiguredSecret and ConfiguredService are the console's reads of what its
// configuration names, which this extra never makes: it names nothing.
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}

func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}
