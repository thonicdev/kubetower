// Package releasesig verifies a release's checksums.txt against the project's
// Ed25519 signature before anything downloaded from that release is trusted.
//
// release.yml's sign job signs the final checksums.txt with the private key in
// the RELEASE_SIGNING_KEY secret, on a GitHub-hosted runner, and publishes the
// raw 64-byte signature as checksums.txt.sig (#46). The public half is
// release-key.pem, committed beside this file and built into the binary: a
// console only ever trusts a file this key signed, and the hash of an asset
// only from such a file.
//
// The format is OpenSSL's, so anyone can check a release by hand:
//
//	openssl pkeyutl -verify -rawin -pubin -inkey release-key.pem \
//	  -in checksums.txt -sigfile checksums.txt.sig
//
// The safe state needs nothing: no key built in, or a key that does not parse,
// verifies nothing, so nothing is installed.
package releasesig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

//go:embed release-key.pem
var embedded []byte

// MaxSums is the largest checksums.txt read: a release lists a dozen files.
const MaxSums = 64 << 10

var (
	// ErrNoKey: this build carries no usable public key.
	ErrNoKey = errors.New("this build carries no release signing key")
	// ErrBadSignature: the signature does not match checksums.txt under the key.
	ErrBadSignature = errors.New("checksums.txt is not signed by the release key")
	// ErrMalformed: checksums.txt is not a list of sha256 lines.
	ErrMalformed = errors.New("checksums.txt is malformed")
	// ErrNotListed: the asset has no line in checksums.txt.
	ErrNotListed = errors.New("the asset is not listed in checksums.txt")
)

// Key is the public key built into this binary.
func Key() (ed25519.PublicKey, error) {
	key, err := ParseKey(embedded)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoKey, err)
	}
	return key, nil
}

// ParseKey reads a PEM "PUBLIC KEY" block holding an Ed25519 key, as
// `openssl pkey -pubout` writes it.
func ParseKey(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("no PEM PUBLIC KEY block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("the key is %T, not Ed25519", parsed)
	}
	return key, nil
}

// Sums is a verified checksums.txt: each asset's lower-case hex sha256.
type Sums map[string]string

// SHA256 is the hash checksums.txt gives for the asset, by file name.
func (s Sums) SHA256(name string) (string, error) {
	sum, ok := s[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotListed, name)
	}
	return sum, nil
}

var line = regexp.MustCompile(`^([0-9a-f]{64}) [ *](?:\./)?([^/\\\x00-\x1f]+)$`)

// Verify checks sig over sums under key, and only then reads sums.
func Verify(key ed25519.PublicKey, sums, sig []byte) (Sums, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, ErrNoKey
	}
	if len(sums) == 0 || len(sums) > MaxSums {
		return nil, fmt.Errorf("%w: %d bytes", ErrMalformed, len(sums))
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, sums, sig) {
		return nil, ErrBadSignature
	}
	out := Sums{}
	for i, text := range strings.Split(strings.TrimRight(string(sums), "\n"), "\n") {
		m := line.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("%w: line %d", ErrMalformed, i+1)
		}
		if was, ok := out[m[2]]; ok && was != m[1] {
			return nil, fmt.Errorf("%w: %s is listed twice with two hashes", ErrMalformed, m[2])
		}
		out[m[2]] = m[1]
	}
	return out, nil
}

// VerifyEmbedded is Verify under the key built into this binary.
func VerifyEmbedded(sums, sig []byte) (Sums, error) {
	key, err := Key()
	if err != nil {
		return nil, err
	}
	return Verify(key, sums, sig)
}

// keyPresent says whether the embedded file holds anything at all.
func keyPresent() bool { return len(bytes.TrimSpace(embedded)) > 0 }
