package metrics

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// samples is a range query's `values`: `[[unix, "value"], ...]`, read straight
// into points.
//
// A week of a large cluster is about eleven million samples, and reading each
// into a `[]any` and every value through a formatted scan cost most of the
// window's time and several gigabytes of garbage. The common shape - a number,
// then a quoted number - is scanned in place; anything else takes the general
// path, which keeps the old rules: an entry with fewer than two items or a
// timestamp that is not a number is left out. A NaN or an infinity is a gap on
// both paths, never a zero: a zero claims the container used nothing, and
// reductions are sized from that claim.
type samples []Point

func (s *samples) UnmarshalJSON(data []byte) error {
	if fast, ok := scanSamples(data); ok {
		*s = fast
		return nil
	}
	var general [][]any
	if err := json.Unmarshal(data, &general); err != nil {
		return err
	}
	out := make(samples, 0, len(general))
	for _, v := range general {
		if len(v) < 2 {
			continue
		}
		at, ok := v[0].(float64)
		if !ok {
			continue
		}
		value, ok := sampleValue(v)
		if !ok {
			continue
		}
		out = append(out, Point{T: at, V: value})
	}
	*s = out
	return nil
}

// scanSamples reads `[[1788294085.36,"0.5"],...]`, whitespace allowed between
// tokens, and says false at the first byte that is not that shape.
func scanSamples(b []byte) (samples, bool) {
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return nil, false
	}
	if len(bytes.TrimSpace(b[1:len(b)-1])) == 0 {
		return samples{}, true
	}
	out := make(samples, 0, bytes.Count(b, []byte("],["))+1)
	i := 1
	skip := func() {
		for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
			i++
		}
	}
	for {
		skip()
		if i >= len(b) || b[i] != '[' {
			return nil, false
		}
		i++
		skip()
		start := i
		for i < len(b) && b[i] != ',' && b[i] != ' ' {
			i++
		}
		at, err := strconv.ParseFloat(string(b[start:i]), 64)
		if err != nil {
			return nil, false
		}
		skip()
		if i >= len(b) || b[i] != ',' {
			return nil, false
		}
		i++
		skip()
		if i >= len(b) || b[i] != '"' {
			return nil, false
		}
		i++
		start = i
		for i < len(b) && b[i] != '"' && b[i] != '\\' {
			i++
		}
		if i >= len(b) || b[i] != '"' {
			return nil, false
		}
		text := b[start:i]
		i++
		skip()
		if i >= len(b) || b[i] != ']' {
			return nil, false
		}
		i++
		// An unreadable value is a gap, as on the general path.
		value, err := strconv.ParseFloat(string(bytes.TrimSpace(text)), 64)
		if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) {
			out = append(out, Point{T: at, V: value})
		}
		skip()
		if i == len(b)-1 {
			return out, true
		}
		if i >= len(b) || b[i] != ',' {
			return nil, false
		}
		i++
	}
}
