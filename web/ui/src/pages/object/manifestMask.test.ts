import { describe, expect, it } from 'vitest';

import {
  MASK,
  anyMasked,
  remask,
  revealInto,
  toBase64,
  withoutMaskNote,
  written,
} from './manifestMask';

/**
 * The two halves of a reveal, on the document `detail.Trimmed` renders: the
 * value goes in where the mask was, and comes back out as the mask if nobody
 * changed it. `internal/edit` diffs against the masked form, so a value left
 * in the document would put every key into the patch.
 */

const DOCUMENT = [
  '# The values below are masked.',
  'apiVersion: v1',
  'data:',
  '  name: <masked>',
  '  password: <masked>',
  '  "tls.crt": <masked>',
  'kind: Secret',
  'metadata:',
  '  name: db',
  '  namespace: shop',
  'type: Opaque',
  '',
].join('\n');

describe('revealInto', () => {
  it('writes the value, quoted, where the mask was and nowhere else', () => {
    const out = revealInto(DOCUMENT, 'password', 'aHVudGVyMg==');
    expect(out).toContain('  password: "aHVudGVyMg=="');
    expect(out).toContain('  name: <masked>');
    expect(out).toContain('  "tls.crt": <masked>');
  });

  it('stays inside the data block: a key called name is not metadata.name', () => {
    // A Secret can carry a key spelled like a metadata child at the same
    // indentation. Matching the first `  name: ` in the document would write a
    // credential over the object's name.
    const out = revealInto(DOCUMENT, 'name', 'YWxpY2U=');
    expect(out).toContain('  name: "YWxpY2U="');
    expect(out).toContain('  name: db');
    expect(out?.indexOf('  name: "YWxpY2U="')).toBeLessThan(out?.indexOf('  name: db') ?? -1);
  });

  it('keeps the quoting the renderer gave a key', () => {
    const out = revealInto(DOCUMENT, 'tls.crt', 'Y2VydA==');
    expect(out).toContain('  "tls.crt": "Y2VydA=="');
  });

  it('refuses to write over a mask the operator already typed on', () => {
    const typed = DOCUMENT.replace('  password: <masked>', '  password: bmV3');
    expect(revealInto(typed, 'password', 'aHVudGVyMg==')).toBeUndefined();
    expect(revealInto(DOCUMENT, 'no-such-key', 'eA==')).toBeUndefined();
  });

  it('writes an empty value as "" and never as nothing', () => {
    // `key: ` is YAML null, and null in a merge patch deletes the key.
    const out = revealInto(DOCUMENT, 'password', '');
    expect(out).toContain('  password: ""');
  });
});

describe('remask', () => {
  const revealed = { password: 'aHVudGVyMg==', name: 'YWxpY2U=' };
  const opened = revealInto(revealInto(DOCUMENT, 'password', 'aHVudGVyMg==') ?? '', 'name', 'YWxpY2U=') ?? '';

  it('gives the untouched document back exactly as it arrived', () => {
    expect(remask(opened, revealed)).toBe(DOCUMENT);
  });

  it('leaves a changed value as the operator typed it and masks the rest', () => {
    const edited = opened.replace('  password: "aHVudGVyMg=="', '  password: "bmV3LXBhc3N3b3Jk"');
    const sent = remask(edited, revealed);
    expect(sent).toContain('  password: "bmV3LXBhc3N3b3Jk"');
    expect(sent).toContain('  name: <masked>');
    expect(sent).not.toContain('YWxpY2U=');
  });

  it('does not charge the operator with a change for removing the quotes', () => {
    const edited = opened.replace('  password: "aHVudGVyMg=="', '  password: aHVudGVyMg==');
    expect(remask(edited, revealed)).toBe(DOCUMENT);
  });

  it('tells a value copied from another key apart from that key left alone', () => {
    // Key `name` now carries `password`'s value: that is a change to `name`,
    // and must reach the cluster. Comparing each line against *its own* revealed
    // value is what keeps it from being masked away as "unchanged".
    const edited = opened.replace('  name: "YWxpY2U="', '  name: "aHVudGVyMg=="');
    const sent = remask(edited, revealed);
    expect(sent).toContain('  name: "aHVudGVyMg=="');
    expect(sent).toContain('  password: <masked>');
  });

  it('leaves a document with no data block alone', () => {
    const pod = 'kind: Pod\nmetadata:\n  name: api\n';
    expect(remask(pod, revealed)).toBe(pod);
  });
});

describe('the encoding', () => {
  it('is byte for byte what the API server holds, outside Latin-1 included', () => {
    expect(toBase64('hunter2')).toBe('aHVudGVyMg==');
    expect(toBase64('')).toBe('');
    // A certificate subject with a character `btoa` alone would throw on.
    expect(toBase64('Zürich')).toBe('WsO8cmljaA==');
  });

  it('quotes what it writes, and the mask is not base64', () => {
    expect(written('MTIzNA==')).toBe('"MTIzNA=="');
    expect(MASK).toBe('<masked>');
  });
});

describe('the note that the values are masked', () => {
  const NOTE = [
    '# The values below are masked. Editing anything else here is safe: a masked',
    '# value is compared against the same mask, so it cannot be written back over the',
    '# real one. To change a value, type the new base64 over its mask.',
  ].join('\n');
  const BODY = 'apiVersion: v1\ndata:\n  a: <masked>\nkind: Secret\n';

  it('comes off the top of the document, and nothing else does', () => {
    expect(withoutMaskNote(`${NOTE}\n${BODY}`)).toBe(BODY);
    // A comment the person wrote, not the note, stays.
    expect(withoutMaskNote(`# mine\n${BODY}`)).toBe(`# mine\n${BODY}`);
    expect(withoutMaskNote(BODY)).toBe(BODY);
  });

  it('is untrue only once no value of the data block is the mask', () => {
    expect(anyMasked(BODY)).toBe(true);
    expect(anyMasked(BODY.replace('<masked>', '"Yg=="'))).toBe(false);
    // A mask outside the data block is not a value.
    expect(anyMasked('metadata:\n  name: <masked>\n')).toBe(false);
  });
});
