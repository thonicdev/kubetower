package logs

// Paging backwards through a container's log: "give me what came before the
// oldest line the panel holds".
//
// The Kubernetes log API does not paginate: PodLogOptions has no offset or
// continue token, TailLines counts from the end, SinceTime and SinceSeconds only
// move the start forward. Two strategies remain:
//
//   - grow the tail: ask for the last `held + page` lines and keep the front of
//     that window. Correct because a container log is append-only, so the last
//     N+P lines always end with the last N.
//   - walk back with SinceTime: needs every line to carry a usable instant, which
//     a multi-line record (a stack trace, pretty-printed JSON) breaks.
//
// This takes the first: it costs more but cannot be wrong about where it is.
//
// The window is re-read each time, so the work is quadratic in the number of
// gestures while what reaches the browser is linear: with a 200-line opening
// tail and 500-line pages, ten gestures read about 27 700 lines to deliver
// 4 500. The API server and the node pay that, which is why maxRewind exists.
//
// `before` comes from the browser and grows a page per gesture, so the ceiling
// is enforced per request with no server-side state, and binds every client.
// Understating `before` only re-sends lines the caller has; overstating it is
// clamped by maxRewind, so a client cannot enlarge the read.
//
// It must never cross into the previous run: `previous=1` is a different
// container instance, and stitching the two would invent a seamless history. The
// flag is carried through untouched, reaching the oldest line of this run is
// announced, and the panel offers the previous run as a separate gesture.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/textutil"
)

const (
	// earlierPage is how many lines one gesture fetches when the caller names no
	// figure: more than defaultTail, which every drawer pays, since this is paid
	// only on request.
	earlierPage = 500
	// maxEarlierPage clamps one request: twice the panel's LOG_CHUNK, and well
	// under maxRewind so no single read asks for the whole ceiling.
	maxEarlierPage = 2000
	// maxRewind is the furthest back any request looks, and so, since `before`
	// is what the caller holds, the furthest back a paging session gets. Twice
	// maxTail; past it a browser is the wrong tool, and the sentence says so.
	maxRewind = 10000
	// maxPageBytes bounds what is held and sent for one gesture, the same figure
	// as the per-stream ceiling.
	maxPageBytes = 8 << 20 // 8 MiB
	// maxEarlierBytes bounds the scan: the window is read through to learn its
	// length, and only maxPageBytes of it is kept. 64 MiB over at most 10 000 lines
	// is 6.5 KiB a line; a log that dense is not one to page through.
	maxEarlierBytes = 64 << 20 // 64 MiB
)

// The three ways paging stops, each with its own sentence: "there is no more"
// and "I will not fetch more" are different answers.
const (
	// stopBeginning: the window came back short, so the read reached the oldest
	// line the kubelet still holds for this container.
	stopBeginning = "beginning"
	// stopCeiling: maxRewind. There are more lines and this will not fetch them.
	stopCeiling = "ceiling"
	// stopDense: the boundary was not reached within maxEarlierBytes, or the
	// page's lines exceeded maxPageBytes first. The block adjacent to what the
	// caller holds cannot be delivered whole, and part of it would prepend lines
	// adjacent to nothing.
	stopDense = "dense"
)

// Earlier is one page backwards: the lines immediately preceding what the
// caller had, oldest first, and whether another page can follow.
//
// Stop is empty while there may be more. Note is the sentence to show when
// there will not be, written here so every client gets the same wording.
type Earlier struct {
	Lines []string `json:"lines"`
	Stop  string   `json:"stop,omitempty"`
	Note  string   `json:"note,omitempty"`
}

func beginningNote() string {
	// Never "the container started here": the kubelet rotates logs, so the
	// oldest line it returns is not necessarily the first the process wrote.
	return "this is as far back as the kubelet has kept — earlier lines, if there were any, have been rotated away"
}

func ceilingNote() string {
	return fmt.Sprintf("paging back stops at %d lines — open a terminal in the dock and read the rest with kubectl", maxRewind)
}

func denseNote() string {
	return fmt.Sprintf("read %d MiB without reaching the line before this one — these lines are too long to page through; open a terminal in the dock", maxEarlierBytes>>20)
}

// earlierOptions builds the request for one page backwards. Not logOptions:
// that one clamps to maxTail, the opening cost of every panel, while this
// answers how far a deliberate gesture may reach.
//
// Follow is always false: the block is already written. Previous is carried
// through untouched; see the top of this file.
func earlierOptions(req Request, window int64) *corev1.PodLogOptions {
	if window > maxRewind {
		window = maxRewind
	}
	if window < 1 {
		window = 1
	}
	return &corev1.PodLogOptions{
		Container:  req.Container,
		Follow:     false,
		Previous:   req.Previous,
		TailLines:  &window,
		Timestamps: req.Timestamps,
	}
}

