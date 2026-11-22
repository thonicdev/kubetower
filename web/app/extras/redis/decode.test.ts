import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';

import { decode, decodeNow, decodedMark, hexRows, preview, readableText, strictBase64 } from './decode';

const b64 = (bytes: Uint8Array | string) => Buffer.from(bytes).toString('base64');

/**
 * The decoding a value goes through before it is drawn: JSON indented,
 * base64 and gzip undone when what comes out reads, and the rest drawn as a
 * size and a hex view rather than as noise.
 */
describe('decoding a value before it is drawn', () => {
  it('indents JSON and says so', async () => {
    const d = await decode('{"user":"ada","n":1}');
    expect(d).toMatchObject({ kind: 'text', json: true, via: [] });
    expect(d.kind === 'text' ? d.text : '').toBe('{\n  "user": "ada",\n  "n": 1\n}');
    expect(decodedMark(d)).toBe('JSON');
  });

  it('decodes base64 that holds JSON', async () => {
    const d = await decode(b64('{"a":[1,2]}'));
    expect(d).toMatchObject({ kind: 'text', json: true, via: ['base64'] });
    expect(decodedMark(d)).toBe('base64 → JSON');
  });

  it('decodes base64 that holds text', async () => {
    const d = await decode(b64('hello, this is a sentence'));
    expect(d).toEqual({ kind: 'text', text: 'hello, this is a sentence', json: false, via: ['base64'] });
    expect(decodedMark(d)).toBe('base64 → text');
  });

  it('leaves a word that happens to be valid base64 as the word it is', async () => {
    // Short words, and longer ones whose bytes read as nothing, stay text.
    for (const word of ['test', 'abcd1234', 'deadbeefcafebabe', 'abcdefghijkl']) {
      const d = await decode(word);
      expect(d, word).toEqual({ kind: 'text', text: word, json: false, via: [] });
      expect(decodedMark(d)).toBeUndefined();
    }
    expect(strictBase64('aGVsbG8gd29ybGQ')).toBeUndefined(); // unpadded
    expect(strictBase64('aGVsbG8gd29y bGQ=')).toBeUndefined();
  });

  it('inflates a gzip sent as bytes, and one sent as base64 text', async () => {
    const zipped = gzipSync(Buffer.from('{"big":true}'));
    const fromRaw = await decode('��', b64(zipped), true);
    expect(fromRaw).toMatchObject({ kind: 'text', json: true, via: ['gzip'] });
    expect(decodedMark(fromRaw)).toBe('gzip → JSON');

    const fromText = await decode(b64(gzipSync(Buffer.from('plain words'))));
    expect(fromText).toEqual({ kind: 'text', text: 'plain words', json: false, via: ['base64', 'gzip'] });
    expect(decodedMark(fromText)).toBe('base64 → gzip → text');
  });

  it('keeps the start of a gzip that was cut, and says it was', async () => {
    const zipped = gzipSync(Buffer.from('x'.repeat(5000) + 'y'.repeat(5000)));
    const d = await decode('', b64(zipped.subarray(0, zipped.length - 10)), true);
    expect(d.kind).toBe('text');
    expect(d.cut).toBe(true);
    expect(decodedMark(d)).toBe('gzip → text (cut)');
  });

  it('calls bytes that yield no readable text binary, with every byte kept for the size', async () => {
    const bytes = new Uint8Array(2355).map((_, i) => (i * 37 + 0x80) % 256);
    const d = await decode('�', b64(bytes), true);
    expect(d.kind).toBe('binary');
    expect(d.kind === 'binary' ? d.bytes.length : 0).toBe(2355);
    // A rendering with no bytes behind it is drawn as it came, marked.
    expect(decodeNow('a�b', undefined, true)).toEqual({ kind: 'text', text: 'a�b', json: false, via: [], lossy: true });
  });

  it('reads as text only what decodes as UTF-8 with no NUL and almost no control character', () => {
    expect(readableText(new TextEncoder().encode('line one\nline two\tok'))).toBe('line one\nline two\tok');
    expect(readableText(new Uint8Array([0x61, 0x00, 0x62]))).toBeUndefined();
    expect(readableText(new Uint8Array([0xff, 0xfe]))).toBeUndefined();
    expect(readableText(new Uint8Array([0x01, 0x02, 0x61]))).toBeUndefined();
  });

  it('draws hex as offset, sixteen bytes and their ASCII, bounded', () => {
    const bytes = new TextEncoder().encode('ABCDEFGHIJKLMNOPqr\u0001');
    const rows = hexRows(bytes);
    expect(rows).toEqual([
      { offset: '00000000', hex: '41 42 43 44 45 46 47 48  49 4a 4b 4c 4d 4e 4f 50', ascii: 'ABCDEFGHIJKLMNOP' },
      { offset: '00000010', hex: '71 72 01', ascii: 'qr.' },
    ]);
    expect(hexRows(new Uint8Array(10000))).toHaveLength(256);
    expect(hexRows(new Uint8Array(100), 32)).toHaveLength(2);
  });

  it('cuts a long value at about two lines or 200 characters, and leaves a short one whole', () => {
    expect(preview('short')).toBeUndefined();
    expect(preview('a'.repeat(500))).toBe('a'.repeat(200));
    expect(preview('one\ntwo\nthree')).toBe('one\ntwo');
    expect(preview('one\ntwo')).toBeUndefined();
  });
});
