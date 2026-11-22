package redis

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/valkey-io/valkey-go"

	"github.com/thonicdev/kubetower/textutil"
)

// Page is one round of SCAN.
type Page struct {
	Keys []KeySummary
	// Cursor is what to pass back for the next round. Zero means the scan has
	// come all the way round.
	Cursor uint64
	// Done is that same fact said plainly, because `Cursor == 0` is a Redis
	// convention and a page should not have to know it.
	Done bool
}

// KeyName is a key as the browser receives it.
//
// Keys are bytes, not text: a non-UTF-8 key would reach the browser with
// U+FFFD and could never be opened again. Name is the text to draw, and Raw,
// set only when Name cannot carry the bytes, is their base64 (URL alphabet,
// unpadded), which the key route accepts instead.
type KeyName struct {
	Name string `json:"name"`
	Raw  string `json:"raw,omitempty"`
}

func keyName(key string) KeyName {
	if utf8.ValidString(key) {
		return KeyName{Name: key}
	}
	return KeyName{Name: strings.ToValidUTF8(key, "�"), Raw: base64.RawURLEncoding.EncodeToString([]byte(key))}
}

// KeySummary is one row of the key list.
type KeySummary struct {
	KeyName
	Type string `json:"type"`
	// TTL is "no expiry" or a duration; a string because "no expiry" is not
	// a number.
	TTL string `json:"ttl"`
	// Memory is what `MEMORY USAGE` says the key costs, in bytes (sampled by
	// the server for a large collection). Absent, not zero, when the server
	// would not say.
	Memory *int64 `json:"memory,omitempty"`
}

// Scan reads one page of keys.
//
// SCAN, never KEYS, which blocks the server for a walk of the whole keyspace.
// A scan is a sample in progress: a round may return no keys with a non-zero
// cursor, and the keys seen so far are not the keyspace's size.
//
// match, when set, is a glob the server applies, so a page can come back empty
// while the scan goes on; hence Done.
func (c *Conn) Scan(ctx context.Context, cursor uint64, match string) (Page, error) {
	args := []string{"SCAN", strconv.FormatUint(cursor, 10)}
	if match = strings.TrimSpace(match); match != "" {
		args = append(args, "MATCH", match)
	}
	args = append(args, "COUNT", strconv.Itoa(maxScanPerRound))

	msg, err := c.do(ctx, args...)
	if err != nil {
		return Page{}, fmt.Errorf("SCAN failed: %w", err)
	}
	parts, err := msg.ToArray()
	if err != nil || len(parts) != 2 {
		return Page{}, fmt.Errorf("SCAN answered something other than a cursor and a list")
	}
	text, err := parts[0].ToString()
	if err != nil {
		return Page{}, fmt.Errorf("SCAN returned no cursor this console can read")
	}
	next, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return Page{}, fmt.Errorf("SCAN returned a cursor this console cannot read (%q)", textutil.Clip(text, 40))
	}
	names, err := parts[1].AsStrSlice()
	if err != nil {
		return Page{}, fmt.Errorf("SCAN returned keys this console cannot read")
	}

	page := Page{Cursor: next, Done: next == 0, Keys: make([]KeySummary, len(names))}
	c.describe(ctx, names, page.Keys)
	return page, nil
}

// How many keys of one SCAN round are described at once.
const describeWidth = 8

// describe fills one summary per key: its type, its expiry and its memory.
//
// Three reads per key, up to hundreds of keys a round, so describeWidth keys
// are read at once and the client pipelines them onto its one connection.
// Every read still goes through do() (readonly_test.go).
func (c *Conn) describe(ctx context.Context, names []string, into []KeySummary) {
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(describeWidth, len(names)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				c.describeOne(ctx, names[i], &into[i])
			}
		}()
	}
	for i := range names {
		next <- i
	}
	close(next)
	wg.Wait()
}

// describeOne reads one key's row. A panic in it (the client's, on an
// unexpected answer) is recovered as an "unreadable" row: net/http does not
// guard this goroutine.
func (c *Conn) describeOne(ctx context.Context, key string, row *KeySummary) {
	defer func() {
		if recover() != nil {
			*row = KeySummary{KeyName: keyName(key), Type: "unreadable", TTL: "—"}
		}
	}()
	*row = KeySummary{KeyName: keyName(key), Type: c.typeOf(ctx, key), TTL: c.ttlOf(ctx, key)}
	if row.Type != "gone" && row.Type != "unreadable" {
		if used, err := c.number(ctx, "MEMORY", "USAGE", key); err == nil {
			row.Memory = &used
		}
	}
}

