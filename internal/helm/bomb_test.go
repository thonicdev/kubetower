package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// A release document is remote input, and the page that reads it is the one an
// operator opens on somebody else's cluster.
//
// `byteBudget` counts `len(secret.Data["release"])`, which is the compressed and
// still-base64 length, and it is checked *before* the decode. Without a bound on
// the decompressed size, a single Secret near etcd's own limit decompresses to
// something near a gigabyte inside `io.ReadAll`.

// gzipped is a release value as Helm writes one: base64 of gzip of the bytes.
func gzipped(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("compressing: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the gzip writer: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// TestAReleaseThatDecompressesPastTheCeilingIsRefusedByName.
//
// Two assertions and they fail in opposite directions. The refusal is the
// guard; the *name* matters because a ceiling that simply truncated would hand
// json.Unmarshal half a document and the page would report a syntax error about
// a size problem.
func TestAReleaseThatDecompressesPastTheCeilingIsRefusedByName(t *testing.T) {
	// Highly compressible, which is the whole mechanism: this is a few kilobytes
	// on the wire and well past the ceiling once open.
	bomb := gzipped(t, bytes.Repeat([]byte("A"), maxDecoded+(1<<20)))

	if len(bomb) > 1<<20 {
		t.Fatalf("the compressed form is %d bytes, so this test is not demonstrating what it "+
			"claims: a real Secret cannot exceed about 1.5 MiB", len(bomb))
	}

	_, err := decodeRelease(bomb)
	if err == nil {
		t.Fatal("a release decompressing past the ceiling was read whole; one Secret can then " +
			"allocate about a gigabyte and take the console with it")
	}
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("the refusal is not errTooLarge, so the page cannot tell an oversized document "+
			"from a corrupt one: %v", err)
	}
	if !strings.Contains(err.Error(), "16.0 MiB") && !strings.Contains(err.Error(), "16 MiB") {
		t.Errorf("the refusal does not name the ceiling, which is the half that makes it "+
			"actionable: %v", err)
	}
}

// TestAnOrdinaryReleaseStillReads is the other half: a ceiling that refused
// everything would pass the test above and fail this one. A ceiling that
// swallows real releases turns the Helm page into an empty one, which is the
// failure the package's four states exist to avoid.
func TestAnOrdinaryReleaseStillReads(t *testing.T) {
	const document = `{"name":"argocd","namespace":"argocd","version":7,` +
		`"info":{"status":"deployed","first_deployed":"2026-01-02T03:04:05Z",` +
		`"last_deployed":"2026-02-03T04:05:06Z","deleted":"","description":"Upgrade complete"},` +
		`"chart":{"metadata":{"name":"argo-cd","version":"7.8.2","appVersion":"v2.13.3"}}}`

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"gzipped, as Helm 3 writes it", gzipped(t, []byte(document))},
		// Early Helm 3 wrote plain JSON there, which is why decodeRelease
		// sniffs the magic rather than assuming.
		{"plain JSON, as early Helm 3 wrote it",
			[]byte(base64.StdEncoding.EncodeToString([]byte(document)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := decodeRelease(tc.data)
			if err != nil {
				t.Fatalf("an ordinary release was refused: %v", err)
			}
			if out.Name != "argocd" || out.Version != 7 {
				t.Errorf("decoded %q v%d, want argocd v7", out.Name, out.Version)
			}
			if out.Chart.Metadata.Version != "7.8.2" {
				t.Errorf("chart version = %q, want 7.8.2", out.Chart.Metadata.Version)
			}
		})
	}

	// And one just under the ceiling, because a bound that is off by a factor is
	// indistinguishable from one that is right until something large arrives.
	// The padding goes in a field the decoder ignores, so this exercises the
	// size path and nothing else.
	big := `{"name":"big","version":1,"pad":"` + strings.Repeat("x", maxDecoded-4096) + `"}`
	if _, err := decodeRelease(gzipped(t, []byte(big))); err != nil {
		t.Errorf("a %s document just under the ceiling was refused: %v",
			"16 MiB", err)
	}
}
