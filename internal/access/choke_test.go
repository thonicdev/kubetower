package access

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestNoWriteReachesAClusterOutsideTheChokePoint is the structural half of
// docs/SERVER-MODE-THREAT-MODEL.md §5.2, and it is not a test of the choke
// point. A behavioral test proves the door works; only this walk proves there
// is no second door.
//
// **Typed, over every package of the module.** Each package is type-checked
// from source against its imports' export data (`go list -export`), so a call
// is judged by what it resolves to rather than by its spelling:
//
//   - **A write** is a method of a `k8s.io/client-go/...` type or interface
//     whose name is in writeMethods - called, or taken as a method value
//     (`del := iface.Delete`) - or the **builder** of a raw REST request that
//     changes something: `Post`, `Put`, `Patch` or `Delete` on a
//     `k8s.io/client-go/rest` type, and `Verb` with anything but a literal
//     `GET`, called or taken as a method value. The builder and not the send,
//     because a request is routinely built into a variable and sent later
//     (`req := rc.Post()...; req.Do(ctx)`), and the send cannot tell which
//     verb it carries.
//   - **The door** is `(*access.Gate).Perform` by identity, or a function that
//     hands one of its own function parameters to it and does nothing else
//     with that parameter. A local method called Perform is not a door; a door
//     that also calls its closure directly is not one either.
//   - A write must sit **lexically inside a function literal passed to a
//     door**, or inside a function every one of whose uses sits inside one.
//
// **What it cannot see, stated rather than discovered**:
//
//   - a write made through reflection, through a client-go method name missing
//     from writeMethods, or in a file the current GOOS does not build;
//   - **a closure leaked out of a Perform and run later**: placement is what is
//     proven, and a closure stored and called after Perform returned sits
//     inside a Perform lexically all the same;
//   - **a Perform asked about the wrong attributes**: placement again, not
//     equality - the walk cannot tell that the question asked is the request
//     made. internal/edit's door builds both from the same Location, and the
//     runtime backstop, transport.go's Guard, checks it on every request;
//   - **an extension's own code** is read like any other: the extras in
//     `extras/` are in this module and inside the walk. The same Guard, on
//     every client the cluster manager builds, refuses an extension's write
//     that no Perform stamped - which is every one, since an extension cannot
//     reach Perform.
func TestNoWriteReachesAClusterOutsideTheChokePoint(t *testing.T) {
	pkgs := loadModule(t)
	sites := writeSites(pkgs)
	var reviewed, listed int
	for _, site := range sites {
		switch {
		case site.inPerform, site.gatedBy != "":
			reviewed++
		case notAClusterWrite[site.key()] != "":
			listed++
		default:
			t.Errorf("%s: %s reaches a cluster outside Gate.Perform (in %s). Wrap the request in a "+
				"closure passed to Perform, so the person behind the request is reviewed before it is sent",
				site.pos, site.call, site.function)
		}
	}
	seen := map[string]bool{}
	for _, site := range sites {
		seen[site.key()] = true
	}
	for key := range notAClusterWrite {
		if !seen[key] {
			t.Errorf("notAClusterWrite lists %q, which the walk no longer finds; remove it", key)
		}
	}
	// The floor: edit's patch, create, delete and eviction, the exec stream and
	// the node shell's pod. Fewer means the walk stopped seeing the tree.
	if reviewed < 6 {
		t.Fatalf("the walk found %d reviewed writes; at least 6 exist, so it is not seeing the tree", reviewed)
	}
	t.Logf("%d packages, %d write-capable calls, %d inside Gate.Perform, %d listed as not a person's write",
		len(pkgs), len(sites), reviewed, listed)
}

// TestNoReadReachesAClusterOutsideTheReview is the same walk for reads: a
// client call that reads a cluster's objects - `List`, `Get`, `Watch`,
// `GetLogs`, a raw REST `GET` - called or taken as a method value, must sit
// inside a closure handed to Gate.Read or List (or be handed to one in place,
// `access.List(..., api.Pods(ns).List)`-shaped through a door that only
// forwards it), or inside a function only ever used from inside one.
//
// The filter on the way out is the same choke point, not a second one, and an
// endpoint that reads without passing through it serves objects
// its caller should not have. **This is the test that there is no such
// endpoint**, with the same blind spots as the write walk, listed there.
//
// The reads deliberately made with the console's own rights are listed in
// notAPersonsRead, each with its reason.
func TestNoReadReachesAClusterOutsideTheReview(t *testing.T) {
	pkgs := loadModule(t)
	sites := readSites(pkgs)
	var reviewed, listed int
	for _, site := range sites {
		switch {
		case site.inPerform, site.gatedBy != "":
			reviewed++
		case notAPersonsRead[site.key()] != "":
			listed++
		default:
			t.Errorf("%s: %s reads a cluster outside Gate.Read and access.List (in %s). Wrap the read in a "+
				"closure passed to one, so the person behind the request is reviewed before what it read is "+
				"shown to them - or, if it fills a hold every person is served from, list it in "+
				"notAPersonsRead with the reason", site.pos, site.call, site.function)
		}
	}
	seen := map[string]bool{}
	for _, site := range sites {
		seen[site.key()] = true
	}
	for key := range notAPersonsRead {
		if !seen[key] {
			t.Errorf("notAPersonsRead lists %q, which the walk no longer finds; remove it", key)
		}
	}
	// The floor: the 36 kinds of the list tables alone are each a reviewed
	// read. Fewer means the walk stopped seeing the tree.
	if reviewed < 40 {
		t.Fatalf("the walk found %d reviewed reads; at least 40 exist, so it is not seeing the tree", reviewed)
	}
	t.Logf("%d packages, %d read-capable calls, %d inside a read door, %d listed as not a person's read",
		len(pkgs), len(sites), reviewed, listed)
}

