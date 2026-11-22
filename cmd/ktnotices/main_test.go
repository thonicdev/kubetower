package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mitText = `MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software.`

const apacheText = `                                 Apache License
                           Version 2.0, January 2004`

func TestLicenceFileNames(t *testing.T) {
	for _, name := range []string{
		"LICENSE", "LICENSE.md", "LICENSE.txt", "license", "LICENSE", "COPYING",
		"NOTICE", "NOTICE.txt", "LICENSE-APACHE", "LICENSE-MIT", "UNLICENSE", "LICENSE.libyaml",
	} {
		if !isLicenceFile(name) {
			t.Errorf("%q is a license file and is not matched", name)
		}
	}
	for _, name := range []string{"licensed.go", "LICENSES", "README.md", "go.mod", "license_test.go", "license.go", "LICENSE.json", "notice.ts"} {
		if isLicenceFile(name) {
			t.Errorf("%q is not a license file and is matched", name)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		files []licenceText
		want  string
	}{
		{[]licenceText{{"LICENSE", mitText}}, "MIT"},
		{[]licenceText{{"LICENSE", apacheText}, {"NOTICE", "This product includes software developed at Example."}}, "Apache-2.0"},
		{[]licenceText{{"LICENSE", "Redistribution and use in source and binary forms ... Neither the name of Google"}}, "BSD-3-Clause"},
		{[]licenceText{{"LICENSE", "Redistribution and use in source and binary forms, with or without"}}, "BSD-2-Clause"},
		{[]licenceText{{"LICENSE-APACHE", apacheText}, {"LICENSE-MIT", mitText}}, "Apache-2.0, MIT"},
		{[]licenceText{{"LICENSE", "All rights reserved, in words nobody wrote a rule for."}}, "see text"},
	} {
		if got := classify(tc.files); got != tc.want {
			t.Errorf("classify(%v) = %q, want %q", tc.files, got, tc.want)
		}
	}
}

// module writes a fake module directory holding the given files.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGoComponentsIsTheUnionOfEveryTarget(t *testing.T) {
	shared := module(t, map[string]string{"LICENSE": mitText})
	winOnly := module(t, map[string]string{"LICENSE": apacheText, "main.go": "package x"})
	asked := 0
	list := func(goos, goarch string) ([]listedPackage, error) {
		asked++
		mods := []listedPackage{{"example.com/shared", "v1.0.0", shared, shared}}
		if goos == "windows" {
			mods = append(mods, listedPackage{"example.com/windows", "v0.1.0", winOnly, winOnly})
		}
		return mods, nil
	}
	parts, err := goComponents(list)
	if err != nil {
		t.Fatal(err)
	}
	if asked != len(targets) {
		t.Errorf("listed %d targets, want every one of the %d a release builds", asked, len(targets))
	}
	if len(parts) != 2 || parts[0].name != "example.com/shared" || parts[1].name != "example.com/windows" {
		t.Fatalf("got %+v, want the shared module once and the Windows-only one", parts)
	}
	if len(parts[1].files) != 1 || parts[1].files[0].name != "LICENSE" {
		t.Errorf("the Windows-only module's files are %+v, want its LICENSE alone", parts[1].files)
	}
}

// A module with no license file is refused by name. A notices file that skips
// one silently is what this tool exists to prevent.
func TestGoComponentsRefusesAModuleWithNoLicence(t *testing.T) {
	bare := module(t, map[string]string{"main.go": "package x"})
	list := func(goos, goarch string) ([]listedPackage, error) {
		return []listedPackage{{"example.com/bare", "v1.2.3", bare, bare}}, nil
	}
	_, err := goComponents(list)
	if err == nil || !strings.Contains(err.Error(), "example.com/bare@v1.2.3") {
		t.Fatalf("got %v, want a refusal naming example.com/bare@v1.2.3", err)
	}
}