// typeOf is one TYPE, with its failure rendered rather than returned.
//
// A key can be deleted between the SCAN and the TYPE; that row says "gone"
// rather than failing the page.
func (c *Conn) typeOf(ctx context.Context, key string) string {
	value, err := c.text(ctx, "TYPE", key)
	if err != nil {
		return "unreadable"
	}
	if value == "none" {
		return "gone"
	}
	return value
}

// ttlOf reads TTL and turns Redis's two sentinels into words.
//
// `-1` is "no expiry" and `-2` is "no such key".
func (c *Conn) ttlOf(ctx context.Context, key string) string {
	seconds, err := c.number(ctx, "TTL", key)
	if err != nil {
		return "—"
	}
	return ttlWords(seconds)
}

func ttlWords(seconds int64) string {
	switch {
	case seconds == -1:
		return "no expiry"
	case seconds < 0:
		return "gone"
	default:
		return shortSeconds(seconds)
	}
}

func shortSeconds(seconds int64) string {
	switch {
	case seconds < 60:
		return strconv.FormatInt(seconds, 10) + "s"
	case seconds < 3600:
		return strconv.FormatInt(seconds/60, 10) + "m"
	case seconds < 86400:
		return strconv.FormatInt(seconds/3600, 10) + "h"
	default:
		return strconv.FormatInt(seconds/86400, 10) + "d"
	}
}

// errNoSuchKey is a key that is not there when it is opened.
var errNoSuchKey = errors.New("there is no such key - it may have expired since the list was drawn")

// errWrongPage is a page token handed back for another kind of key than the
// one opened (the key was replaced under the same name). Refused, since a
// cursor read as an offset is a page from nowhere.
var errWrongPage = errors.New("the page asked for was handed out for another kind of key")

// KeyDetail is one key, opened - one page of it.
type KeyDetail struct {
	KeyName
	Type     string `json:"type"`
	TTL      string `json:"ttl"`
	Encoding string `json:"encoding,omitempty"`
	Memory   string `json:"memory,omitempty"`
	// Length is the number of elements, or the bytes of a string, as the
	// server counted them. Absent when it could not be read. It is the whole
	// key's, not the page's: the page says "N of M loaded" from it.
	Length *int64 `json:"length,omitempty"`
	// Entries is the value: one for a string, one per element of this page
	// otherwise.
	Entries []Entry `json:"entries"`
	// Next is the opaque token for the page after this one, sent back as the
	// key route's `page` parameter. Absent when this page was the last, which
	// is how the browser knows it holds the whole key.
	Next string `json:"next,omitempty"`
	// PageSize is how many elements this server reads a page at a time. A
	// scanned page (a hash, a set) may hold a few more.
	PageSize int `json:"pageSize"`
	// Note is what was not shown and why (a truncated string, a clipped
	// element, an unrendered type), never empty for a partial page. How much
	// of a paged collection is loaded is said by Length and Next.
	Note string `json:"note,omitempty"`
}

// Entry is one element of a value. Field is set for a hash, and for a stream
// is the entry's ID. Binary says the bytes were not text, so what is drawn is a
// rendering with U+FFFD where they could not be shown.
type Entry struct {
	Field string `json:"field,omitempty"`
	// Value is the element as text. For a stream entry it is a JSON object of
	// the entry's fields and values, in the order the server holds them.
	Value string `json:"value"`
	// Score is a sorted set member's score, as the server wrote it.
	Score  string `json:"score,omitempty"`
	Binary bool   `json:"binary,omitempty"`
	// Raw is set only when Binary is: the value's bytes, as cut to the
	// element cap, in standard base64, for a browser that inflates or draws
	// them in hex. Not set for a stream entry, whose value is many values.
	Raw string `json:"raw,omitempty"`
	// Clipped says the element - its value, a hash field's name, or one value
	// of a stream entry - was longer than maxElementBytes and only its start
	// is shown; Bytes is then the whole value's length, for an element with
	// one value.
	Clipped bool  `json:"clipped,omitempty"`
	Bytes   int64 `json:"bytes,omitempty"`
}

