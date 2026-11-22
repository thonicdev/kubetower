package detail

// What the certificate in a Secret says: when it expires and whether it is the
// one expected, without `base64 -d | openssl x509`.
//
// A certificate is public by construction, so its fields are safe to render. A
// private key is not, and that is guaranteed structurally: nothing here walks
// the data block. Only the keys in `certificateKeys` are read; private-key keys
// are looked up for presence and their value discarded into `_` at the call.
// Ranging over the data and PEM-decoding each value would put a private key
// through `pem.Decode`. `TestThePrivateKeyIsNeverReadOrRendered` holds this.
// The panel says which private keys exist and were not read.
//
// The panel keys on the data, not only the type: cert-manager's extra output
// formats, ingress-nginx's default certificate and hand-made Secrets often put
// a `tls.crt` in an `Opaque` Secret. The type is read for one thing: a
// `kubernetes.io/tls` Secret without `tls.crt` gets a sentence, because it is
// broken. ConfigMaps are covered too (`kube-root-ca.crt` holds the cluster CA
// in `ca.crt`); their `data` is plain text, not base64 (`decodeValue`).

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/textutil"
)

// TLSSecretType is the type Kubernetes gives a Secret holding a serving
// certificate; see the file comment for its one use.
const TLSSecretType = "kubernetes.io/tls"

// certificateKeys are the keys this file will decode, in the order the panel
// draws them.
//
// A list of names, never a walk of the data block: a key not on it is not read,
// so a private key under any name cannot reach a PEM parser, a view or a log
// through this panel. `ca.crt` is where cert-manager writes the issuing CA and
// where `kube-root-ca.crt` keeps the cluster's.
var certificateKeys = []string{"tls.crt", "ca.crt"}

// privateKeyKeys are named so they can be avoided: each is looked up for
// presence alone. `tls.key` sits beside a serving certificate, `ca.key` beside
// an in-cluster CA's own (cilium-ca, for instance), the more dangerous one.
var privateKeyKeys = []string{"tls.key", "ca.key"}

// maxSANs caps the list drawn on the panel: a certificate can carry 100 names or
// more, and a chip wall cannot be matched against a hostname. Capped with a
// count rather than cut silently.
const maxSANs = 50

// CertificateSet is the TLS material one object carries, ready to render. nil
// for an object that carries none.
type CertificateSet struct {
	// Bundles is one entry per certificate-bearing key that was present.
	Bundles []CertificateBundle
	// PrivateKeys names the private-key entries this object carries, never their
	// values, which are not read, so the panel can say what was left alone. Empty
	// on a ConfigMap and on a Secret holding none.
	PrivateKeys []string
	// Note is why there is no bundle at all: a kubernetes.io/tls Secret with no
	// tls.crt. Empty when Bundles is filled.
	Note string
}

// CertificateBundle is one key's worth of PEM: a leaf and whatever intermediates
// were appended to it.
type CertificateBundle struct {
	// Key is the data key this came out of: "tls.crt", "ca.crt".
	Key string
	// Certificates are the blocks that parsed, in stored order: leaf first for a
	// serving bundle, as TLS sends them.
	Certificates []Certificate
	// Note is why this bundle is empty, or what stopped the walk part-way. Each
	// failure has its own sentence, and a partial chain is never drawn as a whole
	// one: the note sits beside the certificates that did parse.
	Note string
}

// Certificate is one x509 certificate as the panel needs it. Every field here is
// something the certificate hands to any client that connects.
type Certificate struct {
	// Position is 1-based within the bundle, so the panel can say which is
	// presented first.
	Position int
	// Role is read off the certificate, not its position: a leaf is not a CA, a
	// root is a CA that signed itself, an intermediate is a CA that did not.
	Role string

	Subject string
	Issuer  string
	// SelfSigned reports that the issuer matches the subject and the signature
	// verifies against its own key. Names alone match on a self-issued
	// certificate signed by someone else; `CheckSignatureFrom` refuses a parent
	// that is not a CA, which a self-signed serving certificate is not.
	SelfSigned bool

	// SANs are the names this certificate is valid for: DNS names, IP addresses,
	// URIs and email addresses, in that order. Browsers ignore the common name.
	SANs []string
	// SANsNote says how many were left out. Empty when they all fit.
	SANsNote string

	NotBefore time.Time
	NotAfter  time.Time
	// Expiry is how long is left, in the console's short age form: "expires in
	// 61d4h", "expired 3d2h ago".
	Expiry string
	// ExpiryClass grades it: resources.ClassOK, ClassWarn or ClassBad (gradeExpiry).
	ExpiryClass string

	Serial             string
	SignatureAlgorithm string
	// PublicKey is the key type and its size: "RSA 2048", "ECDSA P-256",
	// "Ed25519".
	PublicKey string
	IsCA      bool
}

