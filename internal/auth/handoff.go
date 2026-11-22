package auth

// A second launch hands off to the first.
//
// A second launch would fail on the port, and with no password on the
// loopback desktop it would have no way into the console. So the second
// process and the running instance prove to each other that they hold the
// launcher secret (a file in the state directory, 0600 in a 0700
// directory), and the instance hands it a fresh one-time code for the window
// it opens. `kubetower open` does the same.
//
// A token the page held would be readable by a DNS-rebound page too, and
// cannot ride `EventSource` or a WebSocket. So the session stays a cookie,
// and the secret never leaves the operator's own processes, not even to the
// instance.
//
// # Why the instance proves itself first
//
// A second launch talks to whatever answers on the port, which on a shared
// machine may be another user who bound 127.0.0.1:5823 first. A client that
// sent its proof first would let a squatter keep it, release the port, and
// replay it to the real instance within thirty seconds for a code and a
// session; a squatter answering with a "code" of its own would get a native
// window on its page, at the console's origin, which would send it the
// window's session cookie.
//
// So the exchange is mutual and bound to a nonce only the running instance
// issues:
//
//  1. **hello**: the client sends a fresh client nonce, and nothing else.
//  2. The instance answers with a fresh **server nonce** - single use, thirty
//     seconds, at most maxChallenges open, the oldest evicted beyond that - and
//     `HMAC(secret, "instance" | address | client nonce | server nonce)`.
//  3. The client checks that MAC in constant time and **stops if it is wrong**:
//     whatever answered does not hold the secret, and it has been sent nothing
//     it can use. Only then does the client send **prove**:
//     `HMAC(secret, "client" | address | both nonces | native)`.
//  4. The instance spends the server nonce, checks the MAC, mints a code and
//     answers it **sealed**: AES-256-GCM under a key derived from the secret,
//     the address and both nonces. The client opens it, which also
//     authenticates it, and opens a window only on a code that opened.
//
// A captured hello is a random number. A captured prove names a server nonce
// that was spent the moment it was answered, by the only instance that issued
// it, so it is refused everywhere afterwards. A relay to an instance on
// another address fails at step 3, because every MAC names the address the
// instance itself answers on. The client never follows a redirect.
//
// # What protects the route
//
//   - The Host allowlist (internal/launch, hostguard.go), which runs first:
//     a rebound page cannot reach this route.
//   - Loopback only, by the connecting address.
//   - POST only, with the steps in a header a form cannot set and a
//     cross-origin script cannot send without a preflight this server never
//     answers.
//   - The mutual exchange above.
//   - Registered only on the loopback desktop with a launcher secret
//     (AcceptHandoffs).
//
// # What it does not protect against
//
// Anything that can read the state directory can read the secret, as it can
// `credentials.json`'s session key: that is the operator's own account. And
// the window then loads `http://<address>/auth/launch?code=…` over plain
// loopback HTTP; the exchange ensures the running instance held the address a
// moment before, and a squatter cannot take a port that instance holds.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
)

const (
	// HandoffPath is where a second launch asks the running instance for a code.
	HandoffPath = api.HandoffPath

	// LauncherHeader carries the two steps of the exchange: `v2.hello.<client
	// nonce>`, then `v2.prove.<client nonce>.<server nonce>.<mac>`.
	LauncherHeader = "X-KubeTower-Launcher"

	// handoffVersion prefixes every header, so another shape (`v1`, a one-way
	// proof) is refused rather than guessed at.
	handoffVersion = "v2"

	// handoffNonceBytes is the size of each nonce: 128 bits, hex on the wire.
	handoffNonceBytes = 16

	// challengeLife is how long a server nonce waits for its prove: both ends
	// are on one machine, so ample.
	challengeLife = 30 * time.Second

	// maxChallenges bounds the server nonces waiting at once, since a hello is
	// unauthenticated. At the bound the oldest is evicted rather than the
	// newcomer refused: refusing would let any local process starve the
	// handoff with 64 hellos every thirty seconds, while evicting leaves a race
	// a client wins by proving within milliseconds, and retries when it loses.
	maxChallenges = 64

	// handoffAttempts is how many times the client runs the exchange when the
	// instance proved itself and then turned the prove away (its challenge was
	// evicted or lapsed).
	handoffAttempts = 3

	// minLauncherSecret is the shortest launcher secret accepted, in hex
	// characters (16 bytes); this build writes 32 random bytes.
	minLauncherSecret = 32

	// handoffLabel starts every MAC and every key derivation, so nothing this
	// exchange computes can be confused with another use of the secret.
	handoffLabel = "kubetower-handoff-v2"
)

