package redis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/svcgroup"
	"github.com/thonicdev/kubetower/textutil"
)

// session is one request's connection: the tunnel and the client through it.
// It lives exactly as long as the request, and close takes both down.
type session struct {
	conn   *Conn
	tunnel extension.Tunnel
	// credential is set when a password was needed, for its provenance. Its
	// value is inside, unexported, and goes with the session.
	credential *Credential
}

func (s *session) close() {
	if s.conn != nil {
		s.conn.Close()
	}
	if s.tunnel.Stop != nil {
		s.tunnel.Stop()
	}
}

// refusal is an answer this package has already worded: what the page shows,
// the code it branches on, and whether it is a refusal (somebody's rights or a
// credential said no), a failure (a read that did not happen) or an absence.
//
// "You may not read the Secret", "the tunnel could not be opened" and "this
// instance is not there any more" are different answers for different people.
// A sentence this file wrote is a conclusion: wantsPassword never reads one as
// a fresh NOAUTH.
type refusal struct {
	status  int
	code    string
	kind    string
	message string
	detail  string
}

func (e *refusal) Error() string { return e.message }

// The three kinds a refusal is.
const (
	kindRefusal = "refusal"
	kindFailure = "failure"
	kindAbsent  = "absent"
)

// The codes, as the page spells them (web/app/extras/redis/data.ts).
const (
	CodeUnknownCluster   = "unknown_cluster"
	CodeListFailed       = "list_failed"
	CodeBadRequest       = "bad_request"
	CodeInstanceGone     = "instance_gone"
	CodeTunnelFailed     = "tunnel_failed"
	CodeUnreachable      = "unreachable"
	CodeNoCredential     = "no_credential"
	CodePodUnknown       = "pod_unknown"
	CodeSecretDenied     = "secret_denied"
	CodeSecretUnreviewed = "secret_unreviewed"
	CodeCredentialFailed = "credential_failed"
	CodePasswordRefused  = "password_refused"
	CodeReadFailed       = "read_failed"
	CodeKeyGone          = "key_gone"
	CodeSentinel         = "sentinel"
	CodeNoSuchDatabase   = "no_such_database"
	CodeNotRedis         = "not_redis"
	CodeRequestCancelled = "cancelled"
	// A read refused for the person: 403, and 503 when it could not be asked.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

// The statuses two families of refusal share: an unknown cluster is a 404, as
// every Host method answers it, and a password this console cannot use is a
// 409 - the instance and the person are both fine, the two disagree.
const (
	unknownClusterStatus  = http.StatusNotFound
	refusedPasswordStatus = http.StatusConflict
)

// connect opens one instance, finding its password only if it turns out to
// want one.
//
// One tunnel, and the password goes down it or nowhere. The instance is first
// dialled with no password. Only on NOAUTH is the reached pod read for its
// container's reference, the value revealed for the person, and the instance
// dialled again through the same tunnel, which a port-forward pins to that pod.
// A tunnel that cannot say which pod it reached is sent no password.
//
// No password ever comes from the request: a URL lands in history, Referers
// and logs.
func connect(r *http.Request, h extension.Host, names *keyNames, cluster string, instance svcgroup.Instance, database int) (*session, error) {
	ctx := r.Context()
	tunnel, err := h.Forward(ctx, cluster, instance.Namespace, instance.Service, instance.Port)
	if err != nil {
		return nil, tunnelRefusal(cluster, instance, err)
	}
	s := &session{tunnel: tunnel}

	conn, err := open(ctx, tunnel.Addr, options{database: database})
	if err == nil {
		s.conn = conn
		return s, nil
	}
	if !wantsPassword(err) {
		s.close()
		return nil, dialRefusal(ctx, instance, database, err, "")
	}

	if !tunnel.PodKnown || tunnel.Pod == "" {
		s.close()
		return nil, &refusal{status: refusedPasswordStatus, code: CodePodUnknown, kind: kindRefusal,
			message: "This instance asked for a password, and the tunnel could not say which pod it reached, " +
				"so no password was read and none was sent. A credential is only ever sent to the pod it was read for."}
	}

	credential, lookup := findPassword(r, h, names, cluster, instance.Namespace, tunnel.Pod)
	if lookup != nil {
		s.close()
		return nil, credentialRefusal(cluster, lookup)
	}
	if credential.Pod != tunnel.Pod {
		// findPassword reads the pod it is given, so this cannot happen;
		// checked so a refactor that broke it fails closed.
		s.close()
		return nil, &refusal{status: refusedPasswordStatus, code: CodePodUnknown, kind: kindRefusal,
			message: fmt.Sprintf("The password was read for pod %s and the tunnel reached pod %s, so it was not sent.",
				credential.Pod, tunnel.Pod)}
	}

	conn, err = open(ctx, tunnel.Addr, options{database: database, password: credential.password})
	if err != nil {
		s.close()
		if wantsPassword(err) {
			// Found a credential, and the server refused it: naming which one
			// tells a rotated Secret from a wrong variable.
			return nil, &refusal{status: refusedPasswordStatus, code: CodePasswordRefused, kind: kindRefusal,
				message: fmt.Sprintf("This instance refused the password in Secret %s, key %s - read from %s. "+
					"Either it has been rotated without the pod being restarted, or the server is using a "+
					"different credential from the one its pod is started with.",
					credential.Secret, credential.Key, credential.Source)}
		}
		return nil, dialRefusal(ctx, instance, database, err, credential.password)
	}
	s.conn = conn
	s.credential = &credential
	return s, nil
}

// wantsPassword is "the instance answered, and it wants credentials".
//
// A phrase match on Redis's own error codes, not on the word "password",
// which this package's own sentences contain.
func wantsPassword(err error) bool {
	if err == nil {
		return false
	}
	// A sentence this package already wrote is a conclusion, not evidence:
	// a refusal quoting NOAUTH must not restart the credential lookup.
	var already *refusal
	if errors.As(err, &already) {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "NOAUTH") ||
		strings.Contains(text, "WRONGPASS") ||
		strings.Contains(text, "ERR Client sent AUTH") ||
		strings.Contains(text, "invalid password") ||
		strings.Contains(text, "without any password configured")
}

func tunnelRefusal(cluster string, instance svcgroup.Instance, err error) error {
	if errors.Is(err, extension.ErrUnknownCluster) {
		return &refusal{status: unknownClusterStatus, code: CodeUnknownCluster, kind: kindAbsent,
			message: extension.UnknownClusterText(cluster)}
	}
	if said := personRefusal(err, fmt.Sprintf("The cluster says you may not open a tunnel to %s, "+
		"so nothing was asked of this instance.", label(instance))); said != nil {
		return said
	}
	return &refusal{status: http.StatusBadGateway, code: CodeTunnelFailed, kind: kindFailure,
		message: fmt.Sprintf("A tunnel to %s could not be opened, so nothing was asked of this instance.", label(instance)),
		detail:  shortError(err)}
}

// dialRefusal says why a connection through an open tunnel failed. password,
// when one was sent, is scrubbed from what is said: no error this package has
// seen carries it, and this is the line that keeps it so.
func dialRefusal(ctx context.Context, instance svcgroup.Instance, database int, err error, password string) error {
	var already *refusal
	if errors.As(err, &already) {
		return already
	}
	if ctx.Err() != nil {
		return &refusal{status: http.StatusServiceUnavailable, code: CodeRequestCancelled, kind: kindFailure,
			message: "The request ended before the instance answered."}
	}
	detail := scrubbed(err, password)
	if database != 0 && noSuchDatabase(err) {
		// The database is chosen by the dial's own SELECT, so a database the
		// server does not have fails the handshake - and is said as that, not
		// as a server that did not answer like a Redis.
		return &refusal{status: http.StatusNotFound, code: CodeNoSuchDatabase, kind: kindAbsent,
			message: fmt.Sprintf("%s has no database %d. A Redis has 16 unless it is configured with more, "+
				"and a Cluster node has database 0 only.", label(instance), database),
			detail: detail}
	}
	if nothingListening(err) {
		return &refusal{status: http.StatusBadGateway, code: CodeUnreachable, kind: kindFailure,
			message: "The tunnel opened and nothing answered on the other end. That port may belong to an " +
				"exporter rather than to the Redis itself, or nothing listens on it in the pod - it was " +
				"offered because " + instance.Why + ".",
			detail: detail}
	}
	return &refusal{status: http.StatusBadGateway, code: CodeUnreachable, kind: kindFailure,
		message: fmt.Sprintf("The tunnel to %s opened, and what is behind it did not answer as a Redis or a Valkey. "+
			"It was offered because %s.", label(instance), instance.Why),
		detail: detail}
}

// noSuchDatabase is the handshake's SELECT refused: Redis and Valkey answer
// "ERR DB index is out of range", a Cluster node "ERR SELECT is not allowed in
// cluster mode".
func noSuchDatabase(err error) bool {
	text := err.Error()
	return strings.Contains(text, "DB index") || strings.Contains(text, "SELECT is not allowed")
}

// nothingListening is a connection that was refused, or closed before it said
// anything - which is what a port-forward does when nothing listens on the
// pod's port: the local end accepts, and the far end hangs up.
func nothingListening(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "connection refused") || strings.Contains(text, "actively refused") ||
		strings.Contains(text, "connection reset") || strings.HasSuffix(text, "EOF")
}