func entry(field, value string) Entry {
	out := Entry{Field: strings.ToValidUTF8(field, "�"), Value: value}
	if !utf8.ValidString(value) || !utf8.ValidString(field) {
		out.Value = strings.ToValidUTF8(value, "�")
		out.Binary = true
		out.Raw = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return out
}

// element is one element of a collection, its value and a hash field's name
// clipped to maxElementBytes, and marked when they were.
func element(field, value string) Entry {
	clippedField := len(field) > maxElementBytes
	whole := int64(len(value))
	clippedValue := whole > maxElementBytes
	e := entry(clipBytes(field, maxElementBytes), clipBytes(value, maxElementBytes))
	if clippedField || clippedValue {
		e.Clipped = true
	}
	if clippedValue {
		e.Bytes = whole
	}
	return e
}

// pageToken is where a page of a key starts, as the key route's `page`
// parameter carries it. The zero value is the first page.
//
// Its text is `h:<cursor>` (a hash), `s:<cursor>` (a set), `o:<offset>` (a
// list or a sorted set) or `x:<entry ID>` (a stream). The kind is in the token
// so a token for one type is refused on another.
type pageToken struct {
	kind   byte
	cursor uint64
	offset int64
	id     streamID
}

// The bounds on a page token: its length, and an offset far below where
// adding a page to it could overflow.
const (
	maxPageToken = 64
	maxOffset    = 1 << 53
)

func (p pageToken) String() string {
	switch p.kind {
	case 'h', 's':
		return string(p.kind) + ":" + strconv.FormatUint(p.cursor, 10)
	case 'o':
		return "o:" + strconv.FormatInt(p.offset, 10)
	case 'x':
		return "x:" + p.id.String()
	}
	return ""
}

// parsePage reads a page token; empty is the first page. A token is only
// ever one this server handed out, so anything else is refused, not mended.
// Neither a cursor nor an offset of 0 is ever handed out - 0 is the start - so
// neither is accepted.
func parsePage(text string) (pageToken, error) {
	if text == "" {
		return pageToken{}, nil
	}
	bad := errors.New("page must be the token the previous page of this key handed back")
	kind, rest, ok := strings.Cut(text, ":")
	if !ok || len(text) > maxPageToken || len(kind) != 1 {
		return pageToken{}, bad
	}
	p := pageToken{kind: kind[0]}
	switch p.kind {
	case 'h', 's':
		n, err := strconv.ParseUint(rest, 10, 64)
		if err != nil || n == 0 {
			return pageToken{}, bad
		}
		p.cursor = n
	case 'o':
		n, err := strconv.ParseUint(rest, 10, 64)
		if err != nil || n == 0 || n > maxOffset {
			return pageToken{}, bad
		}
		p.offset = int64(n)
	case 'x':
		id, ok := parseStreamID(rest)
		if !ok {
			return pageToken{}, bad
		}
		p.id = id
	default:
		return pageToken{}, bad
	}
	return p, nil
}

// pageKind is the token kind a key of this type hands out, or 0 for a type
// read in one piece.
func pageKind(kind string) byte {
	switch kind {
	case "hash":
		return 'h'
	case "set":
		return 's'
	case "list", "zset":
		return 'o'
	case "stream":
		return 'x'
	}
	return 0
}

// streamID is a stream entry's ID, `<milliseconds>-<sequence>`.
type streamID struct{ ms, seq uint64 }

func parseStreamID(text string) (streamID, bool) {
	ms, seq, ok := strings.Cut(text, "-")
	if !ok {
		return streamID{}, false
	}
	a, err := strconv.ParseUint(ms, 10, 64)
	if err != nil {
		return streamID{}, false
	}
	b, err := strconv.ParseUint(seq, 10, 64)
	if err != nil {
		return streamID{}, false
	}
	return streamID{a, b}, true
}

func (id streamID) String() string {
	return strconv.FormatUint(id.ms, 10) + "-" + strconv.FormatUint(id.seq, 10)
}

// after is the smallest ID greater than id, which is where the next page of a
// stream starts: XRANGE's start is inclusive, and its exclusive form needs a
// newer server than this console asks for. False for the last possible ID.
func (id streamID) after() (streamID, bool) {
	switch {
	case id.seq < math.MaxUint64:
		return streamID{id.ms, id.seq + 1}, true
	case id.ms < math.MaxUint64:
		return streamID{id.ms + 1, 0}, true
	}
	return streamID{}, false
}

// scanPage reads one page of a hash (HSCAN) or a set (SSCAN), from cursor,
// round by round until pageSize elements are held, the server says the scan
// is over, or maxPageRounds rounds have run. pairs is a hash's field-value
// shape. It answers the cursor to go on from: 0 when the scan is over.
//
// A scan's COUNT is a hint, not a ceiling, so a page may hold more than
// pageSize: a round's elements are all kept, since the cursor has moved past
// them.
func (c *Conn) scanPage(ctx context.Context, command, name string, cursor uint64, pairs bool) ([][2]string, uint64, error) {
	var out [][2]string
	for round := 0; round < maxPageRounds; round++ {
		msg, err := c.do(ctx, command, name, strconv.FormatUint(cursor, 10), "COUNT", strconv.Itoa(pageSize))
		if err != nil {
			return nil, 0, fmt.Errorf("%s failed: %w", command, err)
		}
		parts, err := msg.ToArray()
		if err != nil || len(parts) != 2 {
			return nil, 0, fmt.Errorf("%s answered something other than a cursor and a list", command)
		}
		text, err := parts[0].ToString()
		if err != nil {
			return nil, 0, fmt.Errorf("%s returned no cursor this console can read", command)
		}
		next, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("%s returned a cursor this console cannot read (%q)", command, textutil.Clip(text, 40))
		}
		items, err := parts[1].AsStrSlice()
		if err != nil {
			return nil, 0, fmt.Errorf("%s returned elements this console cannot read", command)
		}
		if pairs && len(items)%2 != 0 {
			return nil, 0, fmt.Errorf("%s returned an odd number of fields and values", command)
		}
		if pairs {
			for i := 0; i < len(items); i += 2 {
				out = append(out, [2]string{items[i], items[i+1]})
			}
		} else {
			for _, item := range items {
				out = append(out, [2]string{"", item})
			}
		}
		cursor = next
		if cursor == 0 || len(out) >= pageSize {
			break
		}
	}
	return out, cursor, nil
}