// ErrNotTheInstance is what the client half returns when whatever answered on
// the address did not prove it holds the launcher secret: another program,
// another account's KubeTower, or something pretending. Nothing reusable was
// sent to it, and no window may be opened on it.
var ErrNotTheInstance = errors.New("it did not prove it holds this account's launcher secret")

// ErrInstanceBusy is what the client half returns when the instance answered
// 503, or proved itself and then turned the prove away more than
// handoffAttempts times: something local is flooding it with hellos. Not an
// impostor; try again.
var ErrInstanceBusy = errors.New("the instance is busy with other handoffs; try again")

// AcceptHandoffs installs the launcher secret (the hex text from the state
// directory) and the address this instance answers on, which every MAC
// names. Called once, before serving, on the loopback desktop only.
func (a *Auth) AcceptHandoffs(secret, addr string) {
	a.launcher = []byte(secret)
	a.launcherAddr = addr
}

// AcceptsHandoffs is whether a launcher secret was installed, which is what
// decides whether the route is registered.
func (a *Auth) AcceptsHandoffs() bool {
	return len(a.launcher) >= minLauncherSecret && a.OffersLaunch()
}

// handoffMAC is HMAC-SHA256 keyed with the secret over the label, the step's
// role and its fields, each after a zero byte, in hex.
func handoffMAC(secret, role string, fields ...string) string {
	return hex.EncodeToString(handoffMACBytes(secret, role, fields...))
}

func handoffMACBytes(secret, role string, fields ...string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(handoffLabel + "\x00" + role))
	for _, f := range fields {
		mac.Write([]byte{0})
		mac.Write([]byte(f))
	}
	return mac.Sum(nil)
}

// sealCode and openCode are step 4: AES-256-GCM under a key derived from the
// secret, the address and both nonces. The server nonce is single use and
// issued by the instance, so no key is ever used twice and the zero GCM nonce
// is sound. Opening authenticates: a code the instance did not seal does not
// open.
func sealCode(secret, addr, client, server, native, code string) (string, error) {
	aead, err := handoffAEAD(secret, addr, client, server, native)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, make([]byte, aead.NonceSize()), []byte(code), []byte(handoffLabel))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openCode(secret, addr, client, server, native, sealed string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", false
	}
	aead, err := handoffAEAD(secret, addr, client, server, native)
	if err != nil {
		return "", false
	}
	code, err := aead.Open(nil, make([]byte, aead.NonceSize()), raw, []byte(handoffLabel))
	if err != nil || len(code) == 0 {
		return "", false
	}
	return string(code), true
}

