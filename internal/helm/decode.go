package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/textutil"
)

// The storage format. A Helm 3 release revision is one Secret in the release's
// own namespace:
//
//	name   sh.helm.release.v1.<release>.v<revision>
//	type   helm.sh/release.v1
//	labels owner=helm, name=<release>, version=<revision>, status=<status>,
//	       modifiedAt=<unix seconds>
//	data   release: <base64> → <base64> → <gzip> → JSON
//
// The base64 is double: the outer layer is Kubernetes' (client-go has undone
// it by the time `Secret.Data` is read), the inner one is Helm's storage
// driver's own. Undoing only one leaves text no JSON parser accepts, and a
// cluster running releases would read as running none.
//
// The gzip is conditional, as in Helm's own `decodeRelease`: releases written
// by early Helm 3 versions are plain JSON, and decompressing unconditionally
// would report them as corrupt.

// magicGzip is the first three bytes of a gzip stream (0x1f 0x8b, then the
// deflate method), the bytes Helm sniffs for.
var magicGzip = []byte{0x1f, 0x8b, 0x08}

// releaseKey is the one key a release Secret carries.
const releaseKey = "release"

// storedRelease is the release document, narrowed to what this console draws.
//
// Narrow on purpose: a revision's document can be megabytes, mostly chart
// templates and the manifest, and fields absent from this struct are skipped
// by encoding/json without being allocated. A `Manifest string` here would
// retain that text for every revision of every release on the list.
//
// Field names are Helm's spelling: `first_deployed` is snake_case (release.Info's
// tags), `appVersion` is not (Chart.yaml). A wrong name is silent: the field
// just reads as its zero value.
type storedRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Version is the revision: Helm's document calls it `version`, everything
	// a person sees calls it revision, as this package does.
	Version int `json:"version"`
	Info    struct {
		FirstDeployed helmTime `json:"first_deployed"`
		LastDeployed  helmTime `json:"last_deployed"`
		Deleted       helmTime `json:"deleted"`
		Description   string   `json:"description"`
		Status        string   `json:"status"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			AppVersion string `json:"appVersion"`
			Deprecated bool   `json:"deprecated"`
		} `json:"metadata"`
	} `json:"chart"`

	// undecoded marks an entry assembled from the Secret's labels alone,
	// because its document would not decode. Unexported so no decoder can be
	// told to fill it: it describes the read, not the release.
	undecoded bool
}

// storedPayload is the part of the release document only a release's own page
// draws: the rendered `NOTES.txt` and the supplied values.
//
// Kept out of storedRelease, which is decoded for every revision of every
// release on the list; this is decoded by decodeCurrent for one revision only.
//
// Absent is a real answer for both: no `NOTES.txt`, or an install with no
// values, means there are none, and the page says so. "Could not be read"
// is a third state, carried by `History.PayloadNote`.
type storedPayload struct {
	Info struct {
		// Notes is Helm's rendering of `NOTES.txt` at this revision, already
		// templated, hence stored per revision.
		Notes string `json:"notes"`
	} `json:"info"`
	// Config is the supplied values only (`helm get values` without
	// `--all`); the chart's defaults are in `chart.values`. The operator's
	// question is "what did we change", not the merged result.
	//
	// Raw, so Helm's key order and numbers survive to the renderer; a round
	// trip through `map[string]any` would reorder keys and turn numbers into
	// float64.
	Config json.RawMessage `json:"config"`
}

// helmTime is one of Helm's timestamps. Helm writes a zero time as `null`, and
// live documents also carry `""` (as `"deleted"` does). `time.Time` refuses
// both, which would fail the whole release on a field nothing draws, so both
// read as zero.
type helmTime struct{ time.Time }

func (t *helmTime) UnmarshalJSON(b []byte) error {
	text := strings.TrimSpace(string(b))
	switch text {
	case "", "null", `""`:
		t.Time = time.Time{}
		return nil
	}
	return t.Time.UnmarshalJSON(b)
}

// errNoReleaseKey is a Secret typed as a Helm release with no `release` key:
// not corruption but likely a hand-made Secret, and the page says so.
var errNoReleaseKey = errors.New("no `release` key")

// errTooLarge is a release document that decompresses past maxDecoded. Its own
// error rather than a truncation: "not what Helm writes" and "too large to be
// what Helm writes" are different problems.
var errTooLarge = errors.New("this release document is too large to read")

// maxDecoded is the ceiling on one decompressed release document, in bytes:
// a few times the largest plausible real document (etcd caps the compressed
// object near 1.5 MiB), and far below what a hostile one reaches.
const maxDecoded = 16 << 20

// decodeRelease unwraps one Secret's `release` value. `data` is
// `Secret.Data`, with Kubernetes' base64 already undone.
func decodeRelease(data []byte) (storedRelease, error) {
	var out storedRelease
	raw, err := unwrapRelease(data)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("reading the release document: %w", err)
	}
	return out, nil
}

// decodeCurrent reads both storedRelease and storedPayload out of one
// revision's Secret, for a release's own page.
//
// One unwrap and two unmarshals: the second re-scans a document already in
// memory and allocates only the payload, which keeps storedRelease narrow for
// the list (see its comment). The manifest is not read here at all: it is a
// size problem, not a decoding one.
func decodeCurrent(data []byte) (storedRelease, storedPayload, error) {
	var release storedRelease
	var payload storedPayload
	raw, err := unwrapRelease(data)
	if err != nil {
		return release, payload, err
	}
	if err := json.Unmarshal(raw, &release); err != nil {
		return release, payload, fmt.Errorf("reading the release document: %w", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		// Unreachable while the first decode succeeded. Reported anyway: a
		// page silently without notes would be a failure drawn as an absence.
		return release, payload, fmt.Errorf("reading the release payload: %w", err)
	}
	return release, payload, nil
}

// unwrapRelease undoes Helm's own base64 and its conditional gzip, and returns
// the release document as JSON. Written once, for both decoders.
func unwrapRelease(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errNoReleaseKey
	}

	// StdEncoding: Helm writes with padding.
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("un-base64ing the release: %w", err)
	}

	if len(raw) > 3 && bytes.Equal(raw[:3], magicGzip) {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("opening the gzip stream: %w", err)
		}
		defer zr.Close()

		// Bounded on what comes out. The caller's byteBudget counts the
		// compressed, still-base64 length before this runs, and gzip reaches
		// about 1000:1, so one Secret near etcd's size limit could decompress
		// to around a gigabyte and kill the console. Writing one needs only
		// `create secrets` in some namespace, which many developers have.
		//
		// Reading one byte past the cap reports errTooLarge rather than
		// truncating, which would blame the JSON syntax. An oversized release
		// is listed in `Undecoded` with a sentence saying why.
		raw, err = io.ReadAll(io.LimitReader(zr, maxDecoded+1))
		if err != nil {
			return nil, fmt.Errorf("decompressing the release: %w", err)
		}
		if len(raw) > maxDecoded {
			return nil, fmt.Errorf("%w: it decompresses to more than %s, which no chart Helm "+
				"wrote does", errTooLarge, textutil.Bytes(maxDecoded))
		}
	}

	return raw, nil
}
