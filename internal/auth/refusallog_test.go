package auth

// Any web page can provoke refusals
// on the launch and handoff routes, so their lines are coalesced rather than
// written one per request.

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func capturedLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

func linesWith(buf *bytes.Buffer, part string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

func TestAFloodOfRefusedHandoffsIsOneLine(t *testing.T) {
	a := handingOff(t)
	said := capturedLog(t)
	for i := 0; i < 100; i++ {
		step(t, a, http.MethodPost, "v2.hello.not-a-nonce", loopback, "")
	}
	if n := linesWith(said, "a handoff was refused"); n != 1 {
		t.Fatalf("100 refused handoffs wrote %d lines; want 1", n)
	}
	a.now = func() time.Time { return midday.Add(refusalLogEvery) }
	step(t, a, http.MethodPost, "v2.hello.not-a-nonce", loopback, "")
	if n := linesWith(said, "a handoff was refused"); n != 2 {
		t.Fatalf("a refusal a minute later wrote %d lines in all; want 2", n)
	}
	if !strings.Contains(said.String(), "and 99 more like it") {
		t.Errorf("the line after a quiet interval does not count what it stands for:\n%s", said.String())
	}
}

func TestAFloodOfRefusedLaunchesIsOneLine(t *testing.T) {
	a := testAuth(t)
	a.now = func() time.Time { return midday }
	a.OfferLaunches()
	said := capturedLog(t)
	for i := 0; i < 100; i++ {
		arrive(t, a, "a-guess", loopback)
	}
	if n := linesWith(said, "a window sign-in was refused"); n != 1 {
		t.Errorf("100 refused launches wrote %d lines; want 1", n)
	}
}

// A refused sign-in is said once per client per minute: a flood from one
// address is one line, another address still gets its own, and a minute later
// the next line counts what it stands for.
func TestRefusedLoginsAreSaidOncePerClientPerMinute(t *testing.T) {
	a := testAuth(t)
	clock := midday
	a.now = func() time.Time { return clock }
	said := capturedLog(t)

	from := func(addr string) {
		r := postForm(url.Values{"user": {"operator"}, "password": {"wrong"}})
		r.RemoteAddr = addr + ":40000"
		a.Login(httptest.NewRecorder(), r)
	}
	for i := 0; i < 50; i++ {
		from("198.51.100.7")
	}
	from("198.51.100.8")
	if n := linesWith(said, "failed login"); n != 2 {
		t.Fatalf("50 failed logins from one address and one from another wrote %d lines; want 2:\n%s", n, said)
	}
	if n := linesWith(said, "from 198.51.100.8"); n != 1 {
		t.Errorf("the second address is not named on its own line:\n%s", said)
	}
	// Past the budget, the throttled refusal is coalesced the same way.
	if n := linesWith(said, "too many attempts"); n != 1 {
		t.Errorf("the refusals past the budget wrote %d lines; want 1:\n%s", n, said)
	}

	clock = midday.Add(refusalLogEvery)
	from("198.51.100.7")
	if n := linesWith(said, "from 198.51.100.7"); n != 3 {
		t.Errorf("a minute later the address has %d lines in all; want 3 (failed, throttled, failed):\n%s", n, said)
	}
	if !strings.Contains(said.String(), "more like it") {
		t.Errorf("the line after a quiet interval does not count what it stands for:\n%s", said)
	}
}

// A single sign-on callback refused for want of this browser's flow is the
// cheapest request to send in a loop.
func TestRefusedCallbacksAreSaidOncePerClientPerMinute(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	a.now = func() time.Time { return midday }
	said := capturedLog(t)
	for i := 0; i < 100; i++ {
		a.FinishSSO(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
			consoleOrigin+CallbackPath+"?code=c&state=s", nil))
	}
	if n := linesWith(said, "single sign-on refused"); n != 1 {
		t.Errorf("100 refused callbacks wrote %d lines; want 1:\n%s", n, said)
	}
}

// The coalescing must not itself grow without bound under a flood from many
// addresses: past the cap, new clients share one key per kind.
func TestTheRefusalLogStaysBoundedUnderManyClients(t *testing.T) {
	var l refusalLog
	said := capturedLog(t)
	for i := 0; i < 10*refusalLogMaxKeys; i++ {
		l.sayFor(midday, "kind", fmt.Sprintf("client-%d", i), "[auth] refused (from %d)", i)
	}
	if len(l.kinds) > refusalLogMaxKeys+1 {
		t.Errorf("the log tracks %d keys; the bound is %d and one overflow", len(l.kinds), refusalLogMaxKeys)
	}
	if n := linesWith(said, "refused"); n != refusalLogMaxKeys+1 {
		t.Errorf("%d clients in one minute wrote %d lines; want %d", 10*refusalLogMaxKeys, n, refusalLogMaxKeys+1)
	}
	// A minute later the quiet keys are pruned rather than kept.
	l.sayFor(midday.Add(refusalLogEvery), "kind", "a-new-client", "[auth] refused")
	if len(l.kinds) != 1 {
		t.Errorf("a minute later %d keys are kept; want only the new one", len(l.kinds))
	}
}
