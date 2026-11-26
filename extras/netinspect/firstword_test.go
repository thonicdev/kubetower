package netinspect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// tlsPair is a TLS 1.3 server on loopback that requires a client certificate
// signed by its CA, and a client configuration holding one: from that CA when
// trusted, else from another.
type tlsPair struct {
	addr   string
	client *tls.Config
}

// serveOnce accepts one connection, completes its handshake (or fails it),
// and hands it to talk. A failed handshake has already sent its alert.
func serveOnce(t *testing.T, trusted bool, talk func(net.Conn)) tlsPair {
	t.Helper()
	ca := newAuthority(t, "goldmane-ca")
	_, _, serverPair := ca.issue(t, "goldmane", goldmaneServerName)
	clients := x509.NewCertPool()
	clients.AddCert(ca.cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clients,
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = ln.Close()
		<-done // the client's connection is closed first: cleanups run last in, first out
	})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.(*tls.Conn).Handshake(); err != nil {
			return
		}
		talk(conn)
	}()
	issuer := ca
	if !trusted {
		issuer = newAuthority(t, "someone-else")
	}
	_, _, clientPair := issuer.issue(t, "x")
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	return tlsPair{addr: ln.Addr().String(), client: &tls.Config{
		RootCAs: roots, ServerName: goldmaneServerName, Certificates: []tls.Certificate{clientPair}, MinVersion: tls.VersionTLS13,
	}}
}

func dialHeard(t *testing.T, ctx context.Context, p tlsPair) (net.Conn, error) {
	t.Helper()
	raw, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	conn := tls.Client(raw, p.client)
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatalf("handshake: %v", err) // TLS 1.3: the client's side is over before the check
	}
	return firstWord(ctx, conn)
}

// A refused client certificate is read as the server's alert, every time: the
// client has written nothing, so the server's close cannot reset it away (#141).
func TestFirstWordReadsTheAlertOfARefusedCertificate(t *testing.T) {
	for i := range 20 {
		p := serveOnce(t, false, func(net.Conn) {})
		_, err := dialHeard(t, context.Background(), p)
		if err == nil || !isTLSFailure(err) {
			t.Fatalf("run %d: %v, want the server's TLS alert", i, err)
		}
	}
}

// A server that speaks first keeps every byte it said: the first word is read
// in front of the connection, not off it.
func TestFirstWordKeepsWhatTheServerSaid(t *testing.T) {
	p := serveOnce(t, true, func(conn net.Conn) { _, _ = conn.Write([]byte("SETTINGS")) })
	conn, err := dialHeard(t, context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("SETTINGS"))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "SETTINGS" {
		t.Fatalf("read %q, %v", got, err)
	}
}

// A server that waits for the client is still asked: the wait is bounded, and
// the connection reads and writes as usual after it.
func TestFirstWordWaitsBoundedForAServerThatListensFirst(t *testing.T) {
	defer func(was time.Duration) { firstWordWait = was }(firstWordWait)
	firstWordWait = 100 * time.Millisecond
	p := serveOnce(t, true, func(conn net.Conn) {
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err == nil && string(b) == "ping" {
			_, _ = conn.Write([]byte("pong"))
		}
	})
	start := time.Now()
	conn, err := dialHeard(t, context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %v for a server that listens first", waited)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "pong" {
		t.Fatalf("read %q, %v", got, err)
	}
}

// A cancelled call is not watched by a goroutine: it waits at most
// firstWordWait, then says it was cancelled. A call that ran out of time says
// so the same way.
func TestFirstWordSaysACancelledCall(t *testing.T) {
	defer func(was time.Duration) { firstWordWait = was }(firstWordWait)
	firstWordWait = 100 * time.Millisecond
	p := serveOnce(t, true, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	ctx, cancel := context.WithCancel(context.Background())
	raw, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	conn := tls.Client(raw, p.client)
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := firstWord(ctx, conn); !errors.Is(err, context.Canceled) {
		t.Errorf("%v, want the cancellation", err)
	}
}

func TestFirstWordSaysACallOutOfTime(t *testing.T) {
	defer func(was time.Duration) { firstWordWait = was }(firstWordWait)
	firstWordWait = 100 * time.Millisecond
	p := serveOnce(t, true, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	conn := tls.Client(raw, p.client)
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	start := time.Now()
	if _, err := firstWord(expired, conn); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v, want the deadline", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("waited %v, past firstWordWait", waited)
	}
}