// notAPersonsRead is every read the walk finds that is deliberately made with
// the console's own rights, keyed by file and enclosing function, each with
// the reason. **Short on purpose**: an entry is a read nobody is reviewed for,
// and it has to say why what it reads cannot reach a person unreviewed.
var notAPersonsRead = map[string]string{
	// The catalog of kinds the cluster serves. Equivalent to discovery,
	// which every authenticated identity may read (`system:discovery`): it
	// names kinds, never objects, and an object of any kind is read through
	// Registry.List or Registry.Get, which are reviewed.
	"internal/customresources/customresources.go Registry.refreshInBackground": "the kinds a cluster serves, as discovery says them to everybody",
	"internal/customresources/customresources.go Registry.catalogueRound":      "the kinds a cluster serves, as discovery says them to everybody",
	// The event archive's collector. It writes to the state directory and
	// nothing serves it back through the API
	// (docs/SERVER-MODE-THREAT-MODEL.md §6.2).
	"internal/recorder/recorder.go Recorder.collect": "the archive's own collection, never served back to a person",
	// The node shell's clean-up of the pods this console created for a shell.
	"internal/terminal/nodeshell.go Terminal.sweepNodeShells": "the console finding its own node-shell pods to remove them",
	// The console's monitoring backend: where Prometheus is, whether
	// metrics-server is installed, and the series and usage figures read into
	// the process-wide hold. A person is shown a figure beside a row their own
	// list returned, or on the page of an object their own `get` allowed - the
	// review is on the way out.
	"internal/metrics/metrics.go listCandidates": "the console finding its own monitoring backend (findPrometheus, and the integration tests' VictoriaMetricsAlone)",
	"internal/metrics/metrics.go restRaw":        "the console's monitoring backend, into a hold reviewed on the way out",
	// The kubelet's stats, read per node into the usage hold behind the node
	// and claim tables, and for an extension's Host.Usage - which is reviewed
	// in internal/web before it reads.
	"internal/kubeletstats/kubeletstats.go Read": "a node's measured usage, into a hold reviewed on the way out",
	"internal/web/nodedisk.go readNodeDiskWith":  "the node list behind the shared disk gauges, joined only to rows a person's own list returned",
	"internal/web/volumeusage.go volumeUsageOf":  "the pods behind the shared volume gauges, joined only to rows a person's own list returned",
	"internal/web/volumeusage.go listClaims":     "the claims behind the shared volume gauges, joined only to rows a person's own list returned",
	// The object a write is built on, read inside the write path and only after
	// the person was reviewed for the write: the data and manifest previews ask
	// `patch` and `get` first (edit.reviewBeforeReading), and actions.Preview and every row of a bulk act
	// (actions.Runner.one) ask the verb's own questions (actions.Needs) first.
	"internal/edit/edit.go live": "the object a write is built on, read only after the person is reviewed for the write",
	// The node a node-shell pod is pinned to, read to refuse a typo before the
	// pod is created: part of that write, which is reviewed (`create pods`),
	// and on the desktop only - the node shell is not registered under the
	// server profile.
	"internal/terminal/nodeshell.go Terminal.startNodeShell": "the node a node-shell write is built on, desktop only",
	// The desktop's port-forward manager asking whether the pod behind one of
	// its own tunnels still exists, on a timer, to tear the tunnel down. It
	// answers a yes or no to the console and shows a person nothing; the
	// manager is not registered under the server profile.
	"internal/forward/forward.go podChecker": "the forward manager's sweep of its own tunnels",
	// No extra reads an object through extension.Host.Client: what it hands
	// out answers discovery and nothing else (access.DiscoveryOnly), and
	// extras/boundary_test.go refuses k8s.io/client-go in an extra. What an
	// extra reads goes through Host's doors: List, reviewed for the person, or
	// ConfiguredSecret and ConfiguredService, read as the console (consoleReads).
}

// TestEveryReadAsTheConsoleIsListed: AsConsole is the one way to read with the
// console's rights under the server profile, so each use is named here with
// its reason, and a new one fails until it is.
func TestEveryReadAsTheConsoleIsListed(t *testing.T) {
	found := map[string]bool{}
	for _, pkg := range loadModule(t) {
		for i, file := range pkg.files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if f := calleeOf(pkg.info, call); f != nil && f.FullName() == asConsole {
						key := pkg.names[i] + " " + funcName(fn)
						found[key] = true
						if consoleReads[key] == "" {
							t.Errorf("%s: %s reads as the console, which no list here names. A read made "+
								"as the console is shown to nobody unreviewed only if what it fills is "+
								"reviewed on the way out; say so in consoleReads", pkg.fset.Position(call.Pos()), key)
						}
					}
					return true
				})
			}
		}
	}
	for key := range consoleReads {
		if !found[key] {
			t.Errorf("consoleReads lists %q, which no longer calls AsConsole; remove it", key)
		}
	}
}

