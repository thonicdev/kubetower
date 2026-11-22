package overview

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The panel label is prose, and a second package matches on it.
//
// A task that panics degrades its panel and the page says so in
// `notShown`, while the panel's own envelope still reads `{"state":"empty"}` -
// so `internal/web` maps each label to the drawn panels that failure spoils, in
// order to mark them. That map keys on the constants declared beside PanelError.
//
// A `degrade(&out.Degraded, "Ingresses", err)` written as a literal compiles,
// runs, lands in `notShown`, and marks nothing, for one panel, with the page
// still looking as though it worked. This is what refuses the literal.
//
// Parsed from the package's own source rather than exercised, for the reason
// TestEveryGatheredTaskNamesWhereItsFailureGoes gives: what is being asserted is
// a property of every call site including the ones no test reaches.
func TestEveryDegradedPanelIsNamedByAConstant(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(f os.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading this package's own source: %v", err)
	}
	pkg := pkgs["overview"]
	if pkg == nil || len(pkg.Files) < 5 {
		// An empty parse would pass every assertion below, which is the one way
		// this check could be worse than not existing.
		t.Fatalf("parsed %d source files of package overview; it has more than that, so this check is "+
			"not reading the package it is about", len(pkg.Files))
	}

	// The constants, from the source rather than from a second list here.
	declared := map[string]bool{}
	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for _, name := range spec.Names {
				if strings.HasPrefix(name.Name, "Panel") && name.Name != "PanelError" {
					declared[name.Name] = true
				}
			}
			return true
		})
	}
	if len(declared) == 0 {
		t.Fatal("no Panel* constants found; this check is reading nothing")
	}

	var offences []string
	used := map[string]bool{}
	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "degrade" || len(call.Args) != 3 {
				return true
			}
			arg, ok := call.Args[1].(*ast.Ident)
			if !ok || !declared[arg.Name] {
				offences = append(offences, fmt.Sprintf("%s: degrade names its panel with a literal "+
					"rather than one of the Panel* constants - internal/web keys its spoilt-panel map on "+
					"those, so this failure would reach notShown and mark no panel at all",
					fset.Position(call.Pos())))
				return true
			}
			used[arg.Name] = true
			return true
		})
	}
	for _, o := range offences {
		t.Error(o)
	}

	// A constant nobody degrades with is dead, and a constant missing from
	// DegradablePanels is invisible to the caller that checks its own map
	// against it. Both are counted from the source, never recalled.
	if len(DegradablePanels) != len(declared) {
		t.Errorf("DegradablePanels holds %d panels and the source declares %d Panel* constants; "+
			"a constant that is not in that slice is one internal/web is never asked to map",
			len(DegradablePanels), len(declared))
	}
	for name := range declared {
		if !used[name] {
			t.Errorf("%s is declared and nothing degrades with it", name)
		}
	}
}
