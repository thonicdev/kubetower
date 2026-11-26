package releasesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const sums = "1111111111111111111111111111111111111111111111111111111111111111  ./kubetower_0.1.2_linux_amd64.tar.gz\n" +
	"2222222222222222222222222222222222222222222222222222222222222222  ./kubetower-0.1.2-setup.exe\n"

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestAValidSignatureGivesEachAssetItsHash(t *testing.T) {
	pub, priv := keys(t)
	got, err := Verify(pub, []byte(sums), ed25519.Sign(priv, []byte(sums)))
	if err != nil {
		t.Fatal(err)
	}
	if sum, err := got.SHA256("kubetower-0.1.2-setup.exe"); err != nil || sum[0] != '2' {
		t.Errorf("setup: %q, %v", sum, err)
	}
	if _, err := got.SHA256("kubetower-0.1.2-x64.dmg"); !errors.Is(err, ErrNotListed) {
		t.Errorf("an asset with no line: %v, want ErrNotListed", err)
	}
}

func TestEverythingButTheSignedBytesUnderTheKeyIsRefused(t *testing.T) {
	pub, priv := keys(t)
	other, otherPriv := keys(t)
	sig := ed25519.Sign(priv, []byte(sums))
	changed := []byte(sums)
	changed[0] = '3'
	for _, tc := range []struct {
		name      string
		key       ed25519.PublicKey
		sums, sig []byte
		want      error
	}{
		{"a modified checksums.txt", pub, changed, sig, ErrBadSignature},
		{"a truncated signature", pub, []byte(sums), sig[:63], ErrBadSignature},
		{"no signature", pub, []byte(sums), nil, ErrBadSignature},
		{"a signature by another key", pub, []byte(sums), ed25519.Sign(otherPriv, []byte(sums)), ErrBadSignature},
		{"another key than the signer's", other, []byte(sums), sig, ErrBadSignature},
		{"no key", nil, []byte(sums), sig, ErrNoKey},
		{"an empty checksums.txt", pub, nil, sig, ErrMalformed},
	} {
		if _, err := Verify(tc.key, tc.sums, tc.sig); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestASignedButMalformedListIsRefused(t *testing.T) {
	pub, priv := keys(t)
	for name, text := range map[string]string{
		"a short hash":       "abc  ./x\n",
		"a path":             "1111111111111111111111111111111111111111111111111111111111111111  ../etc/passwd\n",
		"two hashes":         "1111111111111111111111111111111111111111111111111111111111111111  ./x\n2222222222222222222222222222222222222222222222222222222222222222  ./x\n",
		"an upper-case hash": "1111111111111111111111111111111111111111111111111111111111111ABC  ./x\n",
		// #154: never a path step, nor a name a space at either end disguises.
		"dot dot":        "1111111111111111111111111111111111111111111111111111111111111111  ..\n",
		"dot":            "1111111111111111111111111111111111111111111111111111111111111111  ./.\n",
		"leading space":  "1111111111111111111111111111111111111111111111111111111111111111   x\n",
		"trailing space": "1111111111111111111111111111111111111111111111111111111111111111  x \n",
	} {
		if _, err := Verify(pub, []byte(text), ed25519.Sign(priv, []byte(text))); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
}

// TestOpenSSLSignsWhatGoVerifies. release.yml signs with `openssl pkeyutl
// -sign -rawin`; the console verifies with crypto/ed25519. The two must agree
// on the bytes signed and the signature's shape.
func TestOpenSSLSignsWhatGoVerifies(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skipf("no openssl here, so the CI format is UNCHECKED: %v", err)
	}
	dir := t.TempDir()
	pub, priv := keys(t)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(in, []byte(sums), 0o600); err != nil {
		t.Fatal(err)
	}
	sigFile := filepath.Join(dir, "checksums.txt.sig")
	if out, err := exec.Command(openssl, "pkeyutl", "-sign", "-rawin", "-inkey", keyFile, "-in", in, "-out", sigFile).CombinedOutput(); err != nil {
		t.Fatalf("openssl: %v: %s", err, out)
	}
	sig, err := os.ReadFile(sigFile)
	if err != nil {
		t.Fatal(err)
	}
	// The public key as `openssl pkey -pubout` writes it, read by ParseKey.
	pubFile := filepath.Join(dir, "pub.pem")
	if out, err := exec.Command(openssl, "pkey", "-in", keyFile, "-pubout", "-out", pubFile).CombinedOutput(); err != nil {
		t.Fatalf("openssl: %v: %s", err, out)
	}
	pubPEM, err := os.ReadFile(pubFile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseKey(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(pub) {
		t.Fatal("the key openssl wrote is not the key it was given")
	}
	if _, err := Verify(parsed, []byte(sums), sig); err != nil {
		t.Fatalf("Go refuses openssl's signature: %v", err)
	}
}

func TestParseKeyRefusesWhatIsNotAnEd25519PublicKey(t *testing.T) {
	_, priv := keys(t)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	for name, data := range map[string][]byte{
		"nothing":       nil,
		"a private key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		"garbage":       pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("x")}),
	} {
		if _, err := ParseKey(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// TestTheBuildCarriesTheReleaseKey. Without it the console verifies nothing and
// installs nothing; the release's sign job checks against the same file.
func TestTheBuildCarriesTheReleaseKey(t *testing.T) {
	if !keyPresent() {
		t.Fatal("internal/releasesig/release-key.pem is empty: commit the public key RELEASE_SIGNING_KEY signs with (#46)")
	}
	if _, err := Key(); err != nil {
		t.Fatal(err)
	}
}
