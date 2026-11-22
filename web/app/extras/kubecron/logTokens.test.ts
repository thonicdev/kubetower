import { describe, expect, it } from 'vitest';

import { stripAnsi, tokenize } from './logTokens';

/** The code viewer's tokenizer. */

const kinds = (text: string) => tokenize(text).tokens.filter((t) => t.kind !== 'plain').map((t) => [t.kind, t.text]);

describe('a log line, cut', () => {
  it('gives the line back character for character', () => {
    for (const line of [
      '2026-10-20T09:00:01.123Z ERROR db: connection refused (retry=3)',
      'level=warn msg="slow query" ms=1200',
      'prefix {"level":"info","msg":"ok","n":[1,2.5e3,true,null]} suffix',
      'map[a:1 b:{2}] is not JSON',
      'I1023 12:00:00.000000    1 main.go:42] started',
    ]) {
      expect(tokenize(line).tokens.map((t) => t.text).join('')).toBe(line);
    }
  });

  it('marks timestamps and a capitalized level, and takes the level for the line', () => {
    const line = tokenize('2026-10-20T09:00:01Z ERROR connection refused at 09:00:02');
    expect(line.level).toBe('error');
    expect(kinds('2026-10-20T09:00:01Z ERROR connection refused at 09:00:02')).toEqual([
      ['time', '2026-10-20T09:00:01Z'],
      ['error', 'ERROR'],
      ['time', '09:00:02'],
    ]);
  });

  it('does not take a lower-case word in prose for a level', () => {
    expect(tokenize('see the docs for more info').level).toBeUndefined();
  });

  it("reads logfmt's keys, and its level from level=", () => {
    expect(tokenize('level=warn msg="slow query" ms=1200').level).toBe('warn');
    expect(kinds('level=warn msg="slow query"')).toEqual([
      ['key', 'level'],
      ['punct', '='],
      ['warn', 'warn'],
      ['key', 'msg'],
      ['punct', '='],
      ['string', '"slow query"'],
    ]);
  });

  it('colors a JSON record by its parts and takes its level field', () => {
    const line = tokenize('{"severity":"ERROR","n":2,"ok":false}');
    expect(line.level).toBe('error');
    expect(kinds('{"severity":"ERROR","n":2,"ok":false}')).toEqual([
      ['punct', '{'],
      ['key', '"severity"'],
      ['punct', ':'],
      ['string', '"ERROR"'],
      ['punct', ','],
      ['key', '"n"'],
      ['punct', ':'],
      ['number', '2'],
      ['punct', ','],
      ['key', '"ok"'],
      ['punct', ':'],
      ['literal', 'false'],
      ['punct', '}'],
    ]);
  });

  it('leaves braces that are not JSON as prose', () => {
    expect(kinds('map[a:1 b:{2}] is not JSON').some(([k]) => k === 'key' || k === 'punct')).toBe(false);
  });

  it("takes klog's letter as the level, over the message's words", () => {
    const line = tokenize('W1023 12:00:00.000000    1 main.go:42] ERROR-free warning');
    expect(line.level).toBe('warn');
    expect(line.tokens[0]).toEqual({ text: 'W', kind: 'warn' });
  });

  it('drops ANSI color codes, which the viewer colors itself', () => {
    expect(stripAnsi('\x1b[31mred\x1b[0m plain')).toBe('red plain');
    expect(tokenize('\x1b[1;33mWARN\x1b[0m disk').level).toBe('warn');
  });
});