// consoleReads is every function that reads as the console, with its reason.
var consoleReads = map[string]string{
	// The namespace dropdown's hold is shared by every person: filled as the
	// console, and each person is reviewed for `list namespaces` before they
	// are shown it (Server.mayListNamespaces).
	"internal/web/namespaces.go Server.namespaceAnswer": "the shared namespace hold, reviewed per person on the way out",
	// Reviews and no read: whether the console's own credentials may list a
	// kind, so the in-cluster menu leaves out the kinds every list of would be
	// refused. A list opened is still reviewed per person.
	"internal/web/listable.go Server.settle": "the in-cluster menu's question of the ServiceAccount's own list rights",
	// One metadata-only row of a kind, into the occupancy hold every person
	// is answered from. What a person is told of it is
	// their own review: of the list across every namespace, else of each
	// namespace (Server.kindOccupancy).
	"internal/web/navoccupancy.go Server.probeOccupancy": "the shared occupancy hold, reviewed per person on the way out",
	// Reviews and no read: whether the console's own credentials may do what
	// a person was not refused, so a control the API server would refuse the
	// ServiceAccount is drawn disabled saying so.
	// The act is still reviewed for the person at the write door.
	"internal/web/access.go reviewBoth": "a control's question of the ServiceAccount's own rights",
	// The tunnels to the console's own Prometheus. A probe's result and a
	// session are held per cluster for every person; what a person is shown
	// of the series is reviewed in internal/web (apiReads.mayReadObject), and
	// the rankings are narrowed by namespace (overview.narrowBoards).
	"internal/metrics/prom.go Metrics.probeTransport": "the console reaching its own monitoring backend",
	"internal/metrics/prom.go Metrics.OpenSession":    "the console reaching its own monitoring backend",
	// The tunnel an extension asks the console to dig for itself
	// (extension.Host.ForwardAsConsole), to a backend its configuration names:
	// argocd-server for the Argo CD diff, the KubeCron collector. The extension
	// asks the person's own reviewed reads first (the object and its
	// Application through Host.List, the CronJobs through Host.List, the log
	// through Host.MayReadLogs) and serves from the tunnel only what they
	// returned. The tunnel is recorded as the console's on the person's
	// behalf and closed when their request ends.
	"internal/web/hostconsole.go host.ForwardAsConsole": "an extension's tunnel to its own backend, served per person through their reviewed reads",
	// What an extension reads with the console's credentials
	// (extension.Host.ConfiguredSecret, ConfiguredService): only a Secret or a
	// Service the extension's Configured names from the console's own
	// configuration - argocd-server and its token, the KubeCron collector and
	// its token when the environment or the cluster's setting names it - never
	// a name read from the cluster. A Secret's read is recorded as the
	// console's on the person's behalf; its value is never served, only sent
	// to that backend.
	"internal/web/hostconfigured.go host.asConsoleFor": "an extension's configured backend and its token, named by the configuration alone",
}

// TestEveryHousekeepingWriteIsListed: access.Housekeeping stamps a request
// nobody was reviewed for, so the transport lets it go. Each use is named here
// with the reason it cannot be anybody's write, and a new one fails until it is
// - the same bargain as consoleReads, for the runtime half of the choke point.
func TestEveryHousekeepingWriteIsListed(t *testing.T) {
	found := map[string]bool{}
	for _, pkg := range loadModule(t) {
		for i, file := range pkg.files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if f := calleeOf(pkg.info, call); f != nil && f.FullName() == housekeeping {
						key := pkg.names[i] + " " + funcName(fn)
						found[key] = true
						if housekeepingWrites[key] == "" {
							t.Errorf("%s: %s stamps a request as housekeeping, which no list here names. "+
								"Housekeeping lets a write go that nobody was reviewed for; say in "+
								"housekeepingWrites why it cannot be anybody's", pkg.fset.Position(call.Pos()), key)
						}
					}
					return true
				})
			}
		}
	}
	for key := range housekeepingWrites {
		if !found[key] {
			t.Errorf("housekeepingWrites lists %q, which no longer calls access.Housekeeping; remove it", key)
		}
	}
}

// housekeepingWrites is every function that stamps a request as housekeeping,
// with its reason. They are the same two notAClusterWrite names for the walk:
// an exception to the review is an exception in both halves, or in neither.
var housekeepingWrites = map[string]string{
	"internal/terminal/nodeshell.go Terminal.sweepNodeShells": "the console removing its own node-shell pods",
	"internal/terminal/nodeshell.go Terminal.removeNodeShell": "the console removing its own node-shell pods",
}