// certificatesOf builds the panel from an object already in hand. nil for any
// object that carries no certificate, which is almost all of them.
func certificatesOf(raw *unstructured.Unstructured, kind string, now time.Time) *CertificateSet {
	secret := strings.EqualFold(kind, "Secret")
	configMap := strings.EqualFold(kind, "ConfigMap")
	if !secret && !configMap {
		return nil
	}

	set := &CertificateSet{}
	if secret {
		// The only lookups of a private key; the value goes to `_`.
		for _, key := range privateKeyKeys {
			if _, present, _ := unstructured.NestedString(raw.Object, FieldData, key); present {
				set.PrivateKeys = append(set.PrivateKeys, key)
			}
		}
	}

	for _, key := range certificateKeys {
		stored, found, err := unstructured.NestedString(raw.Object, FieldData, key)
		if err != nil || !found {
			continue
		}
		// A Secret's values are base64, a ConfigMap's `data` plain text; mixing
		// them up yields no PEM block.
		set.Bundles = append(set.Bundles, bundleOf(key, stored, secret, now))
	}

	if len(set.Bundles) > 0 {
		return set
	}

	// A Secret whose type promises a certificate and carries none is broken;
	// no panel would make it look like any other Secret.
	if secretType, _, _ := unstructured.NestedString(raw.Object, "type"); secret && secretType == TLSSecretType {
		set.Note = "This Secret's type is " + TLSSecretType + ", which promises a certificate under " +
			certificateKeys[0] + " — and it has no such key. Nothing here can be read."
		return set
	}
	return nil
}

// bundleOf decodes one key and walks its PEM blocks.
// It stops at the first block that is not a certificate and says so beside the
// ones that parsed: skipping it would draw a partial chain as a whole one.
func bundleOf(key, stored string, encoded bool, now time.Time) CertificateBundle {
	out := CertificateBundle{Key: key}

	decoded, ok := decodeValue(stored, encoded)
	if !ok {
		out.Note = key + " is not valid base64. The API stores a Secret's values encoded, so nothing " +
			"could be decoded from this one — the value is not what the API says it is."
		return out
	}
	if len(bytes.TrimSpace(decoded)) == 0 {
		out.Note = key + " is present and empty."
		return out
	}

	rest := decoded
	position := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		position++

		// The block type is named only when known: a PEM type line is arbitrary
		// text from somebody's file.
		if block.Type != "CERTIFICATE" {
			out.Note = notCertificate(key, position, block.Type)
			return out
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			out.Note = fmt.Sprintf("PEM block %d of %s says CERTIFICATE and does not parse as one: %s.",
				position, key, firstLine(err.Error()))
			if position > 1 {
				out.Note += fmt.Sprintf(" The chain is shown only as far as #%d.", position-1)
			}
			return out
		}
		out.Certificates = append(out.Certificates, certificateOf(cert, position, now))
	}

	if position == 0 {
		out.Note = key + " decodes to " + textutil.Bytes(int64(len(decoded))) +
			" that hold no PEM block at all. A certificate here would begin with -----BEGIN CERTIFICATE-----."
	}
	return out
}

// pemLabels are the block types this file will name on screen; anything else is
// described rather than quoted.
var pemLabels = map[string]string{
	"PRIVATE KEY":             "a private key",
	"RSA PRIVATE KEY":         "an RSA private key",
	"EC PRIVATE KEY":          "an EC private key",
	"OPENSSH PRIVATE KEY":     "an OpenSSH private key",
	"ENCRYPTED PRIVATE KEY":   "an encrypted private key",
	"PUBLIC KEY":              "a public key",
	"CERTIFICATE REQUEST":     "a certificate signing request",
	"NEW CERTIFICATE REQUEST": "a certificate signing request",
	"X509 CRL":                "a certificate revocation list",
	"DH PARAMETERS":           "a set of Diffie-Hellman parameters",
	"PKCS7":                   "a PKCS#7 container",
}

func notCertificate(key string, position int, blockType string) string {
	label, known := pemLabels[blockType]
	if !known {
		label = "a PEM block this console does not recognize"
	}
	if position == 1 {
		return key + " holds " + label + " and not a certificate."
	}
	return fmt.Sprintf("PEM block %d of %s is %s and not a certificate, so the chain is shown only as far as #%d.",
		position, key, label, position-1)
}

// certificateOf reads one parsed certificate into the view model.
func certificateOf(cert *x509.Certificate, position int, now time.Time) Certificate {
	selfSigned := bytes.Equal(cert.RawIssuer, cert.RawSubject) &&
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil

	out := Certificate{
		Position:           position,
		Role:               roleOf(cert, selfSigned),
		Subject:            nameOf(cert.Subject.String()),
		Issuer:             nameOf(cert.Issuer.String()),
		SelfSigned:         selfSigned,
		NotBefore:          cert.NotBefore,
		NotAfter:           cert.NotAfter,
		Expiry:             expiryText(cert.NotBefore, cert.NotAfter, now),
		ExpiryClass:        gradeExpiry(cert.NotBefore, cert.NotAfter, now),
		Serial:             serialOf(cert),
		SignatureAlgorithm: cert.SignatureAlgorithm.String(),
		PublicKey:          publicKeyOf(cert),
		IsCA:               cert.IsCA,
	}
	out.SANs, out.SANsNote = sansOf(cert)
	return out
}

