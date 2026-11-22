package detail

// The TLS panel, and the five ways it can fail to produce an answer.
//
// Every one of them is a separate test because every one of them is a separate
// sentence on the page: an empty answer and a failed one are different
// answers, and a panel drawing "nothing" for a Secret
// with no tls.crt, a value that is not base64, bytes with no PEM in them and a
// PEM block that is a private key has said the same untrue thing four times.
//
// The most important test in this file is TestThePrivateKeyIsNeverReadOrRendered.
// The obvious implementation of this feature ranges over `.Data` and tries to
// PEM-decode each value, which puts `tls.key` through a parser; the marker
// string is what proves this one does not.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/resources"
)

// --- fixtures, on disk in memory and never in a cluster --------------------------

func signer(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	return key
}

// issue mints one certificate. A nil parent means self-signed, which is the case
// two of these tests are about.
func issue(t *testing.T, template *x509.Certificate, parent *x509.Certificate, parentKey crypto.Signer) (*x509.Certificate, crypto.Signer, string) {
	t.Helper()
	key := signer(t)
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), parentKey)
	if err != nil {
		t.Fatalf("issuing %s: %v", template.Subject, err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("re-reading %s: %v", template.Subject, err)
	}
	return parsed, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func rootTemplate(name string, from, to time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(4242),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"KubeTower test CA"}},
		NotBefore:             from,
		NotAfter:              to,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
}

func leafTemplate(name string, hosts []string, ips []net.IP, from, to time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: bigSerial("0f1e2d3c4b5a69788796a5b4c3d2e1f0"),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    from,
		NotAfter:     to,
		DNSNames:     hosts,
		IPAddresses:  ips,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
}

// certSecret builds a Secret the way the dynamic client hands one over: values
// base64-encoded, because that is what the API stores.
func certSecret(secretType string, data map[string]string) *unstructured.Unstructured {
	encoded := map[string]any{}
	for key, value := range data {
		encoded[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	object := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "shop-tls", "namespace": "shop"},
		"data":       encoded,
	}
	if secretType != "" {
		object["type"] = secretType
	}
	return &unstructured.Unstructured{Object: object}
}

// --- the happy path ---------------------------------------------------------------

