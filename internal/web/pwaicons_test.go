package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/auth"
)

// The PWA bitmaps are generated from the two SVGs and checked in, so they can
// drift from the drawing they came from — somebody edits `icon.svg`, does not
// re-run the rasterizer, and the application installs with the old mark while
// the browser tab shows the new one. Nothing else would notice.
//
// The guard is the one `vendor_test.go` uses for htmx and xterm.js: a table of
// hashes in a markdown file beside the assets, recomputed here from the embedded
// filesystem. What makes it work is not the hash — it is that the table lives in
// the same commit as the file, so updating one without the other fails a build
// rather than shipping.
//
// Hashing the **drawings** and not the bitmaps is deliberate and is the whole
// design: the drawing is what a person edits, so it is the thing whose change
// must force the regeneration. A test over the bitmaps alone would be satisfied
// by any pair of bitmaps, including last week's.
func TestTheIconBitmapsFollowTheDrawings(t *testing.T) {
	doc, err := fs.ReadFile(assets, "static/pwa/ICONS.md")
	if err != nil {
		t.Fatalf("ICONS.md is not embedded, so nothing records what the bitmaps were made from: %v", err)
	}
	table := string(doc)

	for _, name := range []string{"icon.svg", "icon-maskable.svg"} {
		body, err := fs.ReadFile(assets, "static/pwa/"+name)
		if err != nil {
			t.Errorf("%s is not embedded: %v", name, err)
			continue
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		if !strings.Contains(table, digest) {
			t.Errorf("%s has changed and ICONS.md still records the old drawing (%s is not in the "+
				"table). The bitmaps beside it were made from the previous version, so the "+
				"installed application would carry the old mark while the page carries the new "+
				"one. Re-run the rsvg command in ICONS.md and update its table.", name, digest)
		}
	}
}

// TestTheManifestsIconsAreAllPresent is the other half: a build that quietly
// stops carrying the bitmaps must fail.
//
// pwamanifest.go filters the manifest down to what a build carries, which keeps
// what is *served* honest — and would keep it honest all the way down to one
// icon without anybody noticing the other three had gone. This asserts the tree
// itself, so a build that quietly stops carrying the bitmaps fails here instead
// of shipping a smaller manifest.
func TestTheManifestsIconsAreAllPresent(t *testing.T) {
	raw, err := fs.ReadFile(assets, "static/pwa/manifest.webmanifest")
	if err != nil {
		t.Fatalf("the manifest is not embedded: %v", err)
	}
	for _, name := range []string{"icon.svg", "icon-192.png", "icon-512.png", "icon-maskable-512.png"} {
		if !strings.Contains(string(raw), "/kt-pwa/"+name) {
			t.Errorf("the manifest no longer names %s. If that was deliberate, this test is the "+
				"place to say so; if it was not, an installed application just lost an icon", name)
			continue
		}
		if _, err := fs.Stat(assets, "static/pwa/"+name); err != nil {
			t.Errorf("the manifest names %s and this build does not carry it. The served manifest "+
				"will quietly drop it — see pwamanifest.go — so nothing downstream will fail, "+
				"which is exactly how three 404s shipped in 0.1.0 and 0.2.0: %v", name, err)
		}
	}
}

// TestTheTabIconIsServedBeforeThereIsASession is the delivery half of the icon,
// and it is asserted through the same door a browser comes in by.
//
// The icon is fetched by the
// browser before any script runs and without the session cookie, so the whole
// path — the front door's exemption, the route's registration through Mount,
// the file server's type table — has to agree, and the halves tested apart
// (`auth_test.go` that the door lets the path through;
// `pwamanifest_test.go` that the handler serves the manifest) do not prove it.
// This asks for the icon itself, end to end, with no cookie, and checks what comes back.
//
// Both drawings, because the manifest names the maskable one and an installed
// application draws whichever the platform picked.
func TestTheTabIconIsServedBeforeThereIsASession(t *testing.T) {
	_, mux := railServer(t)

	hash, err := bcrypt.GenerateFromPassword([]byte("not-used-here"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("preparing the test hash: %v", err)
	}
	door, err := auth.New(auth.Config{
		User:         "operator",
		PasswordHash: string(hash),
		SessionKey:   []byte("a-thirty-two-byte-key-for-tests!"),
		SessionIdle:  time.Hour,
		SessionMax:   12 * time.Hour,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	guarded := door.Middleware(mux)

	for _, name := range []string{"icon.svg", "icon-maskable.svg"} {
		t.Run(name, func(t *testing.T) {
			// No cookie on the request, on purpose: that is how a tab asks.
			request := httptest.NewRequest(http.MethodGet, auth.PWAPrefix+name, nil)
			request.Header.Set("Accept", "image/avif,image/webp,image/svg+xml,image/*,*/*;q=0.8")
			recorder := httptest.NewRecorder()
			guarded.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("%s%s answered %d without a session, want 200. A browser handed anything "+
					"else here draws its default icon and says nothing", auth.PWAPrefix, name, recorder.Code)
			}
			if ct := recorder.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
				t.Errorf("%s%s answered Content-Type %q, want image/svg+xml. A tab ignores an icon "+
					"whose type is not an image", auth.PWAPrefix, name, ct)
			}
			if !strings.Contains(recorder.Body.String(), "<svg") {
				t.Errorf("%s%s answered 200 with something that is not the drawing: %q",
					auth.PWAPrefix, name, recorder.Body.String())
			}
		})
	}
}