// streamPage reads one page of a stream with XRANGE ... COUNT, from the first
// entry or from the one the token names, and answers the next page's start
// when this page was full.
func (c *Conn) streamPage(ctx context.Context, name string, page pageToken) ([]Entry, string, error) {
	start := "-"
	if page.kind == 'x' {
		start = page.id.String()
	}
	msg, err := c.do(ctx, "XRANGE", name, start, "+", "COUNT", strconv.Itoa(pageSize))
	if err != nil {
		return nil, "", fmt.Errorf("XRANGE failed: %w", err)
	}
	items, err := msg.ToArray()
	if err != nil {
		return nil, "", fmt.Errorf("XRANGE answered something this console cannot read")
	}
	out := make([]Entry, 0, len(items))
	last := ""
	for _, item := range items {
		parts, err := item.ToArray()
		if err != nil || len(parts) != 2 {
			return nil, "", fmt.Errorf("XRANGE returned an entry that is not an ID and its fields")
		}
		id, err := parts[0].ToString()
		if err != nil {
			return nil, "", fmt.Errorf("XRANGE returned an entry with no ID this console can read")
		}
		var fields []string
		if !parts[1].IsNil() {
			if fields, err = parts[1].AsStrSlice(); err != nil || len(fields)%2 != 0 {
				return nil, "", fmt.Errorf("XRANGE returned fields and values this console cannot read")
			}
		}
		out = append(out, streamEntry(id, fields))
		last = id
	}
	if len(items) < pageSize {
		return out, "", nil
	}
	id, ok := parseStreamID(last)
	if !ok {
		return nil, "", fmt.Errorf("XRANGE returned an entry ID this console cannot read (%q)", textutil.Clip(last, 40))
	}
	next, ok := id.after()
	if !ok {
		return out, "", nil
	}
	return out, pageToken{kind: 'x', id: next}.String(), nil
}

// streamEntry is one stream entry: its ID as the field, and its fields and
// values as the text of one JSON object, in the server's order. A field named
// twice in one entry - which XADD allows - is drawn once, where it first
// appears, with the last value, which is what any JSON reader would keep.
// Every name and value is cut to maxElementBytes like any other element, and
// the entry is marked binary when one of them was not text; it carries no
// Raw, having no one value to carry.
func streamEntry(id string, fields []string) Entry {
	e := Entry{Field: strings.ToValidUTF8(id, "�")}
	var names []string
	values := map[string]string{}
	for i := 0; i+1 < len(fields); i += 2 {
		field, value := fields[i], fields[i+1]
		if len(field) > maxElementBytes || len(value) > maxElementBytes {
			e.Clipped = true
		}
		if !utf8.ValidString(field) || !utf8.ValidString(value) {
			e.Binary = true
		}
		if _, seen := values[field]; !seen {
			names = append(names, field)
		}
		values[field] = value
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonText(clipBytes(name, maxElementBytes)))
		b.WriteByte(':')
		b.WriteString(jsonText(clipBytes(values[name], maxElementBytes)))
	}
	b.WriteByte('}')
	e.Value = b.String()
	return e
}