// notAClusterWrite is every write-capable call the walk finds that is
// deliberately not reviewed, keyed by file and enclosing function, each with
// the reason. **Short on purpose**: an entry is a door, and it has to say why
// it is not one.
var notAClusterWrite = map[string]string{
	// The review itself. Asking whether a request may be made is the one
	// request that cannot be asked about first.
	"internal/access/kube.go clientsetReviews.SubjectAccessReview":     "the review itself",
	"internal/access/kube.go clientsetReviews.SelfSubjectAccessReview": "the review itself",
	// The node shell's clean-up of the pods this console itself created for a
	// shell. Refusing it would leave a privileged pod behind; it never deletes
	// anything a person named.
	"internal/terminal/nodeshell.go Terminal.sweepNodeShells": "the console removing its own node-shell pods",
	"internal/terminal/nodeshell.go Terminal.removeNodeShell": "the console removing its own node-shell pods",
}

// writeMethods are the client-go method names that change a cluster, or open
// a stream that can. Matched only on client-go types, so a same-named method
// elsewhere is not taken for one.
var writeMethods = map[string]bool{
	"Create": true, "Update": true, "UpdateStatus": true, "Patch": true,
	"Apply": true, "ApplyStatus": true, "Delete": true, "DeleteCollection": true,
	"Evict": true, "EvictV1": true, "EvictV1beta1": true,
	"UpdateScale": true, "ApplyScale": true, "Bind": true, "CreateToken": true,
	"UpdateApproval": true, "UpdateEphemeralContainers": true, "UpdateResize": true,
	"Replace": true, "DeleteWithOptions": true, "ApplyEphemeralContainers": true,
	"Stream": true, "StreamWithContext": true, "ForwardPorts": true,
}

// restWrites are the builder verbs of a raw REST request that change something.
var restWrites = map[string]bool{"Post": true, "Put": true, "Patch": true, "Delete": true}

const (
	gatePerform  = "(*github.com/thonicdev/kubetower/internal/access.Gate).Perform"
	gateRead     = "(*github.com/thonicdev/kubetower/internal/access.Gate).Read"
	accessList   = "github.com/thonicdev/kubetower/internal/access.List"
	accessEach   = "github.com/thonicdev/kubetower/internal/access.Each"
	accessListIn = "github.com/thonicdev/kubetower/internal/access.ListIn"
	asConsole    = "github.com/thonicdev/kubetower/internal/access.AsConsole"
	housekeeping = "github.com/thonicdev/kubetower/internal/access.Housekeeping"
)

// writeDoors are the functions a write may be made inside. Only Perform: a
// read door reviews a read verb, and a write inside one would be reviewed as
// a read.
var writeDoors = map[string]bool{gatePerform: true}

// readDoors are the functions a read may be made inside. Not Perform: a write's
// review says nothing about whether the person may read, and RBAC grants the
// two separately.
var readDoors = map[string]bool{gateRead: true, accessList: true, accessEach: true, accessListIn: true}

type checked struct {
	path  string
	fset  *token.FileSet
	files []*ast.File
	names []string // the files' paths relative to the module
	info  *types.Info
}

// exports is every package's export data, from one `go list -export`.
type exports struct{ files map[string]string }

func (e *exports) lookup(path string) (io.ReadCloser, error) {
	file := e.files[path]
	if file == "" {
		return nil, fmt.Errorf("no export data for %q", path)
	}
	return os.Open(file)
}

var (
	loadOnce sync.Once
	loaded   []*checked
	loadErr  error
	exported = &exports{files: map[string]string{}}
)

// loadModule type-checks every package of the module, once per test binary.
func loadModule(t *testing.T) []*checked {
	t.Helper()
	loadOnce.Do(func() { loaded, loadErr = typecheckModule() })
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return loaded
}

func goList(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %v: %v\n%s", args, err, stderr.String())
	}
	return out, nil
}

