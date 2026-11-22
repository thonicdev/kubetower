package metrics

import (
	"math/bits"
	"slices"
)

// A window as the cache keeps it.
//
// A week of one container is two series of up to 673 points, and a Point is
// sixteen bytes: about 21 kB, and a cluster of 2 400 containers is 50 MB in
// one entry. Half of it is timestamps the window's own grid already knows -
// every point of a range query is a whole number of steps after the first -
// so a series is kept as its first time, one bit per step saying which steps
// hold a point, and the values, exactly as read. Unpacked, it is the same
// points, bit for bit: the figures computed from a kept window are the figures
// computed from a fresh one. A series whose points are not on the grid is not
// packed, and a window holding one is not kept.

// packedSeries is one series: the time of its first point, and where its
// steps (one bit per step from the first, set where a point is) and its values
// lie in the window's two arenas.
type packedSeries struct {
	first              float64
	stepsAt, stepsLen  int32
	valuesAt, valuesLn int32
}

// packedWindow is a whole window: the read's own fields, its series, and the
// two arenas every series' steps and values lie in - one allocation each, so
// what the window holds is what it counts.
type packedWindow struct {
	read        Window // CPU and Memory nil
	cpu, memory map[string]packedSeries
	steps       []uint64
	values      []float64
	bytes       int
}

// packedSeriesOverhead is what a kept series costs besides its key's bytes and
// its share of the arenas: a map slot with its key's string header and the
// series, rounded up for the map's own growth. TestAKeptWindowCostsWhatItIsCounted
// holds the count against the heap.
const packedSeriesOverhead = 112

// packedKeyOverhead is what a key in a kept listing costs besides its bytes:
// its string header, and its allocation rounded up.
const packedKeyOverhead = 32

// packWindow packs a window read whole, or says it cannot be.
func packWindow(w Window) (packedWindow, bool) {
	out := packedWindow{read: w, cpu: map[string]packedSeries{}, memory: map[string]packedSeries{}}
	out.read.CPU, out.read.Memory = nil, nil
	total := 0
	for _, m := range []map[string][]Point{w.CPU, w.Memory} {
		for _, points := range m {
			total += len(points)
		}
	}
	out.values = make([]float64, 0, total)
	for _, half := range []struct {
		from map[string][]Point
		into map[string]packedSeries
	}{{w.CPU, out.cpu}, {w.Memory, out.memory}} {
		for k, points := range half.from {
			p, ok := out.pack(points)
			if !ok {
				return packedWindow{}, false
			}
			half.into[k] = p
			out.bytes += packedSeriesOverhead + len(k)
		}
	}
	out.steps = slices.Clone(out.steps) // grown by appending: kept at its length
	out.bytes += 8 * (len(out.steps) + len(out.values))
	return out, true
}

// pack adds points spaced by whole steps to the arenas, or says they are not.
func (w *packedWindow) pack(points []Point) (packedSeries, bool) {
	step := WindowStep.Seconds()
	out := packedSeries{stepsAt: int32(len(w.steps)), valuesAt: int32(len(w.values))}
	if len(points) == 0 {
		return out, true
	}
	out.first = points[0].T
	last := (points[len(points)-1].T - out.first) / step
	if last < 0 || last > 1<<20 {
		return packedSeries{}, false
	}
	out.stepsLen, out.valuesLn = int32(int(last)/64+1), int32(len(points))
	w.steps = append(w.steps, make([]uint64, out.stepsLen)...)
	steps := w.steps[out.stepsAt:]
	previous := -1
	for _, p := range points {
		at := int((p.T - out.first) / step)
		// Exactly on the grid and after the previous point, or not packed:
		// unpacking must give back this very time.
		if at <= previous || at >= len(steps)*64 || out.first+float64(at)*step != p.T {
			return packedSeries{}, false
		}
		steps[at/64] |= 1 << (at % 64)
		w.values, previous = append(w.values, p.V), at
	}
	return out, true
}

// unpackSeries is one series' points again.
func (w packedWindow) unpackSeries(p packedSeries) []Point {
	step := WindowStep.Seconds()
	values := w.values[p.valuesAt : p.valuesAt+p.valuesLn]
	out := make([]Point, 0, len(values))
	for word, set := range w.steps[p.stepsAt : p.stepsAt+p.stepsLen] {
		for set != 0 {
			at := word*64 + bits.TrailingZeros64(set)
			out = append(out, Point{T: p.first + float64(at)*step, V: values[len(out)]})
			set &= set - 1
		}
	}
	return out
}

// unpack is the window again, in maps of its own: a caller may change what it
// is handed without changing what is kept.
func (w packedWindow) unpack() Window {
	out := w.read
	out.CPU, out.Memory = make(map[string][]Point, len(w.cpu)), make(map[string][]Point, len(w.memory))
	for k, s := range w.cpu {
		out.CPU[k] = w.unpackSeries(s)
	}
	for k, s := range w.memory {
		out.Memory[k] = w.unpackSeries(s)
	}
	return out
}

// listingBytes is what a kept listing of these keys costs.
func listingBytes(keys []string) int {
	n := 24 + 16*cap(keys)
	for _, k := range keys {
		n += packedKeyOverhead + len(k)
	}
	return n
}
