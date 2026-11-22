package web

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"strings"
	"sync"
)

// The embedded static tree needs its own cache headers. `http.FileServerFS` over
// an `embed.FS` sends no validators (embedded files have a zero modification
// time), and without a validator or `Cache-Control` a browser may invent a
// freshness lifetime (RFC 9111 heuristic expiration) and keep running a rebuilt
// console's old scripts. So assets are `no-cache` with an ETag: an unchanged
// asset costs a 304 and no body.

// staticETags caches one digest per path, from the bytes and per file, so
// changing one script re-downloads only that one. It grows to the number of
// embedded files at most.
var staticETags sync.Map // path -> string

// etagFor is the digest of one embedded file, or "" for a directory, a traversal
// attempt or a missing file. A missing ETag only costs a re-download; failing the
// request would leave a page unstyled.
func etagFor(fsys fs.FS, urlPath string) string {
	name := strings.TrimPrefix(urlPath, "/")
	if name == "" || strings.HasSuffix(name, "/") {
		return ""
	}
	// Defense in depth: `fs.ReadFile` on an embed.FS already refuses `.`, `..` and a
	// leading slash, but this is the package's only read of a path straight off a
	// URL, and another fs.FS behind it may be less strict.
	if !fs.ValidPath(name) {
		return ""
	}
	if tag, ok := staticETags.Load(name); ok {
		return tag.(string)
	}

	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	// Strong, not weak: a byte-for-byte digest of the representation served. The
	// first 16 bytes of it, base64.
	tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
	staticETags.Store(name, tag)
	return tag
}