func typecheckModule() ([]*checked, error) {
	root := filepath.Join("..", "..")
	raw, err := goList(root, "-deps", "-export", "-f", "{{.ImportPath}}\t{{.Export}}", "./...")
	if err != nil {
		return nil, err
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	for scan.Scan() {
		if path, file, ok := strings.Cut(scan.Text(), "\t"); ok && file != "" {
			exported.files[path] = file
		}
	}
	raw, err = goList(root, "-f", "{{.ImportPath}}\t{{.Dir}}\t{{join .GoFiles \",\"}}", "./...")
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// **One FileSet and one importer for the whole module.** An importer per
	// package re-reads every dependency's export data for each package that
	// imports it - client-go's closure once per package - and keeps every copy
	// alive through the packages' type information: about 3 GiB under -race,
	// against 0.7 GiB shared. The gc importer caches what it has read, so each
	// dependency is read once. The walks compare functions by FullName, so
	// sharing the importer changes nothing they decide.
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", exported.lookup)
	var out []*checked
	scan = bufio.NewScanner(bytes.NewReader(raw))
	for scan.Scan() {
		parts := strings.SplitN(scan.Text(), "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		pkg := &checked{path: parts[0], fset: fset}
		for _, name := range strings.Split(parts[2], ",") {
			source := filepath.Join(parts[1], name)
			file, err := parser.ParseFile(fset, source, nil, 0)
			if err != nil {
				return nil, err
			}
			rel, _ := filepath.Rel(absRoot, source)
			pkg.files = append(pkg.files, file)
			pkg.names = append(pkg.names, filepath.ToSlash(rel))
		}
		if err := pkg.check(imp); err != nil {
			return nil, err
		}
		out = append(out, pkg)
	}
	return out, nil
}

// check type-checks the package through imp - the module's shared importer -
// or, when imp is nil, through one of its own on c.fset.
func (c *checked) check(imp types.Importer) error {
	c.info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	if imp == nil {
		imp = importer.ForCompiler(c.fset, "gc", exported.lookup)
	}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(c.path, c.fset, c.files, c.info); err != nil {
		return fmt.Errorf("type-checking %s: %w", c.path, err)
	}
	return nil
}

type site struct {
	pos       token.Position
	file      string
	function  string
	full      string // the enclosing function's identity
	call      string
	index     int
	inPerform bool
	gatedBy   string
}

func (s site) key() string {
	if s.index == 0 {
		return s.file + " " + s.function
	}
	return fmt.Sprintf("%s %s#%d", s.file, s.function, s.index)
}

// calleeOf is the function a call resolves to, or nil.
func calleeOf(info *types.Info, call *ast.CallExpr) *types.Func {
	fn := call.Fun
	// An explicitly instantiated generic function, `access.Each[int](...)`,
	// is the same function.
	switch index := fn.(type) {
	case *ast.IndexExpr:
		fn = index.X
	case *ast.IndexListExpr:
		fn = index.X
	}
	switch fun := fn.(type) {
	case *ast.Ident:
		f, _ := info.Uses[fun].(*types.Func)
		return f
	case *ast.SelectorExpr:
		f, _ := info.Uses[fun.Sel].(*types.Func)
		return f
	}
	return nil
}

// forwardsToADoor is whether fn hands its function parameters to a door and
// does nothing else with them: each use of each one is an argument of a door
// call, or sits inside a function literal that is. A function that also calls
// its closure directly is not a door.
func forwardsToADoor(info *types.Info, fn *ast.FuncDecl, doors map[string]bool) bool {
	if fn.Body == nil || fn.Type.Params == nil {
		return false
	}
	// A parameter of function type, by its type rather than its spelling: a
	// named function type (`type lister func(...)`) is one too.
	params := map[types.Object]bool{}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			obj := info.Defs[name]
			if obj == nil {
				continue
			}
			if _, ok := obj.Type().Underlying().(*types.Signature); ok {
				params[obj] = true
			}
		}
	}
	if len(params) == 0 {
		return false
	}
	handed := doorArguments(info, fn.Body, doors)
	passed, otherwise := 0, false
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		ident, ok := n.(*ast.Ident)
		if !ok || !params[info.Uses[ident]] {
			return true
		}
		if insideAny(stack, handed) {
			passed++
		} else {
			otherwise = true
		}
		return true
	})
	return passed > 0 && !otherwise
}

// doorArguments are the arguments of every door call under root that a door
// runs on the caller's behalf: function literals, and method values or
// function values named in place (`door(ctx, api.Pods(ns).List)`).
func doorArguments(info *types.Info, root ast.Node, doors map[string]bool) map[ast.Node]bool {
	handed := map[ast.Node]bool{}
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if f := calleeOf(info, call); f != nil && doors[f.FullName()] {
			for _, arg := range call.Args {
				switch arg.(type) {
				case *ast.FuncLit, *ast.SelectorExpr, *ast.Ident:
					handed[arg] = true
				}
			}
		}
		return true
	})
	return handed
}

// insideAny is whether any node of stack - the node itself included - is one of
// set.
func insideAny(stack []ast.Node, set map[ast.Node]bool) bool {
	for _, outer := range stack {
		if set[outer] {
			return true
		}
	}
	return false
}

// detector names a call site the walk is looking for, or reports none.
type detector func(info *types.Info, n ast.Node, stack []ast.Node) (string, bool)

// writeSites answers every write-capable call and method value in pkgs.
func writeSites(pkgs []*checked) []site { return sitesOf(pkgs, writeDoors, writeAt) }

// readSites answers every read-capable call and method value in pkgs.
func readSites(pkgs []*checked) []site { return sitesOf(pkgs, readDoors, readAt) }

