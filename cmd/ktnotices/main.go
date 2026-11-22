// Command ktnotices writes THIRD_PARTY_NOTICES.txt: every third-party Go module
// linked into `cmd/kubetower` and every npm package bundled into its console,
// each with the license text it is published under.
//
// LICENSING.md says third-party notices are preserved, and the MIT, BSD and
// Apache-2.0 licenses most of the binary links ask for that. A hand-kept file
// would drift on the first dependency update, so this generates it from the
// build's own inputs:
//
//   - Go: `go list -deps ./cmd/kubetower` over every release target (the
//     union), with each module's license files from the module cache, at its
//     root and in every directory down to a linked package, plus the Go
//     distribution's license, since the runtime is linked into every binary.
//   - npm: the JSON web/scripts/third-party-notices.ts writes during
//     `pnpm build`, from the modules the bundle contains.
//
// A module or package with no license file is an error, never a gap.
//
// release.yml and the Dockerfile write the file into web/dist/ui/ before
// linking, so every binary embeds it and serves /THIRD_PARTY_NOTICES.txt, and
// copy it beside LICENSE in every archive and in the image.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// targets is every platform a release builds (release.yml's archives job).
// A module is listed if any of them links it.
var targets = []string{"windows/amd64", "darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"}

// licenceName is the shape of a license or notice file name: LICENSE,
// LICENSE.md, LICENSE-MIT, NOTICE.txt, LICENSE.libyaml. Any one extension is
// accepted, since a license for part of a module is often named after that
// part; isLicenceFile then refuses source and data extensions, so license.go is
// never read as a license. web/scripts/third-party-notices.ts applies the same
// rule.
var licenceName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|unlicen[cs]e)([-_][^.]*)?(\.([a-z0-9]+))?$`)

// sourceExtensions are the extensions that make a license-shaped name a
// program or a data file rather than a license.
var sourceExtensions = map[string]bool{
	"go": true, "s": true, "c": true, "h": true, "cc": true, "cpp": true, "hpp": true,
	"js": true, "mjs": true, "cjs": true, "ts": true, "mts": true, "cts": true, "tsx": true, "jsx": true,
	"py": true, "rb": true, "sh": true, "rs": true, "java": true, "proto": true,
	"json": true, "yaml": true, "yml": true, "toml": true, "mod": true, "sum": true,
}

// isLicenceFile reports whether a file name is a license or a notice.
func isLicenceFile(name string) bool {
	m := licenceName.FindStringSubmatch(name)
	return m != nil && !sourceExtensions[strings.ToLower(m[4])]
}

// component is one entry in the notices: a Go module or an npm package.
type component struct {
	ecosystem string // "Go" or "npm"
	name      string
	version   string
	licence   string // a short identifier for the index; the text below is what binds
	files     []licenceText
}

type licenceText struct {
	name string
	text string
}

// npmPackage is one entry of the JSON the Vite plugin writes.
type npmPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license"`
	Files   []struct {
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"files"`
}

func main() {
	var npmPath, out string
	flag.StringVar(&npmPath, "npm", "", "the bundled npm packages, as web/scripts/third-party-notices.ts writes them (required)")
	flag.StringVar(&out, "o", "", "where to write THIRD_PARTY_NOTICES.txt (required)")
	flag.Parse()
	if flag.NArg() > 0 {
		fail(fmt.Errorf("unexpected arguments %q", flag.Args()))
	}
	if npmPath == "" || out == "" {
		fail(errors.New("-npm and -o are both required"))
	}

	goParts, err := goComponents(goList("."))
	if err != nil {
		fail(err)
	}
	stdlib, err := goDistribution()
	if err != nil {
		fail(err)
	}
	npmParts, err := npmComponents(npmPath)
	if err != nil {
		fail(err)
	}

	text := render(stdlib, goParts, npmParts)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(out, text, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("third-party notices: the Go distribution, %d Go modules, %d npm packages -> %s\n",
		len(goParts), len(npmParts), out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ktnotices: "+err.Error())
	os.Exit(1)
}

// listedPackage is one line of `go list -deps` output: a linked package, the
// module it belongs to, and both directories.
type listedPackage struct {
	path, version, dir string // the module
	pkgDir             string // the package, inside dir
}

// goList returns what lists, for one target, the packages `cmd/kubetower`
// links outside the main module. dir is the repository root. CGO_ENABLED=0, as
// ktbuild builds.
func goList(dir string) func(goos, goarch string) ([]listedPackage, error) {
	return func(goos, goarch string) ([]listedPackage, error) {
		const format = `{{with .Module}}{{if not .Main}}{{.Path}}` + "\t" +
			`{{with .Replace}}{{.Version}}{{else}}{{.Version}}{{end}}` + "\t" +
			`{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}` + "\t" +
			`{{$.Dir}}{{end}}{{end}}`
		cmd := exec.Command("go", "list", "-deps", "-f", format, "./cmd/kubetower")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list -deps ./cmd/kubetower for %s/%s: %w\n%s", goos, goarch, err, stderr.String())
		}
		var pkgs []listedPackage
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) != 4 {
				return nil, fmt.Errorf("go list printed %q, which is not path, version and two directories", line)
			}
			pkgs = append(pkgs, listedPackage{path: parts[0], version: parts[1], dir: parts[2], pkgDir: parts[3]})
		}
		return pkgs, nil
	}
}

// linkedModule is one module and the directories of the packages linked from
// it, relative to its root.
type linkedModule struct {
	path, version, dir string
	dirs               map[string]bool
}

// goComponents is the union over every target of the modules linked, each
// with its license files.
//
// A license file below a module's root covers the packages under it (forked or
// vendored code), so every directory from each linked package up to the root
// is read, not the root alone.
func goComponents(list func(goos, goarch string) ([]listedPackage, error)) ([]component, error) {
	seen := map[string]*linkedModule{}
	for _, target := range targets {
		goos, goarch, _ := strings.Cut(target, "/")
		pkgs, err := list(goos, goarch)
		if err != nil {
			return nil, err
		}
		for _, p := range pkgs {
			key := p.path + "@" + p.version
			m := seen[key]
			if m == nil {
				if p.dir == "" {
					return nil, fmt.Errorf("%s %s is not in the module cache; run `go mod download` first", p.path, p.version)
				}
				m = &linkedModule{path: p.path, version: p.version, dir: p.dir, dirs: map[string]bool{}}
				seen[key] = m
			}
			rel, err := filepath.Rel(m.dir, p.pkgDir)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("package directory %s is not inside its module %s at %s", p.pkgDir, p.path, m.dir)
			}
			for {
				m.dirs[filepath.ToSlash(rel)] = true
				if rel == "." {
					break
				}
				rel = filepath.Dir(rel)
			}
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("go list found no third-party module in ./cmd/kubetower, which cannot be right")
	}
	var parts []component
	var bare []string
	for _, m := range seen {
		dirs := make([]string, 0, len(m.dirs))
		for d := range m.dirs {
			dirs = append(dirs, d)
		}
		// The root first, then the nested directories in path order.
		sort.Slice(dirs, func(i, j int) bool {
			if (dirs[i] == ".") != (dirs[j] == ".") {
				return dirs[i] == "."
			}
			return dirs[i] < dirs[j]
		})
		var files []licenceText
		for _, d := range dirs {
			found, err := readLicences(filepath.Join(m.dir, filepath.FromSlash(d)))
			if err != nil {
				return nil, err
			}
			for _, f := range found {
				if d != "." {
					f.name = d + "/" + f.name
				}
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			bare = append(bare, m.path+"@"+m.version)
			continue
		}
		parts = append(parts, component{
			ecosystem: "Go", name: m.path, version: m.version, licence: classify(files), files: files,
		})
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		return nil, fmt.Errorf("linked Go modules with no license file at their root or above a linked package, "+
			"so the notices would omit them: %s", strings.Join(bare, ", "))
	}
	sortComponents(parts)
	return parts, nil
}

// goDistribution is the Go runtime and standard library, linked into every
// binary under the Go project's own license.
func goDistribution() (component, error) {
	raw, err := exec.Command("go", "env", "GOROOT", "GOVERSION").Output()
	if err != nil {
		return component{}, fmt.Errorf("go env GOROOT GOVERSION: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		return component{}, fmt.Errorf("go env printed %q, not a GOROOT and a GOVERSION", raw)
	}
	goroot, version := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	files, err := readLicences(goroot)
	if err != nil {
		return component{}, err
	}
	if len(files) == 0 {
		return component{}, fmt.Errorf("no LICENSE in GOROOT %s", goroot)
	}
	return component{
		ecosystem: "Go", name: "Go runtime and standard library", version: version,
		licence: classify(files), files: files,
	}, nil
}

// readLicences reads every license or notice file at the root of dir, in name
// order. A directory with none is not an error here; the caller decides.
func readLicences(dir string) ([]licenceText, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s for its license: %w", dir, err)
	}
	var files []licenceText
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isLicenceFile(entry.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files = append(files, licenceText{name: entry.Name(), text: normalise(string(raw))})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func npmComponents(path string) ([]component, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the bundled npm packages (written by `pnpm build`, web/scripts/third-party-notices.ts): %w", err)
	}
	var pkgs []npmPackage
	if err := json.Unmarshal(raw, &pkgs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s lists no npm package, which cannot be right for a React console", path)
	}
	var parts []component
	for _, p := range pkgs {
		if p.Name == "" || p.Version == "" || len(p.Files) == 0 {
			return nil, fmt.Errorf("%s: %q@%q has no name, version or license file", path, p.Name, p.Version)
		}
		c := component{ecosystem: "npm", name: p.Name, version: p.Version, licence: p.License}
		for _, f := range p.Files {
			c.files = append(c.files, licenceText{name: f.Name, text: normalise(f.Text)})
		}
		if c.licence == "" {
			c.licence = classify(c.files)
		}
		parts = append(parts, c)
	}
	sortComponents(parts)
	return parts, nil
}

func sortComponents(parts []component) {
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].name != parts[j].name {
			return parts[i].name < parts[j].name
		}
		return parts[i].version < parts[j].version
	})
}

// normalize makes a license text's line endings and trailing space the file's
// own, so the output does not depend on how a module was checked out.
func normalise(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// classify names the license for the index, from the text. It is a reader's
// convenience: the text printed below each entry is what applies, and an
// unrecognized one says so rather than guessing.
func classify(files []licenceText) string {
	var found []string
	add := func(id string) {
		for _, f := range found {
			if f == id {
				return
			}
		}
		found = append(found, id)
	}
	for _, f := range files {
		if strings.HasPrefix(strings.ToUpper(f.name), "NOTICE") {
			continue
		}
		t := strings.Join(strings.Fields(f.text), " ")
		switch {
		case strings.Contains(t, "GNU AFFERO GENERAL PUBLIC LICENSE"):
			add("AGPL")
		case strings.Contains(t, "GNU LESSER GENERAL PUBLIC LICENSE"):
			add("LGPL")
		case strings.Contains(t, "GNU GENERAL PUBLIC LICENSE"):
			add("GPL")
		case strings.Contains(t, "Mozilla Public License") && strings.Contains(t, "2.0"):
			add("MPL-2.0")
		case strings.Contains(t, "Apache License") && strings.Contains(t, "Version 2.0"):
			add("Apache-2.0")
		case strings.Contains(t, "Permission is hereby granted, free of charge"):
			add("MIT")
		case strings.Contains(t, "Redistribution and use in source and binary forms"):
			if strings.Contains(t, "Neither the name") || strings.Contains(t, "names of its contributors") ||
				strings.Contains(t, "name of the copyright holder") {
				add("BSD-3-Clause")
			} else {
				add("BSD-2-Clause")
			}
		case strings.Contains(t, "Permission to use, copy, modify, and/or distribute this software"),
			strings.Contains(t, "Permission to use, copy, modify, and distribute this software for any purpose with or without fee"):
			add("ISC")
		}
	}
	if len(found) == 0 {
		return "see text"
	}
	return strings.Join(found, ", ")
}

const rule = "================================================================================"

// render is the whole file. Deterministic: the same inputs give the same bytes.
func render(stdlib component, goParts, npmParts []component) []byte {
	var b strings.Builder
	b.WriteString("KubeTower - third-party notices\n\n")
	b.WriteString("KubeTower itself is licensed under the GNU Affero General Public License,\n")
	b.WriteString("version 3 only (see LICENSE and LICENSING.md, which travel with it).\n\n")
	b.WriteString("The kubetower binary is built with the third-party software listed below.\n")
	b.WriteString("Each component is under its own license, whose text follows its entry;\n")
	b.WriteString("nothing here relicenses any of it. The license column of the index is a\n")
	b.WriteString("reader's summary read from that text; the text is what applies.\n\n")
	b.WriteString("Go modules are those linked into cmd/kubetower for any target a release\n")
	fmt.Fprintf(&b, "builds (%s),\n", strings.Join(targets, ", "))
	b.WriteString("plus the Go runtime and standard library, which every Go binary links.\n")
	b.WriteString("npm packages are those bundled into the web console the binary serves.\n")
	b.WriteString("Generated by cmd/ktnotices; do not edit by hand.\n\n")
	fmt.Fprintf(&b, "Go modules: %d, npm packages: %d\n\n", len(goParts), len(npmParts))

	all := append([]component{stdlib}, goParts...)
	all = append(all, npmParts...)
	b.WriteString("Index\n\n")
	for _, c := range all {
		fmt.Fprintf(&b, "  %-4s %s %s  [%s]\n", c.ecosystem, c.name, c.version, c.licence)
	}
	// A text already printed is named rather than repeated: dozens of modules
	// carry the same Apache-2.0 text.
	printed := map[string]string{}
	for _, c := range all {
		fmt.Fprintf(&b, "\n%s\n%s: %s %s\nLicense: %s\n", rule, c.ecosystem, c.name, c.version, c.licence)
		for _, f := range c.files {
			if first, ok := printed[f.text]; ok {
				fmt.Fprintf(&b, "\n--- %s: the same text as printed above for %s ---\n", f.name, first)
				continue
			}
			printed[f.text] = c.name + " " + c.version
			fmt.Fprintf(&b, "\n--- %s ---\n\n%s\n", f.name, f.text)
		}
	}
	return []byte(b.String())
}