// A leaf, its intermediate, and the root beside them in ca.crt.
//
// "A file with two certificates rendered as one is a wrong answer that looks
// right" is the whole reason the chain is a list: a bundle is the leaf plus
// whatever was appended to it, and reading only the first block would report an
// expiry, an issuer and a set of names that are all individually true and add up
// to a lie about what the server presents.
func TestTheCertificatePanelReadsALeafAndItsChain(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	root, rootKey, rootPEM := issue(t, rootTemplate("KubeTower Test Root", now.AddDate(-2, 0, 0), now.AddDate(8, 0, 0)), nil, nil)
	intermediate, intermediateKey, intermediatePEM := issue(t,
		rootTemplate("KubeTower Test Intermediate", now.AddDate(-1, 0, 0), now.AddDate(4, 0, 0)), root, rootKey)
	_, _, leafPEM := issue(t,
		leafTemplate("shop.example.com",
			[]string{"shop.example.com", "*.shop.example.com"},
			[]net.IP{net.ParseIP("10.0.0.7")},
			now.Add(-10*24*time.Hour), now.Add(80*24*time.Hour)),
		intermediate, intermediateKey)

	set := certificatesOf(certSecret(TLSSecretType, map[string]string{
		"tls.crt": leafPEM + intermediatePEM,
		"tls.key": "-----BEGIN EC PRIVATE KEY-----\nnot-read\n-----END EC PRIVATE KEY-----\n",
		"ca.crt":  rootPEM,
	}), "Secret", now)

	if set == nil {
		t.Fatal("a kubernetes.io/tls Secret produced no certificate panel at all")
	}
	if len(set.Bundles) != 2 {
		t.Fatalf("got %d bundle(s), want tls.crt and ca.crt", len(set.Bundles))
	}
	if set.Bundles[0].Key != "tls.crt" || set.Bundles[1].Key != "ca.crt" {
		t.Fatalf("bundles are %q and %q, want tls.crt first", set.Bundles[0].Key, set.Bundles[1].Key)
	}
	if len(set.PrivateKeys) != 1 || set.PrivateKeys[0] != "tls.key" {
		t.Errorf("PrivateKeys = %v, want [tls.key] — the panel cannot say the key was left alone "+
			"if it does not know the key is there", set.PrivateKeys)
	}

	chain := set.Bundles[0]
	if chain.Note != "" {
		t.Errorf("a whole chain reported a problem: %q", chain.Note)
	}
	if len(chain.Certificates) != 2 {
		t.Fatalf("tls.crt held %d certificate(s), want the leaf and its intermediate — "+
			"a bundle read as one certificate is the defect this list exists for", len(chain.Certificates))
	}

	leaf := chain.Certificates[0]
	if leaf.Position != 1 || leaf.Role != "leaf" {
		t.Errorf("first certificate is #%d %q, want #1 leaf", leaf.Position, leaf.Role)
	}
	if leaf.SelfSigned {
		t.Error("a leaf signed by an intermediate is reported as self-signed")
	}
	if !strings.Contains(leaf.Subject, "shop.example.com") {
		t.Errorf("Subject = %q", leaf.Subject)
	}
	if !strings.Contains(leaf.Issuer, "KubeTower Test Intermediate") {
		t.Errorf("Issuer = %q, want the intermediate that signed it", leaf.Issuer)
	}
	want := []string{"shop.example.com", "*.shop.example.com", "10.0.0.7"}
	if strings.Join(leaf.SANs, ",") != strings.Join(want, ",") {
		t.Errorf("SANs = %v, want %v — the wildcard and the IP are both names a browser matches", leaf.SANs, want)
	}
	if leaf.SANsNote != "" {
		t.Errorf("three names produced a cap note: %q", leaf.SANsNote)
	}
	if leaf.ExpiryClass != resources.ClassOK {
		t.Errorf("ExpiryClass = %q for a certificate 80 days into a 90-day life, want ok", leaf.ExpiryClass)
	}
	if !strings.Contains(leaf.Expiry, "expires in 80d") {
		t.Errorf("Expiry = %q, want the short form this console uses for an age", leaf.Expiry)
	}
	if leaf.PublicKey != "ECDSA P-256" {
		t.Errorf("PublicKey = %q, want the type and its size", leaf.PublicKey)
	}
	if leaf.SignatureAlgorithm == "" {
		t.Error("no signature algorithm")
	}
	// Uppercase hex in pairs, byte for byte, as every other tool prints it. The
	// exact string and not a shape: `fmt.Sprintf("%d", ...)` of the same serial
	// also comes out as colon-separated uppercase groups — 38 decimal digits cut
	// into pairs — so "contains a colon" is a test that a decimal serial passes.
	// A 38-digit number cannot be compared against what the CA reported.
	if want := "0F:1E:2D:3C:4B:5A:69:78:87:96:A5:B4:C3:D2:E1:F0"; leaf.Serial != want {
		t.Errorf("Serial = %q, want %q", leaf.Serial, want)
	}
	if leaf.IsCA {
		t.Error("the leaf is reported as a certificate authority")
	}

	if middle := chain.Certificates[1]; middle.Position != 2 || middle.Role != "intermediate" {
		t.Errorf("second certificate is #%d %q, want #2 intermediate", middle.Position, middle.Role)
	}

	ca := set.Bundles[1]
	if len(ca.Certificates) != 1 {
		t.Fatalf("ca.crt held %d certificate(s), want the root", len(ca.Certificates))
	}
	if !ca.Certificates[0].SelfSigned || ca.Certificates[0].Role != "root" {
		t.Errorf("the root is %q, self-signed %v — want a self-signed root",
			ca.Certificates[0].Role, ca.Certificates[0].SelfSigned)
	}
}

