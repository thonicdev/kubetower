package access

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// reverseProxyNames are the parts of net/http/httputil that forward a request
// the caller did not shape: a ReverseProxy (built by hand or by
// NewSingleHostReverseProxy) and the ProxyRequest its Rewrite hook receives.
var reverseProxyNames = map[string]bool{
	"ReverseProxy":              true,
	"NewSingleHostReverseProxy": true,
	"ProxyRequest":              true,
}

// TestNoGenericReverseProxy: no production file of the module forwards
// requests through httputil's reverse proxy. A proxy towards an API server lets
// the browser choose what the backend calls, and every request it relays
// bypasses the review this package holds; endpoints are use cases instead.
//
// **What it reads.** Every .go file under the module root that is not a test,
// parsed with go/parser whatever its build constraints, extras/ included. A
// dot import of net/http/httputil is refused outright, since its names can no
// longer be told from the file's own. Tests may proxy: a test relay is how the
// launch handoff is shown to hold against one.
func TestNoGenericReverseProxy(t *testing.T) {
	fset := token.NewFileSet()
	files := 0
	var found []string
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "testdata", "vendor", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files++
		rel, _ := filepath.Rel("../..", path)
		for _, use := range reverseProxyUses(fset, file) {
			found = append(found, filepath.ToSlash(rel)+":"+use)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	sort.Strings(found)
	for _, use := range found {
		t.Errorf("%s uses httputil's reverse proxy. Nothing in the console relays requests to "+
			"another server on the browser's behalf: write an endpoint for the use case instead", use)
	}
	// The floor: a walk that read nothing would pass. The module holds hundreds
	// of non-test .go files.
	if files < 200 {
		t.Fatalf("read %d non-test .go files; the module holds hundreds, so the walk is not seeing it", files)
	}
	t.Logf("read %d non-test .go files", files)
}

// The detector itself catches each form, so a green walk is not a detector
// that sees nothing.
func TestReverseProxyUsesAreFound(t *testing.T) {
	for name, src := range map[string]string{
		"constructor": `package p
import "net/http/httputil"
var _ = httputil.NewSingleHostReverseProxy(nil)`,
		"literal": `package p
import "net/http/httputil"
var _ = &httputil.ReverseProxy{}`,
		"rewrite": `package p
import hu "net/http/httputil"
func rewrite(r *hu.ProxyRequest) {}`,
		"dot import": `package p
import . "net/http/httputil"
var _ = DumpRequest`,
	} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name+".go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if uses := reverseProxyUses(fset, file); len(uses) == 0 {
			t.Errorf("%s: the reverse proxy in this source was not found:\n%s", name, src)
		}
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dump.go", `package p
import "net/http/httputil"
var _ = httputil.DumpRequest`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if uses := reverseProxyUses(fset, file); len(uses) != 0 {
		t.Errorf("httputil.DumpRequest is not a proxy, yet was reported: %v", uses)
	}
}

// reverseProxyUses lists, as `line: what`, each place file names httputil's
// reverse proxy.
func reverseProxyUses(fset *token.FileSet, file *ast.File) []string {
	var names []string
	var uses []string
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "net/http/httputil" {
			continue
		}
		switch {
		case imp.Name == nil:
			names = append(names, "httputil")
		case imp.Name.Name == ".":
			uses = append(uses, strconv.Itoa(fset.Position(imp.Pos()).Line)+": dot import of net/http/httputil")
		case imp.Name.Name != "_":
			names = append(names, imp.Name.Name)
		}
	}
	if len(names) == 0 {
		return uses
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !reverseProxyNames[sel.Sel.Name] {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok {
			for _, name := range names {
				if pkg.Name == name {
					uses = append(uses, strconv.Itoa(fset.Position(sel.Pos()).Line)+": httputil."+sel.Sel.Name)
				}
			}
		}
		return true
	})
	return uses
}
