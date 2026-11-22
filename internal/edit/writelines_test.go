package edit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEveryWriteLineGoesThroughTheOneThatNamesWhoAsked is structural, and for
// the reason docs/SERVER-MODE-THREAT-MODEL.md gives for its choke point: a
// behavioral test of one write path proves that path names the person; only a
// walk of the tree proves there is no `write:` line that does not.
//
// Every string literal in non-test Go under `internal/` that begins `write: `
// must be the format of a `Logf(ctx, "write: ...", ...)` call on the Writer -
// which renders through identity.Line and appends `who=... via=...` from ctx.
// A `write:` line written any other way is a change to a cluster whose author
// the log cannot name.
//
// **What it proves, stated exactly**: no `write:` literal is written any other
// way. It does not prove that every change to a cluster writes a `write:` line
// - a path with no line at all is invisible to it - and exec and port-forward,
// which are not writes to an object, have their own attributed lines and their
// own tests in internal/terminal.
//
// Not any `X.Logf(<ident>, "write: ...")`: a `log.Logf(something, "write: ...")`
// on some other type would pass that. The call must be the Writer's - `w.Logf` inside
// internal/edit, where `w` is the receiver, or `<x>.writer.Logf` in the
// packages that hold one - and its first argument must be the request's `ctx`.
func TestEveryWriteLineGoesThroughTheOneThatNamesWhoAsked(t *testing.T) {
	root := filepath.Join("..")
	fset := token.NewFileSet()
	found := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// Every literal that sits in the right place, so the literals that do
		// not can be named.
		blessed := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Logf" || !isTheWriter(sel.X, file.Name.Name) {
				return true
			}
			if ctx, isIdent := call.Args[0].(*ast.Ident); !isIdent || ctx.Name != "ctx" {
				return true
			}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok {
				blessed[lit] = true
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(value, "write: ") {
				return true
			}
			found++
			if !blessed[lit] {
				t.Errorf("%s: a `write:` line that is not the format of Logf(ctx, ...), so it cannot "+
					"name who asked: %s", fset.Position(lit.Pos()), value)
			}
			// **And every one says what the access review decided and what
			// happened**: a record of who asked that does not say whether
			// they were allowed, or whether it worked, does not answer the
			// question it will be asked.
			if !strings.Contains(value, "decision=") || !strings.Contains(value, "outcome=") {
				t.Errorf("%s: a `write:` line without decision= and outcome=: %s", fset.Position(lit.Pos()), value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	// Ten today: set key, apply manifest, delete, evict, patch, a drain's
	// eviction, trigger, install - each written on success by its caller - and
	// the refused and the failed commit, written by internal/edit's door for
	// every one of them. Fewer means the walk is broken or a path went away
	// unnoticed; more means a new write path, which this test has then checked.
	if found < 10 {
		t.Fatalf("found %d `write:` lines under internal/, expected at least 10: the walk is not "+
			"seeing the tree", found)
	}
	t.Logf("%d `write:` lines, each through Logf(ctx, ...)", found)
}

// isTheWriter is whether a Logf receiver is the edit.Writer: `w` inside this
// package, where every Writer method names its receiver `w`, or a field called
// `writer` in the packages that hold one (actions.Runner, prereq.Installer).
func isTheWriter(recv ast.Expr, pkg string) bool {
	switch x := recv.(type) {
	case *ast.Ident:
		return pkg == "edit" && x.Name == "w"
	case *ast.SelectorExpr:
		return x.Sel.Name == "writer"
	}
	return false
}

// TestTheWalkRefusesALineOnAnythingButTheWriter is a mutation a lax walk
// would pass, run against the walk itself.
func TestTheWalkRefusesALineOnAnythingButTheWriter(t *testing.T) {
	src := `package actions
func f(ctx any) {
	other.Logf(ctx, "write: cluster=%q", "prod")
	r.writer.Logf(background, "write: cluster=%q", "prod")
	r.writer.Logf(ctx, "write: cluster=%q", "prod")
}`
	file, err := parser.ParseFile(token.NewFileSet(), "f.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Logf" || !isTheWriter(sel.X, file.Name.Name) {
			return true
		}
		if ctx, isIdent := call.Args[0].(*ast.Ident); isIdent && ctx.Name == "ctx" {
			accepted = append(accepted, "ok")
		}
		return true
	})
	if len(accepted) != 1 {
		t.Errorf("the walk accepted %d of the three calls; only r.writer.Logf(ctx, ...) is the Writer's", len(accepted))
	}
}
