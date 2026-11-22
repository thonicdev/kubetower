package web

import (
	"encoding/json"
	"io/fs"
	"strings"

	"github.com/thonicdev/kubetower/internal/auth"
)

// manifestNamingWhatExists drops from the PWA manifest every icon the embedded
// tree does not hold. The bitmaps are rasterized by the Dockerfile's `icons`
// stage, so a build without Docker (`go build`, `cmd/ktbuild`) carries only the
// SVG; naming the missing files would cost three 404s and, in Chrome, a silently
// missing install offer. The container build has all four and drops nothing.
func manifestNamingWhatExists(files fs.FS, raw []byte) (out []byte, dropped []string) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not our file to repair: serve it as embedded.
		return raw, nil
	}
	icons, ok := doc["icons"]
	if !ok {
		return raw, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(icons, &entries); err != nil {
		return raw, nil
	}

	kept := make([]map[string]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		var src string
		if err := json.Unmarshal(entry["src"], &src); err != nil {
			// An unreadable src cannot be checked, so it is kept.
			kept = append(kept, entry)
			continue
		}
		// The manifest addresses icons as a browser does; the sub-FS is rooted at that
		// prefix.
		name := strings.TrimPrefix(src, auth.PWAPrefix)
		if name == src {
			// Another origin or a data: URI: not this tree's business.
			kept = append(kept, entry)
			continue
		}
		if _, err := fs.Stat(files, name); err != nil {
			dropped = append(dropped, src)
			continue
		}
		kept = append(kept, entry)
	}
	if len(dropped) == 0 {
		return raw, nil
	}
	if len(kept) == 0 {
		// An empty icon list is not installable at all, which is worse than naming a
		// 404. It cannot happen while icon.svg is committed.
		return raw, dropped
	}

	list, err := json.Marshal(kept)
	if err != nil {
		return raw, dropped
	}
	doc["icons"] = list
	rebuilt, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return raw, dropped
	}
	return rebuilt, dropped
}
