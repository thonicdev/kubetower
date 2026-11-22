package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// What a password guess costs, and who is guessing.
//
// Every refusal pays one bcrypt comparison and then holds its goroutine for
// failureDelay, so uncounted, concurrency would be the guesser's choice.
// Where the password routes face an ingress, a flood is unauthenticated CPU
// on the process serving everyone, against a hash that may be weak.
//
//   - Per client, a budget refusing with 429 once spent, only where the
//     address names one client (a direct connection, or the address a proxy
//     in KT_TRUSTED_PROXIES forwarded). Behind an unnamed ingress every
//     attempt has the ingress's address, and a budget on it would let one
//     guesser lock everybody out, so none is kept.
//   - For the process, a queue: globalBurst at once, one more every
//     globalEvery, each waiting up to globalQueue, then 429. It bounds bcrypt
//     CPU against guessers that rotate addresses or share an ingress.
//   - A lane of its own for a browser that signed in before (known.go).
//
// The residual: a flood does not lock out a known browser, but behind an
// unnamed ingress a first-time browser is admitted about 5/R of the time
// against R requests a second (measured 85 % at 5/s, 6 % at 100/s), waiting
// up to two seconds. Naming the ingress gives every client its own budget,
// and the flood then needs a fresh IPv6 /64 every ten attempts.

const (
	// attemptBurst attempts at once from one client, then one more every
	// attemptEvery: ten a minute sustained, which a person mistyping never
	// meets and a guesser meets within a second.
	attemptBurst = 10
	attemptEvery = 6 * time.Second
	// attemptClients is how many clients are remembered. Past it, refilled
	// budgets go first, then arbitrary ones: a bounded map is worth more than
	// remembering one more guesser.
	attemptClients = 10000

	// globalBurst attempts at once for the whole process, then one more every
	// globalEvery (five a second), each waiting at most globalQueue for its
	// turn.
	globalBurst = 10
	globalEvery = 200 * time.Millisecond
	globalQueue = 2 * time.Second
)

// attempts is a token bucket per client key. burst and every are zero for the
// per-address budget's own values (attemptBurst, attemptEvery).
type attempts struct {
	mu      sync.Mutex
	clients map[string]*bucket
	burst   float64
	every   time.Duration
}

func (l *attempts) rate() (float64, time.Duration) {
	if l.burst > 0 && l.every > 0 {
		return l.burst, l.every
	}
	return attemptBurst, attemptEvery
}

type bucket struct {
	tokens float64
	at     time.Time
}

// spend takes one attempt from client's budget at now. When none is left it
// answers false and how long until one is, before the caller runs bcrypt, so
// a refusal past the budget costs nothing.
func (l *attempts) spend(client string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients == nil {
		l.clients = map[string]*bucket{}
	}
	burst, every := l.rate()
	b := l.clients[client]
	if b == nil {
		if len(l.clients) >= attemptClients {
			l.evict(now)
		}
		b = &bucket{tokens: burst, at: now}
		l.clients[client] = b
	}
	b.refill(now, burst, every)
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) * float64(every))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// refund gives back the attempt spend took, when the attempt was then refused
// by the queue: a client is not charged for a 429 it did not cause.
func (l *attempts) refund(client string) {
	if client == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	burst, _ := l.rate()
	if b := l.clients[client]; b != nil && b.tokens+1 <= burst {
		b.tokens++
	}
}

// forgive gives one client its whole budget back: a correct password is not a
// guess. An empty key (an address naming nobody) forgives nothing, so a
// sign-in through a shared address never hands a guesser more tries.
func (l *attempts) forgive(client string) {
	if client == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, client)
}

func (b *bucket) refill(now time.Time, burst float64, every time.Duration) {
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens += float64(elapsed) / float64(every)
		if b.tokens > burst {
			b.tokens = burst
		}
		b.at = now
	}
}

// evict makes room. The caller holds mu.
func (l *attempts) evict(now time.Time) {
	burst, every := l.rate()
	for client, b := range l.clients {
		b.refill(now, burst, every)
		if b.tokens >= burst {
			delete(l.clients, client)
		}
	}
	for client := range l.clients {
		if len(l.clients) < attemptClients*9/10 {
			return
		}
		delete(l.clients, client)
	}
}

// queue is a share of the hashing capacity: a token bucket whose tokens may be
// reserved ahead, so concurrent attempts each learn their own wait. burst and
// every are zero for the anonymous queue's own values (globalBurst,
// globalEvery); every queue waits at most globalQueue.
type queue struct {
	mu    sync.Mutex
	b     *bucket
	burst float64
	every time.Duration
}

// reserve takes the next turn at now and answers how long to wait for it, or
// false when that is longer than globalQueue (and then takes nothing).
func (q *queue) reserve(now time.Time) (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	burst, every := q.burst, q.every
	if burst <= 0 || every <= 0 {
		burst, every = globalBurst, globalEvery
	}
	if q.b == nil {
		q.b = &bucket{tokens: burst, at: now}
	}
	q.b.refill(now, burst, every)
	q.b.tokens--
	if q.b.tokens >= 0 {
		return true, 0
	}
	wait := time.Duration(-q.b.tokens * float64(every))
	if wait > globalQueue {
		q.b.tokens++
		return false, wait
	}
	return true, wait
}

