package web

import (
	"io/fs"
	"strings"

	webassets "github.com/thonicdev/kubetower/web"
)

// Bundle is the front end this console serves at `/`: Options.UI when given,
// the embedded bundle otherwise. `SecurityHeadersFor` hashes the shell's inline
// script from the same value, so the policy always matches the served document.
func (s *Server) Bundle() fs.FS { return bundleFS(s.ui) }

// bundleFS resolves a caller's front end into the layout this package reads. Nil
// is the embedded tree. A caller's bundle is the root of a Vite build and is
// mounted under the same `dist/ui/` prefix, so every path below is the embedded
// tree's and nothing else in this package knows there are two.
func bundleFS(ui fs.FS) fs.FS {
	if ui == nil {
		return webassets.Assets
	}
	return underPrefix{prefix: bundleRoot, fsys: ui}
}

// underPrefix shows fsys at prefix and nothing anywhere else: a name outside it
// does not exist.
type underPrefix struct {
	prefix string
	fsys   fs.FS
}

func (p underPrefix) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == p.prefix {
		return p.fsys.Open(".")
	}
	rest, ok := strings.CutPrefix(name, p.prefix+"/")
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return p.fsys.Open(rest)
}
