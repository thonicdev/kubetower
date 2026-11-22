import { describe, expect, it } from 'vitest';

import { indentJSON, jsonEnd } from './json';

/**
 * The JSON indenter, run on real lines: an almost-JSON line comes out
 * unchanged. The filter, the anchoring of the toggle, the download and the
 * toolbar's toggle are the log pane's, and tested there.
 */
describe('indentJSON', () => {
  // Replaces TestAnAlmostJSONLineIsLeftAlone, and asserts the property rather
  // than the shape of the source.
  //
  // "What must never happen is a parse error where a log line was. The operator
  // came for the line, and a pane that answers 'SyntaxError: Unexpected end of
  // JSON input' has destroyed the one thing it was asked to show -
  // irreversibly, because the line is not written down anywhere else in the
  // browser."
  it.each([
    ['a kubelet-truncated record', '{"level":"error","msg":"db refu'],
    ['a Go map printed with %v', 'map[a:1 b:2]'],
    ['a Java stack trace', 'at com.example.Thing.run(Thing.java:42) {inner}'],
    ['a half-written record', '2026-09-12 {"a":'],
    ['prose with a lone bracket', 'unexpected ] here'],
    ['nothing bracketed at all', 'plain old log line'],
    ['a value closed by the wrong bracket', '{"a":1]'],
  ])('leaves %s exactly as it arrived', (_what, line) => {
    expect(indentJSON(line)).toBeNull();
  });

  it('never returns anything built out of the parse failure', () => {
    // The whole class, stated once: whatever comes back is either null or a
    // string containing every character the container printed.
    for (const line of ['{"a":', 'map[x:1]', '[1,2', '{"a":1,}']) {
      const out = indentJSON(line);
      expect(out === null || out.includes(line.slice(0, 4))).toBe(true);
      expect(out ?? '').not.toMatch(/SyntaxError|Unexpected/);
    }
  });

  // Replaces TestWhatIsAroundTheJSONIsKept.
  //
  // A product decision rather than an implementation detail: a log line is
  // `<timestamp> <level> {…}` far more often than a bare record, and the
  // timestamp and the level are how an operator scans a pane.
  it('keeps what is in front of the record and what is behind it', () => {
    const out = indentJSON('08:15:03 ERROR {"msg":"db refused"} (retry 3)');
    expect(out).not.toBeNull();
    expect(out).toMatch(/^08:15:03 ERROR \{\n/);
    expect(out).toMatch(/\} \(retry 3\)$/);
    expect(out).toContain('"msg": "db refused"');
  });

  // Replaces TestTheJSONSpanIsScannedNotMatched, at the level of behavior: a
  // brace inside a string is not a brace, and no regular expression counts
  // nesting.
  it('finds the span by counting brackets, so a brace inside a string is not one', () => {
    const out = indentJSON('{"msg":"unexpected } here","n":1}');
    expect(out).not.toBeNull();
    expect(out).toContain('"msg": "unexpected } here"');
    expect(out).toContain('"n": 1');
  });

  it('is not fooled by an escaped quote inside a string', () => {
    const out = indentJSON('{"msg":"he said \\"} \\" and left","n":1}');
    expect(out).not.toBeNull();
    expect(out).toContain('"n": 1');
  });

  it('skips a candidate that does not parse and finds the record behind it', () => {
    // `[warn]` opens a bracket and is not JSON. The record is the second
    // candidate, and a scanner that gave up on the first would leave the line
    // dense.
    const out = indentJSON('[warn] {"a":1,"b":2}');
    expect(out).not.toBeNull();
    expect(out).toContain('"a": 1');
    expect(out?.startsWith('[warn] ')).toBe(true);
  });

  it('gives up after four candidates rather than walking a long line', () => {
    // Five decoys in front of the record. The fifth candidate is never tried, so
    // the line stays dense - which is the bound working, not a defect.
    const line = '[a] [b] [c] [d] [e] {"real":1}';
    expect(indentJSON(line)).toBeNull();
  });

  // Replaces TestTheIndentIsBounded.
  it('leaves a value alone rather than turning one line into four hundred rows', () => {
    const big: Record<string, number> = {};
    for (let i = 0; i < 500; i++) big[`k${i}`] = i;
    expect(indentJSON(JSON.stringify(big))).toBeNull();
  });

  it('does not mark an empty object as formatted', () => {
    // Already one row. Indenting it would change nothing except to say the line
    // was formatted, so the search moves on and the line is left alone.
    expect(indentJSON('{}')).toBeNull();
    expect(indentJSON('[]')).toBeNull();
  });

  it('is pure, which is what lets the panel cache it against a line index', () => {
    const line = '{"a":1,"b":[2,3]}';
    expect(indentJSON(line)).toBe(indentJSON(line));
  });
});

describe('jsonEnd', () => {
  it('refuses a value that never closes', () => {
    expect(jsonEnd('{"a":1', 0)).toBe(-1);
  });

  it('refuses a value closed by the wrong bracket', () => {
    expect(jsonEnd('{"a":1]', 0)).toBe(-1);
  });

  it('refuses a line that closes more than it opened', () => {
    expect(jsonEnd('}', 0)).toBe(-1);
  });

  it('returns one past the balanced value', () => {
    expect(jsonEnd('x {"a":{"b":1}} y', 2)).toBe(15);
  });
});
