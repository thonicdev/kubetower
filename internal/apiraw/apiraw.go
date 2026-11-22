// Package apiraw sends one request through a raw REST client and keeps the API
// server's own words when it fails.
//
// Not DoRaw, nor Do(ctx).Raw(): both return client-go's error without reading
// the Status body, so a JSON refusal reads "unknown" instead of saying who may
// not do what. Result.Error() decodes that Status and keeps the status code,
// which the callers classify on.
//
// A leaf: it imports client-go and nothing of this module, so the packages
// that read raw take on no dependency through it.
package apiraw

import (
	"context"
	"io"

	"k8s.io/client-go/rest"
)

// Do sends the request and returns its body undecoded.
//
// The body is returned with the error, as DoRaw returns it, so a caller can
// pass an error envelope (Prometheus's 400 naming an unknown label) through
// verbatim.
func Do(ctx context.Context, req *rest.Request) ([]byte, error) {
	result := req.Do(ctx)
	raw, err := result.Raw()
	if err != nil {
		return raw, result.Error()
	}
	return raw, nil
}

// DoCapped sends the request and returns its body, or tooLarge when the body
// runs past limit bytes: a truncated answer is not an answer, and the whole
// body would otherwise be held in memory whatever its size.
//
// Through Stream rather than Do, so the cap applies as the body is read; Do
// reads it whole first. A refusal still goes through Result.Error(), so the
// API server's sentence is kept. Unlike Do, no body comes back with an error.
func DoCapped(ctx context.Context, req *rest.Request, limit int64, tooLarge error) ([]byte, error) {
	body, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(Cap(body, limit, tooLarge))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Cap reads body up to limit bytes and fails with tooLarge on the byte after
// it, rather than ending early the way io.LimitReader does: a reader that
// ends early is a short answer that looks whole.
func Cap(body io.ReadCloser, limit int64, tooLarge error) io.ReadCloser {
	return &cappedBody{ReadCloser: body, left: limit, tooLarge: tooLarge}
}

type cappedBody struct {
	io.ReadCloser
	left     int64
	tooLarge error
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, b.tooLarge
	}
	// One byte more than is left, so that a body ending exactly at the cap
	// reads to its EOF and one going past it is caught on the byte that does.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.left {
		n, b.left = int(b.left), -1
		return n, b.tooLarge
	}
	b.left -= int64(n)
	return n, err
}