// jsonText is s as a JSON string, with what is not UTF-8 as U+FFFD and
// without escaping <, > and &, which a JSON reader does not need escaped and
// an operator reading the text should see as they are.
func jsonText(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(strings.ToValidUTF8(s, "�"))
	return strings.TrimSuffix(b.String(), "\n")
}

// nextOffset is the token of the page after one read by offset from start, or
// "" when that page was the last: when the count says nothing is left, or -
// the count unreadable - when the page came back short.
func nextOffset(start, total int64, got int) string {
	end := start + pageSize
	if (total >= 0 && end < total) || (total < 0 && got >= pageSize) {
		return pageToken{kind: 'o', offset: end}.String()
	}
	return ""
}

// Key reads one page of a key's value, bounded: the first 64 KiB of a string,
// pageSize elements of anything else from where page says, the length and
// memory of the whole key. page is the zero token for the first page.
func (c *Conn) Key(ctx context.Context, name string, page pageToken) (KeyDetail, error) {
	kind, err := c.text(ctx, "TYPE", name)
	if err != nil {
		return KeyDetail{}, fmt.Errorf("TYPE failed: %w", err)
	}
	if kind == "none" {
		return KeyDetail{}, errNoSuchKey
	}
	if page.kind != 0 && page.kind != pageKind(kind) {
		return KeyDetail{}, errWrongPage
	}

	detail := KeyDetail{KeyName: keyName(name), Type: kind, TTL: c.ttlOf(ctx, name), Entries: []Entry{},
		PageSize: pageSize}
	if encoding, err := c.text(ctx, "OBJECT", "ENCODING", name); err == nil {
		detail.Encoding = encoding
	}
	if used, err := c.number(ctx, "MEMORY", "USAGE", name); err == nil {
		detail.Memory = textutil.Bytes(used)
	}
	length := func(args ...string) int64 {
		n, err := c.number(ctx, args...)
		if err != nil {
			return -1
		}
		detail.Length = &n
		return n
	}

	switch kind {
	case "string":
		// STRLEN first, and GET only under maxStringRead: GET has no range.
		size := length("STRLEN", name)
		switch {
		case size < 0:
			detail.Note = "How long this string is could not be read, so it was not read: a string may be " +
				"half a gigabyte, and this console reads one only when it knows the size first."
		case size > maxStringRead:
			detail.Note = fmt.Sprintf("This value is %s, more than the %s this console reads, so none of "+
				"it is shown.", textutil.Bytes(size), textutil.Bytes(maxStringRead))
		default:
			value, err := c.text(ctx, "GET", name)
			if err != nil {
				return detail, fmt.Errorf("GET failed: %w", err)
			}
			if len(value) > maxValueBytes {
				// Cut on bytes, because what comes back may not be text at
				// all, and said so - including that the whole of it was read.
				detail.Note = fmt.Sprintf("This value is %s; the first %s are shown. The whole value was "+
					"read to show them.", textutil.Bytes(int64(len(value))), textutil.Bytes(maxValueBytes))
				value = clipBytes(value, maxValueBytes)
			}
			detail.Entries = []Entry{entry("", value)}
		}
		return detail, nil

	case "list":
		total := length("LLEN", name)
		start := page.offset
		items, err := c.strings(ctx, "LRANGE", name, strconv.FormatInt(start, 10),
			strconv.FormatInt(start+pageSize-1, 10))
		if err != nil {
			return detail, fmt.Errorf("LRANGE failed: %w", err)
		}
		detail.Entries = valueEntries(items)
		detail.Next = nextOffset(start, total, len(items))

	case "hash", "set":
		// Scanned a page at a time (scanPage). Each page is sorted so an
		// unchanged key draws the same way twice; the whole key is not.
		count, command := "HLEN", "HSCAN"
		if kind == "set" {
			count, command = "SCARD", "SSCAN"
		}
		length(count, name)
		pairs, cursor, err := c.scanPage(ctx, command, name, page.cursor, kind == "hash")
		if err != nil {
			return detail, err
		}
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i][0] != pairs[j][0] {
				return pairs[i][0] < pairs[j][0]
			}
			return pairs[i][1] < pairs[j][1]
		})
		for _, pair := range pairs {
			detail.Entries = append(detail.Entries, element(pair[0], pair[1]))
		}
		if cursor != 0 {
			detail.Next = pageToken{kind: pageKind(kind), cursor: cursor}.String()
		}

	case "zset":
		// WITHSCORES is an argument of ZRANGE, which is on the allowlist: the
		// same read, and a sorted set without its scores is half of it.
		total := length("ZCARD", name)
		start := page.offset
		msg, err := c.do(ctx, "ZRANGE", name, strconv.FormatInt(start, 10),
			strconv.FormatInt(start+pageSize-1, 10), "WITHSCORES")
		if err != nil {
			return detail, fmt.Errorf("ZRANGE failed: %w", err)
		}
		pairs, err := scoredPairs(msg)
		if err != nil {
			return detail, fmt.Errorf("ZRANGE answered something this console cannot read")
		}
		for _, pair := range pairs {
			e := element("", pair[0])
			e.Score = pair[1]
			detail.Entries = append(detail.Entries, e)
		}
		detail.Next = nextOffset(start, total, len(pairs))

	case "stream":
		length("XLEN", name)
		entries, next, err := c.streamPage(ctx, name, page)
		if err != nil {
			return detail, err
		}
		detail.Entries, detail.Next = entries, next

	default:
		// A module type (RedisJSON, RediSearch, ...): named, not drawn as
		// empty.
		detail.Note = fmt.Sprintf("This key is a %q, which this console does not know how to "+
			"read. Its size and expiry above are still its own.", kind)
		return detail, nil
	}
	// An element cut short is said whatever else the page is: it is partial
	// even in a collection loaded whole.
	detail.Note = strings.TrimSpace(clippedNote(detail.Entries))
	return detail, nil
}