// A self-signed *serving* certificate, which is not a root and must not be
// reported as one.
//
// This is why SelfSigned is a name comparison plus a signature check rather than
// `CheckSignatureFrom`: that refuses a parent whose BasicConstraints say it is
// not a CA, which is exactly what this certificate is, so it would report the
// commonest self-signed certificate in a cluster as chained to something.
func TestASelfSignedServingCertificateIsALeafAndSaysItSignedItself(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	_, _, leafPEM := issue(t,
		leafTemplate("dev.internal", []string{"dev.internal"}, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0)),
		nil, nil)

	set := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": leafPEM}), "Secret", now)
	if set == nil || len(set.Bundles) != 1 || len(set.Bundles[0].Certificates) != 1 {
		t.Fatalf("the panel did not read one certificate: %+v", set)
	}
	got := set.Bundles[0].Certificates[0]
	if got.Role != "leaf" {
		t.Errorf("Role = %q, want leaf — IsCA is false on it", got.Role)
	}
	if !got.SelfSigned {
		t.Error("a certificate that signed itself is not reported as self-signed, which is the " +
			"one thing an operator debugging a chain error needs to know")
	}
}

// An RSA key's size is the fact, not the word RSA.
func TestTheKeySizeIsReportedAndNotOnlyTheAlgorithm(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating an RSA key: %v", err)
	}
	template := leafTemplate("rsa.internal", []string{"rsa.internal"}, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0))
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	block := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	set := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": block}), "Secret", now)
	if set == nil || len(set.Bundles) == 0 || len(set.Bundles[0].Certificates) == 0 {
		t.Fatalf("nothing read: %+v", set)
	}
	if got := set.Bundles[0].Certificates[0].PublicKey; got != "RSA 2048 bits" {
		t.Errorf("PublicKey = %q, want \"RSA 2048 bits\" — \"RSA\" alone does not answer "+
			"whether the key is still strong enough", got)
	}
}

// An empty SAN list is two different facts, and the panel must not read them as
// one.
//
// On a live Let's Encrypt bundle of a leaf, an intermediate and the
// cross-signed ISRG root, saying that both CA certificates are "valid for no
// host as far as a browser is concerned" is true, useless, and phrased as an
// alarm about two certificates behaving exactly as designed. On a *leaf* the same
// sentence is the answer to why the site is refused.
func TestAnEmptyNameListIsSaidDifferentlyOnALeafAndOnACA(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	_, _, leafBlock := issue(t, leafTemplate("legacy", nil, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0)), nil, nil)
	leaf := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": leafBlock}), "Secret", now).
		Bundles[0].Certificates[0]
	if len(leaf.SANs) != 0 {
		t.Fatalf("SANs = %v, want none", leaf.SANs)
	}
	if !strings.Contains(leaf.SANsNote, "valid for no host") {
		t.Errorf("a serving certificate valid for no host says %q — an empty Names row reads as "+
			"a panel that failed to look, and this is the reason a browser refuses the site",
			leaf.SANsNote)
	}

	_, _, caBlock := issue(t, rootTemplate("Internal Root", now.AddDate(-1, 0, 0), now.AddDate(9, 0, 0)), nil, nil)
	ca := certificatesOf(certSecret("Opaque", map[string]string{"ca.crt": caBlock}), "Secret", now).
		Bundles[0].Certificates[0]
	if ca.SANsNote == "" {
		t.Fatal("the CA's Names row is blank, which reads as unread")
	}
	if strings.Contains(ca.SANsNote, "valid for no host") {
		t.Errorf("a certificate authority is told it is valid for no host: %q. It is not a "+
			"server and has no business carrying a SAN — the sentence is a false alarm about a "+
			"certificate that is working", ca.SANsNote)
	}
}