// credentialRefusal says why no password could be had: nothing named, a read
// refused, a read failed, or a review the console could not make.
func credentialRefusal(cluster string, err error) error {
	detail := shortError(err)
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		return &refusal{status: unknownClusterStatus, code: CodeUnknownCluster, kind: kindAbsent,
			message: extension.UnknownClusterText(cluster)}
	case errors.Is(err, ErrNoCredential):
		// The instance is fine and this console cannot open it. Said as the fact
		// it is, and pointed at the one place that can.
		return &refusal{status: refusedPasswordStatus, code: CodeNoCredential, kind: kindRefusal,
			message: "This instance asked for a password, and nothing in the pod the tunnel reached names one " +
				"this console can read. Open it from the dock's terminal, where redis-cli takes the password you " +
				"already have.",
			detail: detail}
	case errors.Is(err, extension.ErrRevealDenied):
		return &refusal{status: http.StatusForbidden, code: CodeSecretDenied, kind: kindRefusal,
			message: "This instance asked for a password, and you may not read the Secret its pod names. That is an " +
				"RBAC grant you do not hold, not a Redis that is down.",
			detail: detail}
	case errors.Is(err, extension.ErrRevealUnreviewed):
		return &refusal{status: http.StatusServiceUnavailable, code: CodeSecretUnreviewed, kind: kindFailure,
			message: "This instance asked for a password, and the console could not review or record reading the " +
				"Secret its pod names, so it read nothing. Worth trying again.",
			detail: detail}
	}
	return &refusal{status: http.StatusBadGateway, code: CodeCredentialFailed, kind: kindFailure,
		message: "This instance asked for a password, and the pod or the Secret holding it could not be read.",
		detail:  detail}
}

func label(instance svcgroup.Instance) string {
	return instance.Namespace + "/" + instance.Service + ":" + strconv.Itoa(instance.Port)
}

func shortError(err error) string {
	if err == nil {
		return ""
	}
	return textutil.ReasonN(err.Error(), 240)
}

// scrubbed is the error's short form with the password withheld - judged on
// the whole message **before** it is shortened, since shortening cuts and
// collapses it and a cut password prefix would no longer match, and again
// after.
func scrubbed(err error, password string) string {
	if err == nil {
		return ""
	}
	if raw := scrub(err.Error(), password); raw != err.Error() {
		return raw
	}
	return scrub(shortError(err), password)
}

// scrub withholds a sentence that contains the password, whatever its length.
// Withheld whole rather than masked: a mask leaves the password's length and
// surroundings, and no error this package has seen echoes one anyway.
func scrub(text, password string) string {
	if password != "" && strings.Contains(text, password) {
		return "(the detail is withheld: it would have carried the password)"
	}
	return text
}