// admission is which budget an admitted attempt was counted against, so a
// correct password gives back that one and no other.
type admission struct {
	client string // the per-address key, "" where the address names nobody
	known  string // the known-browser cookie's id, "" for an anonymous attempt
}

// admit decides whether a password attempt may be checked now, before any
// bcrypt, and on a refusal how long until a retry can succeed. A valid
// known-browser cookie takes its own lane (known.go); anything else takes its
// address's budget, where the address names a client, and the anonymous
// queue.
func (a *Auth) admit(r *http.Request) (admission, bool, time.Duration) {
	now := a.now()
	if id, ok := a.knownBrowser(r); ok {
		in := admission{known: id}
		if ok, wait := a.known.spend(id, now); !ok {
			return in, false, wait
		}
		ok, wait := a.knownQueue.reserve(now)
		if !ok {
			a.known.refund(id)
			return in, false, wait
		}
		if wait > 0 {
			a.sleep(wait)
		}
		return in, true, 0
	}
	key, meaningful, _, _ := a.client(r)
	in := admission{}
	if meaningful {
		in.client = key
		if ok, wait := a.tries.spend(key, now); !ok {
			return in, false, wait
		}
	}
	ok, wait := a.queue.reserve(now)
	if !ok {
		a.tries.refund(in.client)
		return in, false, wait
	}
	if wait > 0 {
		a.sleep(wait)
	}
	return in, true, 0
}

// admitted gives back the budget a correct password's attempt was counted
// against, and no other, and marks this browser as known.
func (a *Auth) admitted(w http.ResponseWriter, r *http.Request, in admission) {
	a.tries.forgive(in.client)
	a.known.forgive(in.known)
	a.rememberBrowser(w, r)
}

// retryAfter is a Retry-After value: whole seconds, at least one.
func retryAfter(wait time.Duration) string {
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprint(seconds)
}

// client is who a request came from, as the budget counts it and the log
// names it.
//
//   - key: the address, an IPv6 one by its /64, which every IPv6 host holds
//     whole (fifty addresses in one /64 must not be fifty budgets).
//   - meaningful: whether key names one client (an address a named proxy
//     forwarded, or a connecting address that is not a proxy; behind an
//     ingress, known only when the deployment named its proxies).
//   - addr and via: for the log line, the address and the proxy it came
//     through when X-Forwarded-For was believed.
//
// The header is believed only from a named proxy. From anybody else it is
// the guesser's choice, and would let them write the log and reset their
// budget with every request.
func (a *Auth) client(r *http.Request) (key string, meaningful bool, addr, via string) {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	ip, err := netip.ParseAddr(peer)
	if err != nil {
		return peer, false, peer, ""
	}
	ip = ip.Unmap()
	if !a.trustedProxy(ip) {
		return clientKey(ip), !a.cfg.BehindProxy || len(a.cfg.TrustedProxies) > 0, ip.String(), ""
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseHop(hops[i])
		if !ok {
			// Not an address: this hop cannot be named, and those to its left
			// were written by somebody the proxies did not vouch for.
			break
		}
		if !a.trustedProxy(hop) {
			return clientKey(hop), true, hop.String(), ip.String()
		}
	}
	// A named proxy that forwarded nobody: its address is not a client.
	return clientKey(ip), false, ip.String(), ""
}

// parseHop reads one X-Forwarded-For entry: an address, or an address with a
// port, which some proxies write.
func parseHop(hop string) (netip.Addr, bool) {
	hop = strings.TrimSpace(hop)
	if ip, err := netip.ParseAddr(hop); err == nil {
		return ip.Unmap(), true
	}
	// `[2001:db8::1]`, a bracketed IPv6 address without a port, which
	// neither parser reads.
	if strings.HasPrefix(hop, "[") && strings.HasSuffix(hop, "]") {
		if ip, err := netip.ParseAddr(hop[1 : len(hop)-1]); err == nil {
			return ip.Unmap(), true
		}
	}
	if ap, err := netip.ParseAddrPort(hop); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// clientKey is the budget's key for an address: itself for IPv4, its /64 for
// IPv6.
func clientKey(ip netip.Addr) string {
	if ip.Is6() {
		prefix, _ := ip.Prefix(64)
		return prefix.String()
	}
	return ip.String()
}

func (a *Auth) trustedProxy(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, prefix := range a.cfg.TrustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// clientLine is client for the log: the address, and the proxy it came through
// when the header was believed.
func (a *Auth) clientLine(r *http.Request) string {
	_, _, addr, via := a.client(r)
	if via == "" {
		return addr
	}
	return addr + " (X-Forwarded-For, through trusted proxy " + via + ")"
}

// ParseTrustedProxies reads KT_TRUSTED_PROXIES: addresses or CIDR prefixes,
// separated by commas or spaces. Empty (the default) means the connecting
// address is the client.
func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		if prefix, err := netip.ParsePrefix(field); err == nil {
			out = append(out, prefix.Masked())
			continue
		}
		ip, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("%q is neither an address nor a CIDR prefix", field)
		}
		out = append(out, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
	}
	return out, nil
}