// The cap admits itself, which is the bargain RelationsNote already makes.
func TestALongNameListIsCappedAndSaysHowMany(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	hosts := make([]string, maxSANs+7)
	for i := range hosts {
		hosts[i] = "host-" + itoa(i) + ".example.com"
	}
	_, _, block := issue(t, leafTemplate("many", hosts, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0)), nil, nil)

	got := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": block}), "Secret", now).
		Bundles[0].Certificates[0]
	if len(got.SANs) != maxSANs {
		t.Errorf("drew %d names, want the cap of %d", len(got.SANs), maxSANs)
	}
	if !strings.Contains(got.SANsNote, "7 more") {
		t.Errorf("SANsNote = %q, want the number left out — a capped list that does not admit "+
			"it is a lie the operator acts on", got.SANsNote)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// --- the five refusals ------------------------------------------------------------

// Refusal 1. The type promises a certificate and the data carries none.
func TestATLSTypedSecretWithNoCertificateKeySaysSo(t *testing.T) {
	now := time.Now()
	set := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.key": "irrelevant"}), "Secret", now)
	if set == nil {
		t.Fatal("no panel at all, so a Secret whose type promises a certificate and has none looks " +
			"like every other Secret and stays broken")
	}
	if len(set.Bundles) != 0 {
		t.Fatalf("bundles from a Secret with no certificate key: %+v", set.Bundles)
	}
	if !strings.Contains(set.Note, "tls.crt") || !strings.Contains(set.Note, TLSSecretType) {
		t.Errorf("Note = %q, want it to name the type and the missing key", set.Note)
	}
}

// Refusal 2. The stored value is not base64, so nothing decodes.
func TestAValueThatIsNotBase64IsSaidRatherThanShownEmpty(t *testing.T) {
	// Built by hand rather than through secretObject, which encodes: the point is
	// a stored value that is *not* valid base64.
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "broken", "namespace": "shop"},
		"type":     TLSSecretType,
		"data":     map[string]any{"tls.crt": "-----BEGIN CERTIFICATE----- not base64 at all"},
	}}

	set := certificatesOf(object, "Secret", time.Now())
	if set == nil || len(set.Bundles) != 1 {
		t.Fatalf("no bundle for a present-but-undecodable key: %+v", set)
	}
	bundle := set.Bundles[0]
	if len(bundle.Certificates) != 0 {
		t.Fatalf("certificates out of a value that is not base64: %+v", bundle.Certificates)
	}
	if !strings.Contains(bundle.Note, "base64") {
		t.Errorf("Note = %q, want it to name base64 — otherwise this is indistinguishable "+
			"from a Secret with no certificate in it", bundle.Note)
	}
}

// Refusal 3. It decodes and holds no PEM block.
func TestBytesWithNoPEMBlockAreSaidRatherThanShownEmpty(t *testing.T) {
	set := certificatesOf(certSecret(TLSSecretType, map[string]string{
		"tls.crt": "this is a certificate, honestly, all forty-eight bytes",
	}), "Secret", time.Now())
	if set == nil || len(set.Bundles) != 1 {
		t.Fatalf("no bundle: %+v", set)
	}
	bundle := set.Bundles[0]
	if len(bundle.Certificates) != 0 {
		t.Fatalf("certificates out of bytes with no PEM in them: %+v", bundle.Certificates)
	}
	if !strings.Contains(bundle.Note, "PEM") || !strings.Contains(bundle.Note, "BEGIN CERTIFICATE") {
		t.Errorf("Note = %q, want it to say there is no PEM block and what one looks like", bundle.Note)
	}
}