// A license below the module root, on the way from a linked package up to it,
// is read and named by its path; one beside an unlinked package is not.
func TestGoComponentsReadsLicencesBetweenAPackageAndItsRoot(t *testing.T) {
	root := module(t, map[string]string{"LICENSE": apacheText})
	forked := filepath.Join(root, "third_party", "forked", "golang")
	unlinked := filepath.Join(root, "third_party", "unlinked")
	for dir, text := range map[string]string{forked: "Copyright 2009 The Go Authors.", unlinked: mitText} {
		if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list := func(goos, goarch string) ([]listedPackage, error) {
		return []listedPackage{
			{"example.com/k8s", "v0.1.0", root, root},
			{"example.com/k8s", "v0.1.0", root, filepath.Join(forked, "pkg")},
		}, nil
	}
	parts, err := goComponents(list)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range parts[0].files {
		names = append(names, f.name)
	}
	if got := strings.Join(names, ","); got != "LICENSE,third_party/forked/golang/LICENSE" {
		t.Errorf("files %s, want the root LICENSE then third_party/forked/golang/LICENSE, and nothing from the unlinked directory", got)
	}
}

func TestGoComponentsRefusesAPackageOutsideItsModule(t *testing.T) {
	root := module(t, map[string]string{"LICENSE": mitText})
	elsewhere := t.TempDir()
	_, err := goComponents(func(string, string) ([]listedPackage, error) {
		return []listedPackage{{"example.com/m", "v1.0.0", root, elsewhere}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "not inside its module") {
		t.Fatalf("got %v, want a refusal", err)
	}
}

func TestGoComponentsPassesOnAListFailure(t *testing.T) {
	boom := errors.New("go list failed")
	_, err := goComponents(func(string, string) ([]listedPackage, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the list's own error", err)
	}
}

func TestNpmComponentsRefusesAPackageWithNoLicence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm.json")
	if err := os.WriteFile(path, []byte(`[{"name":"left-pad","version":"1.3.0","license":"WTFPL","files":[]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := npmComponents(path); err == nil || !strings.Contains(err.Error(), "left-pad") {
		t.Fatalf("got %v, want a refusal naming left-pad", err)
	}
}

// The same inputs give the same bytes, the index names every component, and a
// license text shared by two components is printed once.
func TestRenderIsDeterministicAndPrintsASharedTextOnce(t *testing.T) {
	stdlib := component{ecosystem: "Go", name: "Go runtime and standard library", version: "go1.0", licence: "BSD-3-Clause",
		files: []licenceText{{"LICENSE", "Copyright The Go Authors."}}}
	goParts := []component{
		{ecosystem: "Go", name: "example.com/a", version: "v1.0.0", licence: "Apache-2.0", files: []licenceText{{"LICENSE", apacheText}}},
		{ecosystem: "Go", name: "example.com/b", version: "v2.0.0", licence: "Apache-2.0", files: []licenceText{{"LICENSE.txt", apacheText}}},
	}
	npmParts := []component{
		{ecosystem: "npm", name: "react", version: "19.0.0", licence: "MIT", files: []licenceText{{"LICENSE", mitText}}},
	}
	first := string(render(stdlib, goParts, npmParts))
	if second := string(render(stdlib, goParts, npmParts)); first != second {
		t.Fatal("two renders of the same inputs differ")
	}
	for _, want := range []string{
		"Go modules: 2, npm packages: 1",
		"Go   example.com/a v1.0.0  [Apache-2.0]",
		"npm  react 19.0.0  [MIT]",
		"--- LICENSE.txt: the same text as printed above for example.com/a v1.0.0 ---",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("the notices lack %q", want)
		}
	}
	if n := strings.Count(first, "Version 2.0, January 2004"); n != 1 {
		t.Errorf("the shared Apache text is printed %d times, want once", n)
	}
}

// The real module graph: every module a release links has a license file.
// This is the check that turns red on the pull request that adds a dependency
// publishing none, rather than on the tag.
func TestEveryLinkedModuleHasALicence(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the module graph for every release target; skipped under -short")
	}
	parts, err := goComponents(goList(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if p.ecosystem != "Go" || len(p.files) == 0 {
			t.Errorf("%+v is not a Go module with a license", p)
		}
	}
	// Two modules every build links, as a floor: an answer without them is
	// an empty graph, not a clean one.
	for _, want := range []string{"k8s.io/client-go", "golang.org/x/net"} {
		found := false
		for _, p := range parts {
			found = found || p.name == want
		}
		if !found {
			t.Errorf("%s is not among the %d modules listed, so the listing is not the real graph", want, len(parts))
		}
	}
	// A license below a module's root, above a linked package: forked Go code with
	// its own copyright line, which a root-only read would leave out.
	for _, want := range []struct{ module, file string }{
		{"k8s.io/apimachinery", "third_party/forked/golang/LICENSE"},
		{"k8s.io/client-go", "third_party/forked/golang/LICENSE"},
		// A license named after the part it covers: the MIT license of the
		// libyaml code ported into yaml/v2.
		{"go.yaml.in/yaml/v2", "LICENSE.libyaml"},
	} {
		found := false
		for _, p := range parts {
			for _, f := range p.files {
				found = found || (p.name == want.module && f.name == want.file)
			}
		}
		if !found {
			t.Errorf("%s's %s is not in the notices", want.module, want.file)
		}
	}
}