// sitesOf finds every site detect names in pkgs, and says of each whether it is
// made inside one of roots - or inside a function that only forwards its
// closures to one, found to a fixed point so a door built on a door is one.
func sitesOf(pkgs []*checked, roots map[string]bool, detect detector) []site {
	doors := map[string]bool{}
	for name := range roots {
		doors[name] = true
	}
	for changed := true; changed; {
		changed = false
		for _, pkg := range pkgs {
			for _, file := range pkg.files {
				for _, decl := range file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok {
						continue
					}
					obj, ok := pkg.info.Defs[fn.Name].(*types.Func)
					if !ok || doors[obj.FullName()] || !forwardsToADoor(pkg.info, fn, doors) {
						continue
					}
					doors[obj.FullName()] = true
					changed = true
				}
			}
		}
	}

	performed := map[ast.Node]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.files {
			for node := range doorArguments(pkg.info, file, doors) {
				performed[node] = true
			}
		}
	}

	type use struct{ inPerform bool }
	uses := map[string][]use{}
	var sites []site
	for _, pkg := range pkgs {
		for i, file := range pkg.files {
			name := pkg.names[i]
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				function := funcName(fn)
				full := ""
				if obj, ok := pkg.info.Defs[fn.Name].(*types.Func); ok {
					full = obj.FullName()
				}
				count := 0
				var stack []ast.Node
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if n == nil {
						stack = stack[:len(stack)-1]
						return true
					}
					stack = append(stack, n)
					inside := insideAny(stack, performed)
					// **Every mention of a function is a use**, called or not:
					// a method value handed to a goroutine is a path to its body.
					if ident, ok := n.(*ast.Ident); ok {
						if f, ok := pkg.info.Uses[ident].(*types.Func); ok {
							uses[f.FullName()] = append(uses[f.FullName()], use{inPerform: inside})
						}
						return true
					}
					what, ok := detect(pkg.info, n, stack)
					if !ok {
						return true
					}
					sites = append(sites, site{
						pos: pkg.fset.Position(n.Pos()), file: name, function: function, full: full,
						call: what, index: count, inPerform: inside,
					})
					count++
					return true
				})
			}
		}
	}
	// A site outside a closure is still reviewed when the function holding it
	// is only ever used from inside one. One level, and every use.
	for i := range sites {
		found := uses[sites[i].full]
		if sites[i].inPerform || sites[i].full == "" || len(found) == 0 {
			continue
		}
		all := true
		for _, u := range found {
			all = all && u.inPerform
		}
		if all {
			sites[i].gatedBy = sites[i].full
		}
	}
	sort.Slice(sites, func(a, b int) bool { return sites[a].pos.String() < sites[b].pos.String() })
	return sites
}

// writeAt reports whether n - a selector, called or used as a value - is a
// write, and names it.
func writeAt(info *types.Info, n ast.Node, stack []ast.Node) (string, bool) {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	method, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || method.Pkg() == nil {
		return "", false
	}
	name := method.Name()
	// A client-go method, or any method of a write name whose signature takes
	// the API's own write options - which is what a local interface narrowing
	// a client (`podCreator`) looks like, and what a wrapper passes through.
	if !strings.HasPrefix(method.Pkg().Path(), "k8s.io/client-go/") &&
		!(writeMethods[name] && takesWriteOptions(method)) {
		return "", false
	}
	called := false
	if len(stack) >= 2 {
		if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == sel {
			called = true
		}
	}
	if method.Pkg().Path() == "k8s.io/client-go/rest" {
		switch {
		case restWrites[name]:
			if !called {
				return "a method value of the REST builder " + render(sel), true
			}
			return "a REST " + name + " built by " + render(sel), true
		case name == "Verb":
			if called {
				call := stack[len(stack)-2].(*ast.CallExpr)
				if len(call.Args) == 1 {
					if tv, ok := info.Types[call.Args[0]]; ok && tv.Value != nil &&
						strings.EqualFold(strings.Trim(tv.Value.ExactString(), `"`), "GET") {
						return "", false
					}
				}
				return "a REST request of a verb not known to be GET, built by " + render(sel), true
			}
			return "a method value of the REST builder " + render(sel), true
		}
		return "", false
	}
	if name == "Stream" && strings.Contains(render(sel), ".GetLogs(") {
		// A log stream is a read (`pods/log`), the read filter's question.
		return "", false
	}
	if !writeMethods[name] {
		return "", false
	}
	if !called {
		return "a method value of " + render(sel), true
	}
	return render(sel), true
}

// readMethods are the client-go method names that read a cluster's objects.
// Matched only on the cluster clients (readsACluster), so a cache's Get or a
// config's List is not taken for one.
var readMethods = map[string]bool{"List": true, "Get": true, "Watch": true, "GetLogs": true}

// readsACluster is whether a client-go package is one that sends requests to
// an API server for objects: the typed clients, the dynamic and metadata
// clients, and the raw REST client. Discovery is not: what a cluster serves is
// readable by every authenticated identity (`system:discovery`).
func readsACluster(path string) bool {
	return strings.HasPrefix(path, "k8s.io/client-go/kubernetes/typed/") ||
		path == "k8s.io/client-go/dynamic" || path == "k8s.io/client-go/metadata" ||
		path == "k8s.io/client-go/rest"
}

// readAt reports whether n - a selector, called or used as a value - reads a
// cluster, and names it. A raw REST request reads when it is built with `Get`,
// or with `Verb` and a literal `GET`; any other verb is the write walk's.
func readAt(info *types.Info, n ast.Node, stack []ast.Node) (string, bool) {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	method, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || method.Pkg() == nil || !readsACluster(method.Pkg().Path()) {
		return "", false
	}
	if sig, ok := method.Type().(*types.Signature); !ok || sig.Recv() == nil {
		return "", false
	}
	called := false
	if len(stack) >= 2 {
		if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == sel {
			called = true
		}
	}
	name := method.Name()
	if name == "Verb" && method.Pkg().Path() == "k8s.io/client-go/rest" && called {
		call := stack[len(stack)-2].(*ast.CallExpr)
		if len(call.Args) == 1 {
			if tv, ok := info.Types[call.Args[0]]; ok && tv.Value != nil &&
				strings.EqualFold(strings.Trim(tv.Value.ExactString(), `"`), "GET") {
				return "a REST GET built by " + render(sel), true
			}
		}
		return "", false
	}
	if !readMethods[name] {
		return "", false
	}
	if !called {
		return "a method value of " + render(sel), true
	}
	return render(sel), true
}

