package launch

// A second launch that falls to
// a browser removes its page before it exits, rather than leaving it to a
// timer that dies with the process.

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// browserOpener stands in for openWindow when there is no native window: the
// browser rung, through browserTarget. read says whether the "browser" reads
// the page before the opener returns.
func browserOpener(t *testing.T, pages *[]string, read bool) windowOpener {
	return func(_ context.Context, listen string, mint func(native bool) (string, error), _ windowSources) bool {
		target := browserTarget(listen, mint, "")
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "file" {
			t.Fatalf("the browser rung was handed %q; want a page", target)
		}
		path := pagePath(t, target)
		*pages = append(*pages, path)
		if read {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			// What a browser reading the page does to it, as the file
			// system records it.
			if err := os.Chtimes(path, info.ModTime().Add(2*time.Second), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
		return false
	}
}

func TestASecondLaunchRemovesItsPageBeforeItReturns(t *testing.T) {
	for name, read := range map[string]bool{"read by the browser": true, "never read": false} {
		t.Run(name, func(t *testing.T) {
			listen := aRunningInstance(t)
			previous := launchPageWait
			launchPageWait = 300 * time.Millisecond
			t.Cleanup(func() { launchPageWait = previous })

			var pages []string
			start := time.Now()
			if !secondLaunch(true, listen, false, false, browserOpener(t, &pages, read)) {
				t.Fatal("the second launch did not hand off")
			}
			if len(pages) != 1 {
				t.Fatalf("%d pages were written; want one", len(pages))
			}
			if _, err := os.Stat(pages[0]); err == nil {
				t.Errorf("the page %s outlived the second launch", pages[0])
			}
			if read && time.Since(start) > launchPageWait {
				t.Errorf("a page the browser had read was kept for the whole wait (%s)", time.Since(start))
			}
		})
	}
}

func TestPageReadFollowsTheAccessTime(t *testing.T) {
	dir := t.TempDir()
	path, err := writeLaunchPage(dir, "http://127.0.0.1:5823/", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Pinned rather than taken from the file system, so the test cannot skip:
	// written and not read since is "not read", whatever this disk records on
	// a write. A pageRead that always says "read" would remove the page
	// before the browser opened it.
	written := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	if pageRead(path) {
		t.Fatal("a page whose access time is its write time is seen as read")
	}
	if err := os.Chtimes(path, written.Add(time.Second), written); err != nil {
		t.Fatal(err)
	}
	if !pageRead(path) {
		t.Error("a page whose access time moved past its write time is not seen as read")
	}
	if !strings.HasPrefix(path, dir) {
		t.Errorf("the page %s is not in %s", path, dir)
	}
}