// Earlier reads the block of lines immediately before the `before` lines the
// caller already holds. It returns an error only when the question could not be
// asked (unknown cluster, pod gone, forbidden); "nothing earlier" is an answer,
// in Stop.
func (s *Streamer) Earlier(ctx context.Context, req Request, before, page int64) (Earlier, error) {
	if page <= 0 {
		page = earlierPage
	}
	if page > maxEarlierPage {
		page = maxEarlierPage
	}
	if before < 1 {
		before = 1
	}

	// The ceiling, checked before anything is opened against a cluster so a
	// refusal costs the API server nothing.
	if before >= maxRewind {
		return Earlier{Stop: stopCeiling, Note: ceilingNote()}, nil
	}
	ceiling := false
	if before+page > maxRewind {
		page = maxRewind - before
		ceiling = true
	}
	window := before + page

	req, err := s.address(ctx, req)
	if err != nil {
		return Earlier{}, err
	}

	// The bounded client: a fixed read of a finished block keeps the per-request
	// timeout.
	client, err := s.clusters.Client(req.Cluster)
	if err != nil {
		return Earlier{}, err
	}
	var stream io.ReadCloser
	_, err = s.gate.Read(ctx, req.Cluster, logQuestion(req), func(ctx context.Context) error {
		var err error
		stream, err = client.Clients.CoreV1().Pods(req.Namespace).
			GetLogs(req.Pod, earlierOptions(req, window)).Stream(ctx)
		return err
	})
	if err != nil {
		return Earlier{}, err
	}
	defer func() { _ = stream.Close() }()

	scanner := bufio.NewScanner(stream)
	// One log line can be a serialized stack trace; the default 64 KiB token
	// limit would fail it with "token too long".
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	// `total` is how long the window turned out to be, the only way to know
	// whether the tail reached the start of the file; `kept` is its front, the
	// answer. The rest is read and discarded, which is what makes the boundary known
	// rather than guessed.
	kept := make([]string, 0, min(page, 1024))
	var total, keptBytes, read int64
	dense := false

	for scanner.Scan() {
		line := strings.ReplaceAll(scanner.Text(), "\r", "")
		if int64(len(kept)) < page && keptBytes < maxPageBytes {
			kept = append(kept, line)
			keptBytes += int64(len(line)) + 1
		}
		total++
		read += int64(len(line)) + 1
		if total >= window {
			break // the window is full; there is nothing left to learn from it
		}
		// After the window check, so a full window is never reported as dense.
		if read >= maxEarlierBytes {
			dense = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return Earlier{}, err
	}

	switch {
	case total >= window:
		// A full window has its boundary exactly at `page`. Fewer kept lines mean
		// maxPageBytes bit first: those lines are the oldest of the block, not adjacent
		// to what the caller has.
		if int64(len(kept)) < page {
			return Earlier{Stop: stopDense, Note: denseNote()}, nil
		}
		if ceiling {
			return Earlier{Lines: kept, Stop: stopCeiling, Note: ceilingNote()}, nil
		}
		// A window exactly the file's length reports no stop; the next gesture finds
		// the beginning. The API gives no way to tell "exactly full" from "full".
		return Earlier{Lines: kept}, nil

	case dense:
		return Earlier{Stop: stopDense, Note: denseNote()}, nil

	default:
		// Short window: the tail began at the oldest line the kubelet holds. The
		// boundary is total-before, which is zero or negative when lines were rotated
		// away since the caller's read: everything still visible, the caller has.
		n := total - before
		if n < 0 {
			n = 0
		}
		if n > int64(len(kept)) {
			n = int64(len(kept))
		}
		return Earlier{Lines: kept[:n], Stop: stopBeginning, Note: beginningNote()}, nil
	}
}

// earlier answers a page backwards as JSON, on a route whose other answer is
// a stream: an EventSource cannot read a failed handshake's status, but a fetch
// can, and tell a refusal from a dead cluster. The status says whether the
// question could be asked, the body what the answer was.
func (s *Streamer) earlier(w http.ResponseWriter, r *http.Request, before, page int64) {
	req := requestFrom(r.URL.Query())

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	answer, err := s.Earlier(ctx, req, before, page)
	if err != nil {
		http.Error(w, textutil.FirstLineN(err.Error(), errorLine), statusFor(err))
		return
	}
	if answer.Lines == nil {
		// An empty array rather than null: the panel reads `data.lines.length`.
		answer.Lines = []string{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(answer); err != nil {
		s.logf("writing earlier lines for %s/%s: %v", req.Namespace, req.Pod, err)
	}
}

// earlierFrom reads the two numbers off the query, refusing ones that make no
// sense. `before` absent means an ordinary stream request, with no extra cost.
func earlierFrom(q url.Values) (before, page int64, paging bool, err error) {
	raw := q.Get("before")
	if raw == "" {
		return 0, 0, false, nil
	}
	before, err = strconv.ParseInt(raw, 10, 64)
	if err != nil || before < 1 {
		return 0, 0, true, fmt.Errorf(
			"before must be how many lines you already hold, as a whole number greater than zero; got %q", raw)
	}
	if raw := q.Get("page"); raw != "" {
		page, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || page < 1 {
			return 0, 0, true, fmt.Errorf(
				"page must be how many earlier lines to fetch, as a whole number greater than zero; got %q", raw)
		}
	}
	return before, page, true, nil
}