// takesWriteOptions is whether a method's parameters include one of
// `metav1.CreateOptions`, `UpdateOptions`, `PatchOptions`, `DeleteOptions` or
// `ApplyOptions`, by value or by pointer.
func takesWriteOptions(method *types.Func) bool {
	sig, ok := method.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		// A package function - edit.Delete - is a door's own entry point, and
		// the write inside it is what the walk judges.
		return false
	}
	for i := 0; i < sig.Params().Len(); i++ {
		t := sig.Params().At(i).Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "k8s.io/apimachinery/pkg/apis/meta/v1" {
			continue
		}
		switch named.Obj().Name() {
		case "CreateOptions", "UpdateOptions", "PatchOptions", "DeleteOptions", "ApplyOptions":
			return true
		}
	}
	return false
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if index, ok := recv.(*ast.IndexExpr); ok {
		recv = index.X
	}
	if ident, ok := recv.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func render(sel *ast.SelectorExpr) string {
	var b strings.Builder
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.Ident:
			b.WriteString(x.Name)
		case *ast.SelectorExpr:
			walk(x.X)
			b.WriteString("." + x.Sel.Name)
		case *ast.CallExpr:
			walk(x.Fun)
			b.WriteString("(…)")
		default:
			b.WriteString("…")
		}
	}
	walk(sel)
	return b.String()
}

// TestTheWalkRefusesAWriteOutsideAClosure is the walk run, typed, against code
// that breaks each rule, so a walk that stopped matching anything cannot pass.
func TestTheWalkRefusesAWriteOutsideAClosure(t *testing.T) {
	loadModule(t)
	src := `package fixture

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
)

var (
	gate  *access.Gate
	iface dynamic.ResourceInterface
	attrs access.Attributes
	opts  metav1.DeleteOptions
	rc    rest.Interface
)

func bare(ctx context.Context) { _ = iface.Delete(ctx, "a", opts) }

func wrapped(ctx context.Context) {
	gate.Perform(ctx, "prod", attrs, func(ctx context.Context) error { return iface.Delete(ctx, "a", opts) })
}

func helper(ctx context.Context) error { return iface.Delete(ctx, "b", opts) }
func caller(ctx context.Context) {
	gate.Perform(ctx, "prod", attrs, func(ctx context.Context) error { return helper(ctx) })
}
func leak(ctx context.Context) { _ = helper(ctx) }

func only(ctx context.Context) error { return iface.Delete(ctx, "c", opts) }
func gated(ctx context.Context) {
	gate.Perform(ctx, "prod", attrs, func(ctx context.Context) error { return only(ctx) })
}

func methodValue(ctx context.Context) { del := iface.Delete; _ = del(ctx, "d", opts) }

func restPost(ctx context.Context) { _ = rc.Post().Resource("pods").Do(ctx) }
func restVerb(ctx context.Context, v string) { _ = rc.Verb(v).Resource("pods").Do(ctx) }
func restGet(ctx context.Context) { _ = rc.Verb("GET").Resource("pods").Do(ctx) }

func restHeld(ctx context.Context) {
	req := rc.Post().Resource("pods")
	_ = req.Do(ctx)
}
func restPostValue(ctx context.Context) {
	post := rc.Post
	_ = post().Resource("pods").Do(ctx)
}
func restVerbValue(ctx context.Context) {
	verb := rc.Verb
	_ = verb("POST").Resource("pods").Do(ctx)
}

func door(ctx context.Context, do func(context.Context) error) error {
	_, err := gate.Perform(ctx, "prod", attrs, do)
	return err
}
func throughDoor(ctx context.Context) {
	_ = door(ctx, func(ctx context.Context) error { return iface.Delete(ctx, "e", opts) })
}

func twoFaced(ctx context.Context, do func(context.Context) error) error {
	if ctx == nil {
		return do(ctx)
	}
	_, err := gate.Perform(ctx, "prod", attrs, do)
	return err
}
func throughTwoFaced(ctx context.Context) {
	_ = twoFaced(ctx, func(ctx context.Context) error { return iface.Delete(ctx, "f", opts) })
}

type creator interface {
	Create(ctx context.Context, name string, opts metav1.CreateOptions) error
}

var narrowed creator

func throughNarrowed(ctx context.Context) { _ = narrowed.Create(ctx, "h", metav1.CreateOptions{}) }

type local struct{}

func (local) Perform(ctx context.Context, cluster string, a access.Attributes, do func(context.Context) error) error {
	return do(ctx)
}
func throughLocal(ctx context.Context) {
	_ = local{}.Perform(ctx, "prod", attrs, func(ctx context.Context) error { return iface.Delete(ctx, "g", opts) })
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := &checked{path: "example.invalid/fixture", fset: fset, files: []*ast.File{file}, names: []string{"fixture.go"}}
	if err := pkg.check(nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range writeSites([]*checked{pkg}) {
		got[s.function] = got[s.function] || s.inPerform || s.gatedBy != ""
	}
	want := map[string]bool{
		"bare": false, "wrapped": true, "helper": false, "only": true, "methodValue": false,
		"restPost": false, "restVerb": false, "throughDoor": true, "throughTwoFaced": false, "throughLocal": false,
		"throughNarrowed": false, "restHeld": false, "restPostValue": false, "restVerbValue": false,
	}
	for fn, reviewed := range want {
		if r, ok := got[fn]; !ok || r != reviewed {
			t.Errorf("%s: found=%t reviewed=%t, want reviewed=%t", fn, ok, r, reviewed)
		}
	}
	if _, ok := got["restGet"]; ok {
		t.Error("a REST GET was taken for a write")
	}
}

// TestTheReadWalkRefusesAReadOutsideADoor is the read walk run, typed, against
// code that breaks each of its rules - and against the shapes the tree uses
// that it must accept (a method value handed in place to a door through a
// function that only forwards it, as resources.Listed does).
func TestTheReadWalkRefusesAReadOutsideADoor(t *testing.T) {
	loadModule(t)
	src := `package fixture

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
)