func handoffAEAD(secret, addr, client, server, native string) (cipher.AEAD, error) {
	block, err := aes.NewCipher(handoffMACBytes(secret, "seal", addr, client, server, native))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nativeField(native bool) string {
	if native {
		return "1"
	}
	return "0"
}

func newHandoffNonce() (string, error) {
	raw := make([]byte, handoffNonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// isHandoffNonce is whether s is a nonce this exchange draws: exactly
// handoffNonceBytes, in lower-case hex.
func isHandoffNonce(s string) bool {
	if len(s) != 2*handoffNonceBytes || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// handoffChallenges are the server nonces issued by hello and not yet spent by
// a prove, each with the client nonce it was issued to.
type handoffChallenges struct {
	mu   sync.Mutex
	open map[string]handoffChallenge
	seq  uint64
}

type handoffChallenge struct {
	client string
	lapses time.Time
	seq    uint64
}

// issue draws a server nonce for client. When maxChallenges are already
// waiting, the oldest is evicted to make room, and evicted says so.
func (c *handoffChallenges) issue(client string, now time.Time) (server string, evicted bool, err error) {
	server, err = newHandoffNonce()
	if err != nil {
		return "", false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	if len(c.open) >= maxChallenges {
		oldest, first := "", true
		var min uint64
		for nonce, open := range c.open {
			if first || open.seq < min {
				oldest, min, first = nonce, open.seq, false
			}
		}
		delete(c.open, oldest)
		evicted = true
	}
	if c.open == nil {
		c.open = map[string]handoffChallenge{}
	}
	c.seq++
	c.open[server] = handoffChallenge{client: client, lapses: now.Add(challengeLife), seq: c.seq}
	return server, evicted, nil
}

// spend removes server and reports whether it was open, unlapsed and issued to
// client. Removed whatever the answer, so a server nonce is never tried
// twice.
func (c *handoffChallenges) spend(client, server string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	open, ok := c.open[server]
	delete(c.open, server)
	return ok && hmac.Equal([]byte(open.client), []byte(client))
}

func (c *handoffChallenges) prune(now time.Time) {
	for server, open := range c.open {
		if !now.Before(open.lapses) {
			delete(c.open, server)
		}
	}
}

// handoffAnswer is a 200's body: a challenge and the instance's MAC after a
// hello, a sealed code after a prove.
type handoffAnswer struct {
	Challenge string `json:"challenge,omitempty"`
	Proof     string `json:"proof,omitempty"`
	Sealed    string `json:"sealed,omitempty"`
}

// Handoff is `POST /auth/handoff`, both steps of the exchange above. Anything
// that is not a well-formed step from loopback is a 403 with no body worth
// reading.
func (a *Auth) Handoff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !a.AcceptsHandoffs() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	refuse := func() {
		a.refusals.say(a.now(), "handoff", "[auth] a handoff was refused from %s: not this machine, or not "+
			"a step of the exchange with this instance's launcher secret", a.clientLine(r))
		http.Error(w, "refused", http.StatusForbidden)
	}
	if !fromLoopback(r) {
		refuse()
		return
	}
	parts := strings.Split(r.Header.Get(LauncherHeader), ".")
	if len(parts) < 2 || parts[0] != handoffVersion {
		refuse()
		return
	}
	now := a.now()
	secret := string(a.launcher)
	switch {
	case parts[1] == "hello" && len(parts) == 3 && isHandoffNonce(parts[2]):
		client := parts[2]
		server, evicted, err := a.challenges.issue(client, now)
		if err != nil {
			log.Printf("[auth] a handoff could not be started: no nonce could be drawn (%v)", err)
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		if evicted {
			a.refusals.say(now, "handoff-evicted", "[auth] %d handoffs were already waiting, so the oldest "+
				"was dropped to make room. Only many hellos at once do that, from something on this machine",
				maxChallenges)
		}
		writeHandoff(w, handoffAnswer{
			Challenge: server,
			Proof:     handoffMAC(secret, "instance", a.launcherAddr, client, server),
		})
	case parts[1] == "prove" && len(parts) == 5 && isHandoffNonce(parts[2]) && isHandoffNonce(parts[3]):
		client, server, mac := parts[2], parts[3], parts[4]
		native := r.URL.Query().Get("native") == "1"
		// Spent before the MAC is looked at, so a wrong guess costs the nonce.
		if !a.challenges.spend(client, server, now) {
			refuse()
			return
		}
		want := handoffMAC(secret, "client", a.launcherAddr, client, server, nativeField(native))
		if !hmac.Equal([]byte(mac), []byte(want)) {
			refuse()
			return
		}
		code, err := a.IssueLaunchCode(native)
		if err != nil {
			log.Printf("[auth] a handoff could not mint a code: %v", err)
			http.Error(w, "no code could be minted", http.StatusServiceUnavailable)
			return
		}
		sealed, err := sealCode(secret, a.launcherAddr, client, server, nativeField(native), code)
		if err != nil {
			log.Printf("[auth] a handoff could not seal its code: %v", err)
			http.Error(w, "no code could be sealed", http.StatusServiceUnavailable)
			return
		}
		log.Printf("[auth] a second launch was handed a one-time code for a %s window", windowKind(native))
		writeHandoff(w, handoffAnswer{Sealed: sealed})
	default:
		refuse()
	}
}

func writeHandoff(w http.ResponseWriter, answer handoffAnswer) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(answer)
}

func windowKind(native bool) string {
	if native {
		return "native"
	}
	return "browser"
}

// handoffAnswerLimit bounds what the client reads of an answer.
const handoffAnswerLimit = 4096

// RequestHandoff is the client half, run by a second launch against addr, the
// address the instance is expected to answer on: hello, check the instance's
// MAC, prove, open the sealed code. It returns the code, or an error wrapping
// ErrNotTheInstance when whatever answered did not prove it holds secret, in
// which case nothing reusable was sent to it. Its client never follows a
// redirect, which would carry the exchange somewhere else.
func RequestHandoff(ctx context.Context, secret, addr string, native bool, timeout time.Duration) (string, error) {
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var err error
	for attempt := 0; attempt < handoffAttempts; attempt++ {
		var code string
		code, err = exchange(ctx, client, secret, addr, native)
		if !errors.Is(err, ErrInstanceBusy) {
			return code, err
		}
	}
	return "", err
}

// exchange is one run of the two steps.
func exchange(ctx context.Context, client *http.Client, secret, addr string, native bool) (string, error) {
	target := "http://" + addr + HandoffPath

	clientNonce, err := newHandoffNonce()
	if err != nil {
		return "", err
	}
	hello, err := postHandoff(ctx, client, target, handoffVersion+".hello."+clientNonce)
	if err != nil {
		return "", err
	}
	if !isHandoffNonce(hello.Challenge) {
		return "", fmt.Errorf("%w: its answer carried no challenge", ErrNotTheInstance)
	}
	want := handoffMAC(secret, "instance", addr, clientNonce, hello.Challenge)
	if !hmac.Equal([]byte(hello.Proof), []byte(want)) {
		return "", fmt.Errorf("%w: its proof did not match", ErrNotTheInstance)
	}

	n := nativeField(native)
	proof := handoffMAC(secret, "client", addr, clientNonce, hello.Challenge, n)
	query := ""
	if native {
		query = "?native=1"
	}
	got, err := postHandoff(ctx, client, target+query,
		handoffVersion+".prove."+clientNonce+"."+hello.Challenge+"."+proof)
	if errors.Is(err, ErrNotTheInstance) {
		// The instance proved itself a moment ago, so this is not an
		// impostor: its challenge was evicted or lapsed before the prove.
		return "", fmt.Errorf("%w: its challenge was turned away after it had proved itself", ErrInstanceBusy)
	}
	if err != nil {
		return "", err
	}
	code, ok := openCode(secret, addr, clientNonce, hello.Challenge, n, got.Sealed)
	if !ok {
		return "", fmt.Errorf("%w: its code was not sealed with the secret", ErrNotTheInstance)
	}
	return code, nil
}

// postHandoff sends one step and reads a 200's JSON body. Any other answer,
// a redirect included, is an error that says the status and nothing of the
// body.
func postHandoff(ctx context.Context, client *http.Client, target, header string) (handoffAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return handoffAnswer{}, err
	}
	req.Header.Set(LauncherHeader, header)
	resp, err := client.Do(req)
	if err != nil {
		return handoffAnswer{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, handoffAnswerLimit))
	if resp.StatusCode == http.StatusServiceUnavailable {
		return handoffAnswer{}, ErrInstanceBusy
	}
	if resp.StatusCode != http.StatusOK {
		return handoffAnswer{}, fmt.Errorf("%w: it answered %d", ErrNotTheInstance, resp.StatusCode)
	}
	var answer handoffAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return handoffAnswer{}, fmt.Errorf("%w: its answer was not the exchange's", ErrNotTheInstance)
	}
	return answer, nil
}
