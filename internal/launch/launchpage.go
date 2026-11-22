package launch

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// A browser window's one-time code, kept off the command line.
//
// The browser tiers of openWindow (Chromium's `--app=`, then the default browser
// through xdg-open, open or `cmd /c start`) are other programs started with an
// argument, and on Linux /proc/<pid>/cmdline is readable by every local account.
// On the loopback desktop a code is the whole sign-in, so a poller reaching
// /auth/launch first would have the session.
//
// So the code goes in a file, as Jupyter does: a 0600 page in the state
// directory that forwards to the launch address, and the browser is handed its
// file:// address, worthless to anybody who cannot read the operator's files.
// The same on every platform.
//
// A page is removed launchPageLife after it is written (its code has lapsed by
// then) by the process that wrote it, or else by the next window any KubeTower
// opens (sweepLaunchPages), since a second launch that falls to a browser exits
// at once.

const (
	// launchPagePrefix and launchPageSuffix name the pages, so the sweep finds
	// its own and nothing else.
	launchPagePrefix = "launch-"
	launchPageSuffix = ".html"

	// launchPageLife is how long a page is kept: twice a code's minute, so a
	// slow browser still finds it.
	launchPageLife = 2 * time.Minute
)

// browserTarget is what a browser tier is handed: the address of a page that
// forwards with a fresh browser code, or the bare console address when there is
// no code or the page could not be written, which opens on the sign-in page.
// The page goes in dir, or in the state directory when dir is "".
func browserTarget(listen string, mint func(native bool) (string, error), dir string) string {
	code := codeFor(mint, false)
	if code == "" {
		return consoleURL(listen)
	}
	if dir == "" {
		dir = statefile.Dir()
	}
	page, err := writeLaunchPage(dir, windowURL(listen, code), time.Now())
	if err != nil {
		log.Printf("[kubetower] the browser window opens on the sign-in page: the page that would carry "+
			"its one-time code could not be written (%v)", err)
		return consoleURL(listen)
	}
	time.AfterFunc(launchPageLife, func() { _ = os.Remove(page) })
	pendingPages.add(page)
	return fileURL(page)
}

// pendingPages are the pages this process wrote and has not removed. The
// running instance's timer removes them; a second launch removes its own
// before exiting (releaseLaunchPages).
var pendingPages pageSet

type pageSet struct {
	mu    sync.Mutex
	paths []string
}

func (p *pageSet) add(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, path)
}

func (p *pageSet) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	taken := p.paths
	p.paths = nil
	return taken
}

// launchPageWait is how long a second launch keeps its page for a browser that
// has not read it yet: a launch code's minute.
var launchPageWait = time.Minute // a launch code's life, internal/auth/window.go

// releaseLaunchPages removes every page this process wrote, each once the
// browser has read it or launchPageWait has passed, whichever is first.
//
// "Read" is the access time moving past the write time, which Linux (relatime)
// and macOS record by default. Where it is not kept (noatime, NTFS), the page is
// kept for the whole wait, so such a second launch may take a minute to exit
// rather than leave a code behind.
func releaseLaunchPages() {
	for _, page := range pendingPages.take() {
		deadline := time.Now().Add(launchPageWait)
		for time.Now().Before(deadline) && !pageRead(page) {
			time.Sleep(100 * time.Millisecond)
		}
		_ = os.Remove(page)
	}
}

// pageRead reports whether path has been read since it was written.
func pageRead(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true // gone already: nothing left to keep
	}
	read, ok := accessTime(info)
	return ok && read.After(info.ModTime())
}

// writeLaunchPage writes the forwarding page for target into dir, 0600, after
// sweeping the pages older than launchPageLife, and returns its path.
func writeLaunchPage(dir, target string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, statefile.DirPerm); err != nil {
		return "", err
	}
	sweepLaunchPages(dir, now)
	f, err := os.CreateTemp(dir, launchPagePrefix+"*"+launchPageSuffix)
	if err != nil {
		return "", err
	}
	path := f.Name()
	// Owner-only before the code is written: on Windows a file takes its
	// directory's ACL, which on another drive may be every account.
	if err := restrictToOwner(path); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("narrowing %s to its owner: %w", path, err)
	}
	_, werr := f.WriteString(launchPage(target))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("writing %s: %v", path, firstError(werr, cerr))
	}
	// CreateTemp makes it 0600 already; stated so it cannot drift.
	_ = os.Chmod(path, statefile.FilePerm)
	return path, nil
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// launchPage is the page itself: a refresh, a script for a browser that
// ignores the refresh, and a link for one that runs neither. `location.replace`
// keeps the page out of the window's history. No referrer is sent.
func launchPage(target string) string {
	attr := html.EscapeString(target)
	literal, _ := json.Marshal(target) // escapes <, > and &, so it cannot end the script
	return "<!doctype html>\n<html><head><meta charset=\"utf-8\">\n" +
		"<meta name=\"referrer\" content=\"no-referrer\">\n" +
		"<meta http-equiv=\"refresh\" content=\"0;url=" + attr + "\">\n" +
		"<title>KubeTower</title></head>\n" +
		"<body><script>location.replace(" + string(literal) + ")</script>\n" +
		"<p><a href=\"" + attr + "\">Open KubeTower</a></p></body></html>\n"
}

// sweepLaunchPages removes this file's pages older than launchPageLife from
// dir. Silent: it is housekeeping, and a page it cannot remove holds a code
// that has lapsed.
func sweepLaunchPages(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, launchPagePrefix) || !strings.HasSuffix(name, launchPageSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < launchPageLife {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// fileURL is path as a `file://` address: forward slashes, and a leading one
// before a Windows drive letter.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