// clipBytes cuts s to at most max bytes, and - when s is text - not inside a
// character, so a cut value does not turn into a binary one at its last byte.
func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	if utf8.ValidString(s) {
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}
	return s[:cut]
}

func (c *Conn) strings(ctx context.Context, args ...string) ([]string, error) {
	msg, err := c.do(ctx, args...)
	if err != nil {
		return nil, err
	}
	return msg.AsStrSlice()
}

// scoredPairs reads ZRANGE ... WITHSCORES: under RESP3 an array of
// [member, score] pairs with the score a double, under RESP2 a flat member,
// score, member, score array of strings. The score is kept as the text the
// server wrote, so nothing is lost to a float's rounding on the way.
func scoredPairs(msg valkey.ValkeyMessage) ([][2]string, error) {
	items, err := msg.ToArray()
	if err != nil {
		return nil, err
	}
	var out [][2]string
	if len(items) > 0 && items[0].IsArray() {
		for _, item := range items {
			pair, err := item.ToArray()
			if err != nil || len(pair) != 2 {
				return nil, errors.New("a scored member that is not a pair")
			}
			member, err := pair[0].ToString()
			if err != nil {
				return nil, err
			}
			out = append(out, [2]string{member, scoreText(pair[1])})
		}
		return out, nil
	}
	if len(items)%2 != 0 {
		return nil, errors.New("an odd number of member and score strings")
	}
	for i := 0; i < len(items); i += 2 {
		member, err := items[i].ToString()
		if err != nil {
			return nil, err
		}
		out = append(out, [2]string{member, scoreText(items[i+1])})
	}
	return out, nil
}

func scoreText(m valkey.ValkeyMessage) string {
	if text, err := m.ToString(); err == nil {
		return text
	}
	if f, err := m.AsFloat64(); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return "?"
}

func valueEntries(items []string) []Entry {
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		out = append(out, element("", item))
	}
	return out
}

// clippedNote is the sentence for elements cut to maxElementBytes, with a
// leading space, or "" when none was.
func clippedNote(entries []Entry) string {
	n := 0
	for _, e := range entries {
		if e.Clipped {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(" One element is longer than %s and only its start is shown.", textutil.Bytes(maxElementBytes))
	}
	return fmt.Sprintf(" %d elements are longer than %s and only their start is shown.", n, textutil.Bytes(maxElementBytes))
}