// Refusal 4, both halves: the first block is not a certificate, and — the case
// that matters more — a *later* block is not, so the chain above it is partial
// and must say so beside itself rather than being drawn as whole.
func TestAPEMBlockThatIsNotACertificateIsSaidBesideWhateverDidParse(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	_, _, leafPEM := issue(t,
		leafTemplate("shop.example.com", []string{"shop.example.com"}, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0)),
		nil, nil)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("MARKER-NEVER-RENDER")}))
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("whatever")}))

	t.Run("the first block", func(t *testing.T) {
		set := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": csrPEM}), "Secret", now)
		bundle := set.Bundles[0]
		if len(bundle.Certificates) != 0 {
			t.Fatalf("certificates out of a CSR: %+v", bundle.Certificates)
		}
		if !strings.Contains(bundle.Note, "signing request") {
			t.Errorf("Note = %q, want it to name what the block actually is", bundle.Note)
		}
	})

	t.Run("a later block, so the chain is partial", func(t *testing.T) {
		set := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": leafPEM + keyPEM}), "Secret", now)
		bundle := set.Bundles[0]
		if len(bundle.Certificates) != 1 {
			t.Fatalf("got %d certificate(s), want the leaf that did parse", len(bundle.Certificates))
		}
		if bundle.Note == "" {
			t.Fatal("the block that did not parse is silent, so a partial chain is drawn as a whole one")
		}
		if !strings.Contains(bundle.Note, "as far as #1") {
			t.Errorf("Note = %q, want it to say how far the chain was read", bundle.Note)
		}
		// And the block's own bytes are described, never echoed.
		if strings.Contains(bundle.Note, "MARKER-NEVER-RENDER") {
			t.Errorf("the note carries the block's contents: %q", bundle.Note)
		}
	})

	t.Run("an unrecognized block type is described and not quoted", func(t *testing.T) {
		// A PEM type line is arbitrary text out of somebody's file. Printing it
		// verbatim is printing whatever was put there.
		odd := string(pem.EncodeToMemory(&pem.Block{Type: "SUPERSECRET password=hunter2", Bytes: []byte("x")}))
		set := certificatesOf(certSecret(TLSSecretType, map[string]string{"tls.crt": odd}), "Secret", now)
		if note := set.Bundles[0].Note; strings.Contains(note, "hunter2") {
			t.Errorf("Note = %q, want an unrecognized block described rather than quoted", note)
		} else if !strings.Contains(note, "does not recognize") {
			t.Errorf("Note = %q, want it to say the block was not recognized", note)
		}
	})
}

// Refusal 5 lives one layer up — the identity could not read the Secret at all,
// so there is no object to build a panel from and `internal/web`'s drawer prints
// the API's own refusal. What this file owes it is that nothing here invents a
// panel out of an object that is not there.
func TestNoPanelIsInventedWithoutAnObject(t *testing.T) {
	if set := certificatesOf(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "unreadable"},
	}}, "Secret", time.Now()); set != nil {
		t.Fatalf("a Secret with no data block grew a certificate panel: %+v", set)
	}
}

// --- the value that must never be read -------------------------------------------

// The private key is never decoded, never parsed and never in the view model.
//
// The obvious implementation of this feature ranges over `.Data` and tries to
// PEM-decode every value, which reaches `tls.key`; this drives a Secret whose key
// material — and a decoy `server.crt`, and a `password` — all carry the same
// marker, and asserts the marker appears nowhere in what was built.
func TestThePrivateKeyIsNeverReadOrRendered(t *testing.T) {
	const marker = "MARKER-THIS-IS-PRIVATE-KEY-MATERIAL"
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	_, _, leafPEM := issue(t,
		leafTemplate("shop.example.com", []string{"shop.example.com"}, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0)),
		nil, nil)

	set := certificatesOf(certSecret(TLSSecretType, map[string]string{
		"tls.crt": leafPEM,
		"tls.key": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte(marker)})),
		// Two decoys under names this file does not know: a key stored anywhere
		// but tls.key must be just as unreachable, which is what a name list
		// gives and a walk of the data block does not.
		"server.key": marker,
		"password":   marker,
	}), "Secret", now)

	if set == nil {
		t.Fatal("nothing was read at all")
	}
	if len(set.PrivateKeys) != 1 || set.PrivateKeys[0] != "tls.key" {
		t.Errorf("PrivateKeys = %v, want [tls.key] — and `server.key` deliberately not, because "+
			"this file reads a list of names", set.PrivateKeys)
	}
	if found := grepSet(set, marker); found != "" {
		t.Fatalf("key material reached the view model, in %s", found)
	}

	// And the structural half, which is what the guarantee actually rests on: a
	// bundle exists only for a name on `certificateKeys`. Without this the marker
	// assertion above passes even when the implementation ranges over the whole
	// data block — the PEM type allowlist catches the leak on the way out, so the
	// value stays off the page while a private key is being decoded and parsed on
	// every drawer open. Both are wanted; only this one fails when the walk comes
	// back.
	for _, bundle := range set.Bundles {
		if bundle.Key != "tls.crt" && bundle.Key != "ca.crt" {
			t.Errorf("a bundle was built from %q. This panel reads a list of *names* and never "+
				"walks the data block; that is the whole of what keeps a private key — under "+
				"tls.key or any other name — away from a parser", bundle.Key)
		}
	}
}