var (
	gate  *access.Gate
	iface dynamic.ResourceInterface
	attrs access.Attributes
	rc    rest.Interface
)

type lister func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error)

func bareGet(ctx context.Context) { _, _ = iface.Get(ctx, "a", metav1.GetOptions{}) }

func readInside(ctx context.Context) {
	gate.Read(ctx, "prod", attrs, func(ctx context.Context) error {
		_, err := iface.Get(ctx, "a", metav1.GetOptions{})
		return err
	})
}

func readInPerform(ctx context.Context) {
	gate.Perform(ctx, "prod", attrs, func(ctx context.Context) error {
		_, err := iface.List(ctx, metav1.ListOptions{})
		return err
	})
}

func listed(ctx context.Context) {
	access.List(ctx, gate, "prod", attrs, nil, func(ctx context.Context) ([]int, error) {
		_, err := iface.List(ctx, metav1.ListOptions{})
		return nil, err
	})
}

func each(ctx context.Context) {
	access.Each[int](ctx, gate, "prod", attrs, nil, func(ctx context.Context, keep func(*int) bool) error {
		_, err := iface.List(ctx, metav1.ListOptions{})
		return err
	})
}

func methodValue(ctx context.Context) { list := iface.List; _, _ = list(ctx, metav1.ListOptions{}) }

func forward(ctx context.Context, list lister) error {
	_, _, err := access.List(ctx, gate, "prod", attrs, nil, func(ctx context.Context) ([]int, error) {
		_, err := list(ctx, metav1.ListOptions{})
		return nil, err
	})
	return err
}

func throughForward(ctx context.Context) { _ = forward(ctx, iface.List) }

func heldValue(ctx context.Context) { list := iface.List; _ = forward(ctx, list) }

func leaky(ctx context.Context, list lister) error {
	_, _ = list(ctx, metav1.ListOptions{})
	return forward(ctx, list)
}

func throughLeaky(ctx context.Context) { _ = leaky(ctx, iface.List) }

func restGet(ctx context.Context)     { _ = rc.Get().Resource("pods").Do(ctx) }
func restVerbGet(ctx context.Context) { _ = rc.Verb("GET").Resource("pods").Do(ctx) }
func restPost(ctx context.Context)    { _ = rc.Post().Resource("pods").Do(ctx) }

func restGetInside(ctx context.Context) {
	gate.Read(ctx, "prod", attrs, func(ctx context.Context) error { return rc.Get().Resource("pods").Do(ctx).Error() })
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := &checked{path: "example.invalid/fixture", fset: fset, files: []*ast.File{file}, names: []string{"fixture.go"}}
	if err := pkg.check(nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range readSites([]*checked{pkg}) {
		reviewed := s.inPerform || s.gatedBy != ""
		if prior, ok := got[s.function]; ok {
			reviewed = reviewed && prior
		}
		got[s.function] = reviewed
	}
	want := map[string]bool{
		"bareGet": false, "readInside": true, "readInPerform": false, "listed": true, "each": true,
		"methodValue": false, "throughForward": true, "heldValue": false, "throughLeaky": false,
		"restGet": false, "restVerbGet": false, "restGetInside": true,
	}
	for fn, reviewed := range want {
		if r, ok := got[fn]; !ok || r != reviewed {
			t.Errorf("%s: found=%t reviewed=%t, want reviewed=%t", fn, ok, r, reviewed)
		}
	}
	if _, ok := got["restPost"]; ok {
		t.Error("a REST POST was taken for a read")
	}
}

// TestTheTestHelperIsNotImportedByTheBinary: accesstest answers reviews from
// a function, and a production path holding one is the choke point deciding
// for itself.
func TestTheTestHelperIsNotImportedByTheBinary(t *testing.T) {
	for _, pkg := range loadModule(t) {
		if strings.HasSuffix(pkg.path, "/internal/access/accesstest") {
			continue
		}
		for i, file := range pkg.files {
			for _, imp := range file.Imports {
				if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/internal/access/accesstest") {
					t.Errorf("%s imports accesstest, which answers reviews without a cluster", pkg.names[i])
				}
			}
		}
	}
}