// sansOf collects the names this certificate is valid for.
//
// The empty case depends on the role: a CA carries no SAN by design, while a
// leaf with none is why a browser refuses the site.
func sansOf(cert *x509.Certificate) ([]string, string) {
	names := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses)+len(cert.URIs)+len(cert.EmailAddresses))
	names = append(names, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	for _, u := range cert.URIs {
		names = append(names, (*url.URL)(u).String())
	}
	names = append(names, cert.EmailAddresses...)

	if len(names) == 0 {
		if cert.IsCA {
			return nil, "None, which is what a certificate authority is expected to carry: it signs for names rather than serving them."
		}
		// Said rather than left blank: browsers do not fall back to the common
		// name, so this certificate is valid for no host.
		return nil, "No subject alternative names. Browsers stopped falling back to the common name years ago, so this certificate is valid for no host as far as one is concerned."
	}
	if len(names) > maxSANs {
		left := len(names) - maxSANs
		return names[:maxSANs], fmt.Sprintf("… and %d more, of %d in all.", left, len(names))
	}
	return names, ""
}

// roleOf says what a certificate is, from the certificate and not from where it
// sits in the file, so a bundle stored in another order is not relabeled.
func roleOf(cert *x509.Certificate, selfSigned bool) string {
	if !cert.IsCA {
		return "leaf"
	}
	if selfSigned {
		return "root"
	}
	return "intermediate"
}

// nameOf clips a distinguished name, so one long DN cannot fill the panel.
func nameOf(dn string) string { return textutil.Clip(dn, maxDistinguishedName) }

const maxDistinguishedName = 300

// serialOf renders the serial as other tools do, uppercase hex in pairs: `%d` of
// a 20-byte serial cannot be compared with what a CA reported.
func serialOf(cert *x509.Certificate) string {
	if cert.SerialNumber == nil {
		return ""
	}
	raw := fmt.Sprintf("%X", cert.SerialNumber)
	if len(raw)%2 == 1 {
		raw = "0" + raw
	}
	parts := make([]string, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		parts = append(parts, raw[i:i+2])
	}
	return strings.Join(parts, ":")
}

// publicKeyOf names the key type and its size; the type alone does not say
// whether it is strong enough.
func publicKeyOf(cert *x509.Certificate) string {
	switch key := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d bits", key.N.BitLen())
	case *ecdsa.PublicKey:
		if key.Curve != nil && key.Curve.Params() != nil {
			return "ECDSA " + key.Curve.Params().Name
		}
		return "ECDSA"
	case ed25519.PublicKey:
		return "Ed25519"
	}
	return cert.PublicKeyAlgorithm.String()
}

// expiryText is how long is left, in the console's short age form.
func expiryText(notBefore, notAfter, now time.Time) string {
	switch {
	case now.Before(notBefore):
		return "not valid for another " + shortDuration(notBefore.Sub(now))
	case !now.Before(notAfter):
		return "expired " + shortDuration(now.Sub(notAfter)) + " ago"
	default:
		return "expires in " + shortDuration(notAfter.Sub(now))
	}
}

// gradeExpiry decides the color, by a fraction of the certificate's own
// lifetime, clamped at both ends.
//
// A number of days is not one fact: 30 days left is ordinary for a 90-day
// certificate (cert-manager renews at two thirds), late for a one-year one, and
// impossible for a one-hour one. So warn is a third of the lifetime remaining
// (when cert-manager opens renewal) and bad is a twelfth.
//
// Clamped, because a pure fraction is wrong at the extremes:
//
//   - Never further out than 30 days (warn) or 7 days (bad): a ten-year CA would
//     otherwise be red for its last ten months.
//   - Never nearer than 1 hour (warn) or 15 minutes (bad): a one-hour
//     certificate with ten minutes left must not be merely amber.
//
// It grades how urgent expiry is, not whether renewal is overdue; that belongs
// to whatever watches cert-manager.
func gradeExpiry(notBefore, notAfter, now time.Time) string {
	if now.Before(notBefore) || !now.Before(notAfter) {
		// A notBefore in the future fails every handshake like an expiry (clock
		// skew, or a certificate issued moments ago).
		return resources.ClassBad
	}
	remaining := notAfter.Sub(now)
	lifetime := notAfter.Sub(notBefore)

	// Divided rather than multiplied: `remaining * 12` overflows a
	// time.Duration (about 292 years), and internal CAs issue century-long
	// certificates.
	if remaining <= clampDuration(lifetime/12, 15*time.Minute, 7*24*time.Hour) {
		return resources.ClassBad
	}
	if remaining <= clampDuration(lifetime/3, time.Hour, 30*24*time.Hour) {
		return resources.ClassWarn
	}
	return resources.ClassOK
}

func clampDuration(d, low, high time.Duration) time.Duration {
	if d < low {
		return low
	}
	if d > high {
		return high
	}
	return d
}