// grepSet walks everything the panel would render and returns where the marker
// turned up, or "".
func grepSet(set *CertificateSet, marker string) string {
	if strings.Contains(set.Note, marker) {
		return "CertificateSet.Note"
	}
	for _, bundle := range set.Bundles {
		if strings.Contains(bundle.Note, marker) {
			return "bundle " + bundle.Key + " Note"
		}
		for _, cert := range bundle.Certificates {
			for label, value := range map[string]string{
				"Subject": cert.Subject, "Issuer": cert.Issuer, "Serial": cert.Serial,
				"Expiry": cert.Expiry, "PublicKey": cert.PublicKey,
				"SignatureAlgorithm": cert.SignatureAlgorithm, "Role": cert.Role,
				"SANsNote": cert.SANsNote,
			} {
				if strings.Contains(value, marker) {
					return "certificate " + label
				}
			}
			for _, san := range cert.SANs {
				if strings.Contains(san, marker) {
					return "certificate SANs"
				}
			}
		}
	}
	return ""
}

// --- what the panel is drawn for --------------------------------------------------

// An Opaque Secret carrying a tls.crt is read, because that is the case an
// operator hits: cert-manager's additionalOutputFormats, ingress-nginx's default
// certificate, and anything made by hand.
func TestAnOpaqueSecretCarryingACertificateIsRead(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	_, _, leafPEM := issue(t,
		leafTemplate("opaque.example.com", []string{"opaque.example.com"}, nil, now.Add(-time.Hour), now.AddDate(1, 0, 0)),
		nil, nil)

	set := certificatesOf(certSecret("Opaque", map[string]string{"tls.crt": leafPEM}), "Secret", now)
	if set == nil || len(set.Bundles) != 1 || len(set.Bundles[0].Certificates) != 1 {
		t.Fatalf("keying on the type alone misses the commonest case: %+v", set)
	}
}

// And the cluster's own CA, which is a ConfigMap and whose `data` is plain text
// rather than base64. Reading one as the other is how a certificate comes out as
// its own base64 and reports no PEM block.
func TestTheClusterCAConfigMapIsRead(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	_, _, rootPEM := issue(t, rootTemplate("kubernetes", now.AddDate(-1, 0, 0), now.AddDate(9, 0, 0)), nil, nil)

	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "kube-root-ca.crt", "namespace": "shop"},
		"data":     map[string]any{"ca.crt": rootPEM},
	}}
	set := certificatesOf(object, "ConfigMap", now)
	if set == nil || len(set.Bundles) != 1 || len(set.Bundles[0].Certificates) != 1 {
		t.Fatalf("kube-root-ca.crt was not read: %+v", set)
	}
	if len(set.PrivateKeys) != 0 {
		t.Errorf("a ConfigMap was reported as holding a private key: %v", set.PrivateKeys)
	}
	if got := set.Bundles[0].Certificates[0]; got.Role != "root" || !got.SelfSigned {
		t.Errorf("the cluster CA is %q, self-signed %v", got.Role, got.SelfSigned)
	}
}

// Nothing at all for an object with no certificate in it, which is almost all of
// them: a heading over an empty card on every ConfigMap in the cluster is worse
// than the feature being absent.
func TestNothingIsDrawnForAnObjectWithNoCertificate(t *testing.T) {
	for _, c := range []struct {
		name   string
		object *unstructured.Unstructured
		kind   string
	}{
		{"an ordinary Opaque Secret", certSecret("Opaque", map[string]string{"password": "hunter2"}), "Secret"},
		{"an ordinary ConfigMap", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"data": map[string]any{"nginx.conf": "worker_processes 4;"},
		}}, "ConfigMap"},
		{"a Pod", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"data": map[string]any{"tls.crt": "not a real field on a pod"},
		}}, "Pod"},
	} {
		if set := certificatesOf(c.object, c.kind, time.Now()); set != nil {
			t.Errorf("%s grew a certificate panel: %+v", c.name, set)
		}
	}
}

// --- the grading -----------------------------------------------------------------

// The bands are a fraction of the certificate's own lifetime, clamped at both
// ends — and every case below is one that some *other* plausible rule gets
// wrong. That is the point of the table: an absolute number of days is wrong for
// short and long lifetimes alike, and an unclamped fraction is wrong at both
// extremes in the other direction.
func TestTheExpiryBandsGradeOnTheLifetimeAndNotOnlyOnDays(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	for _, c := range []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		want      string
		why       string
	}{
		{
			"a 90-day certificate two days old", now.Add(-2 * day), now.Add(88 * day),
			resources.ClassOK, "freshly renewed",
		},
		{
			"a 90-day certificate at its renewal point", now.Add(-61 * day), now.Add(29 * day),
			resources.ClassWarn,
			"cert-manager renews at two thirds elapsed, so under a third left means renewal " +
				"has not happened yet",
		},
		{
			"a 90-day certificate with five days left", now.Add(-85 * day), now.Add(5 * day),
			resources.ClassBad,
			"under a twelfth of the lifetime, which on 90 days is 7.5 — renewal has been " +
				"failing for three weeks",
		},
		{
			"a one-year certificate with 25 days left", now.AddDate(-1, 0, 0).Add(25 * day), now.Add(25 * day),
			resources.ClassWarn,
			"the warn clamp: a third of a year is 121 days, and painting a certificate amber " +
				"four months out says nothing. 30 days is where it starts to matter",
		},
		{
			"a one-year certificate with 40 days left", now.AddDate(-1, 0, 0).Add(40 * day), now.Add(40 * day),
			resources.ClassOK,
			"the same clamp from the other side. Unclamped, a third of a year would have made " +
				"this amber — and it is 40 days from expiry, which is not urgent. This badge " +
				"grades urgency; whether cert-manager is overdue is a different question",
		},
		{
			"a ten-year internal CA with two weeks left", now.AddDate(-10, 0, 0).Add(14 * day), now.Add(14 * day),
			resources.ClassWarn, "inside the 30-day warn clamp and outside the 7-day bad one",
		},
		{
			"a ten-year internal CA with three days left", now.AddDate(-10, 0, 0).Add(3 * day), now.Add(3 * day),
			resources.ClassBad,
			"the bad clamp. A twelfth of ten years is ten months, so an unclamped fraction " +
				"would have had this red since last autumn and red would have stopped meaning " +
				"anything by now",
		},
		{
			"a hundred-year self-signed CA with five years left",
			now.AddDate(-95, 0, 0), now.AddDate(5, 0, 0),
			resources.ClassOK,
			"the clamp that matters most for the fraction rule's credibility: a twelfth of a " +
				"century is over eight years, so unclamped this certificate is red for its " +
				"last eight and an operator learns to ignore the color",
		},
		{
			"a 24-hour workload certificate at its renewal point",
			now.Add(-16 * time.Hour), now.Add(8 * time.Hour),
			resources.ClassWarn,
			"a third left, and an absolute 30-day rule would have called this red for the " +
				"whole of its life — every SPIFFE certificate in the cluster, permanently",
		},
		{
			"a one-hour workload certificate with ten minutes left",
			now.Add(-50 * time.Minute), now.Add(10 * time.Minute),
			resources.ClassBad,
			"the lower clamp. A sixth of its life left is above a twelfth, so the fraction " +
				"alone grades ten minutes from an outage as merely amber",
		},
		{
			"expired an hour ago", now.Add(-90 * day), now.Add(-time.Hour),
			resources.ClassBad, "the whole point",
		},
		{
			"not valid for another hour", now.Add(time.Hour), now.Add(90 * day),
			resources.ClassBad,
			"a certificate whose notBefore is in the future fails every handshake exactly as " +
				"an expired one does, and reads as healthy on any rule that only looks at notAfter",
		},
	} {
		if got := gradeExpiry(c.notBefore, c.notAfter, now); got != c.want {
			t.Errorf("%s: graded %q, want %q — %s", c.name, got, c.want, c.why)
		}
	}
}

// And the words beside the color say which of the three states it is.
func TestTheExpiryTextNamesTheState(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		want      string
	}{
		{"valid", now.Add(-time.Hour), now.Add(50 * 24 * time.Hour), "expires in 50d"},
		{"expired", now.Add(-90 * 24 * time.Hour), now.Add(-3 * 24 * time.Hour), "expired 3d0h ago"},
		{"not yet", now.Add(2 * time.Hour), now.Add(90 * 24 * time.Hour), "not valid for another 2h0m"},
	} {
		if got := expiryText(c.notBefore, c.notAfter, now); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: %q, want it to begin %q", c.name, got, c.want)
		}
	}
}

// bigSerial is a 16-byte serial, which is what a real CA issues and what does
// not fit in an int64 — the case serialOf's hex formatting exists for.
func bigSerial(hex string) *big.Int {
	n, ok := new(big.Int).SetString(hex, 16)
	if !ok {
		panic("bad serial fixture: " + hex)
	}
	return n
}

// An in-cluster CA keeps its key under `ca.key`, not `tls.key`, and the page has
// to say so or it says nothing about the more dangerous of the two.
//
// `cilium-ca` in kube-system is an Opaque Secret holding `ca.crt` and `ca.key`.
// With only `tls.key` on the list the certificate would be read and the private
// key beside it go unmentioned — a silence that reads as "there is no key
// here".
func TestAnInClusterCAsPrivateKeyIsNamedTooAndStillNotRead(t *testing.T) {
	const marker = "MARKER-CA-KEY-MATERIAL"
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	_, _, caPEM := issue(t, rootTemplate("Cilium CA", now.AddDate(-1, 0, 0), now.AddDate(2, 0, 0)), nil, nil)

	set := certificatesOf(certSecret("Opaque", map[string]string{
		"ca.crt": caPEM,
		"ca.key": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte(marker)})),
	}), "Secret", now)

	if set == nil || len(set.Bundles) != 1 || len(set.Bundles[0].Certificates) != 1 {
		t.Fatalf("the CA certificate was not read: %+v", set)
	}
	if len(set.PrivateKeys) != 1 || set.PrivateKeys[0] != "ca.key" {
		t.Errorf("PrivateKeys = %v, want [ca.key]", set.PrivateKeys)
	}
	for _, bundle := range set.Bundles {
		if bundle.Key == "ca.key" {
			t.Error("ca.key was decoded and walked for PEM, which is exactly what the name list " +
				"exists to prevent")
		}
	}
	if found := grepSet(set, marker); found != "" {
		t.Fatalf("key material reached the view model, in %s", found)
	}
}

// Self-issued is not self-signed, and the difference is a signature check.
//
// A certificate whose issuer DN equals its subject DN has said nothing about who
// signed it: a CA re-issued under the same name by somebody else looks identical
// on the names alone. "Self-signed" is the sentence an operator reads as "no
// chain leads to a public root", so getting it from a string comparison would
// put that sentence on a certificate that chains perfectly well.
//
// This test pins the signature check: deleting it would leave every other test
// in this file green.
func TestSelfIssuedIsNotSelfSigned(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	// A CA whose subject is the very name the certificate below will also carry.
	impostor, impostorKey, _ := issue(t,
		rootTemplate("Shared Name CA", now.AddDate(-3, 0, 0), now.AddDate(5, 0, 0)), nil, nil)

	// Same subject as its issuer, and a different key behind the signature.
	template := rootTemplate("Shared Name CA", now.AddDate(-1, 0, 0), now.AddDate(4, 0, 0))
	template.SerialNumber = bigSerial("11")
	_, _, block := issue(t, template, impostor, impostorKey)

	set := certificatesOf(certSecret("Opaque", map[string]string{"ca.crt": block}), "Secret", now)
	got := set.Bundles[0].Certificates[0]

	if got.Subject != got.Issuer {
		t.Fatalf("the fixture is wrong: subject %q, issuer %q — they must match for this "+
			"test to say anything", got.Subject, got.Issuer)
	}
	if got.SelfSigned {
		t.Error("a certificate signed by another key is reported as self-signed. The names " +
			"matching is not the answer; verifying the signature against its own public key is.")
	}
	if got.Role != "intermediate" {
		t.Errorf("Role = %q, want intermediate — a CA that did not sign itself", got.Role)
	}
}
