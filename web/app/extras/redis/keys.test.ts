import { describe, expect, it } from 'vitest';

import type { KeyRow } from './data';
import { asJson, bytes, folderPaths, isPartial, keyTree, typeWord, type TreeItem } from './keys';

const row = (name: string, type = 'string'): KeyRow => ({ name, type, ttl: 'no expiry' });

/** The tree as indented lines, so a test reads like the page. */
function draw(items: readonly TreeItem[], depth = 0): string[] {
  return items.flatMap((item) =>
    item.kind === 'key'
      ? [`${'  '.repeat(depth)}${item.label}`]
      : [`${'  '.repeat(depth)}${item.label} (${item.count})`, ...draw(item.children, depth + 1)],
  );
}

describe('the key tree', () => {
  it('folds keys by their `:` prefixes, folders first, in natural order', () => {
    const tree = keyTree([row('user:10:name'), row('user:2:name'), row('user:2:mail'), row('config'), row('user:count')]);
    expect(draw(tree)).toEqual([
      'user: (4)',
      '  2: (2)',
      '    mail',
      '    name',
      '  10: (1)',
      '    name',
      '  count',
      'config',
    ]);
  });

  it('keeps a key that is also a prefix beside its folder, and an empty segment as its own folder', () => {
    const tree = keyTree([row('a'), row('a:b'), row('a::c')]);
    expect(draw(tree)).toEqual(['a: (2)', '  : (1)', '    c', '  b', 'a']);
    // Every key is still reachable, by its own row: none hides another.
    const keys = (items: readonly TreeItem[]): string[] =>
      items.flatMap((i) => (i.kind === 'key' ? [i.row.name] : keys(i.children)));
    expect(keys(tree).sort()).toEqual(['a', 'a::c', 'a:b']);
  });

  it('keeps a key that is not text openable by its raw bytes', () => {
    const raw: KeyRow = { name: 'bin:�', raw: 'YmluOv8', type: 'hash', ttl: 'no expiry' };
    const tree = keyTree([raw]);
    const folder = tree[0];
    expect(folder?.kind).toBe('folder');
    const leaf = folder?.kind === 'folder' ? folder.children[0] : undefined;
    expect(leaf?.kind === 'key' ? leaf.row.raw : undefined).toBe('YmluOv8');
  });

  it('lists every folder for "expand all"', () => {
    expect(folderPaths(keyTree([row('a:b:c'), row('d:e')]))).toEqual(['a:', 'a:b:', 'd:']);
  });
});

describe('what a row says', () => {
  it('writes memory as a person reads it, and nothing when the server did not say', () => {
    expect(bytes(72)).toBe('72 B');
    expect(bytes(1536)).toBe('1.5 KiB');
    expect(bytes(20 * 1024 * 1024)).toBe('20 MiB');
    expect(bytes(undefined)).toBe('');
    expect(bytes(-1)).toBe('');
  });

  it('names a type short, and a module type as itself', () => {
    expect(typeWord('string')).toBe('str');
    expect(typeWord('zset')).toBe('zset');
    expect(typeWord('ReJSON-RL')).toBe('ReJSON-RL');
  });
});

describe('a string read as JSON', () => {
  it('pretty-prints an object or an array, and nothing else', () => {
    expect(asJson('{"a":1,"b":[true,null]}')).toBe('{\n  "a": 1,\n  "b": [\n    true,\n    null\n  ]\n}');
    expect(asJson(' [1,2]')).toBe('[\n  1,\n  2\n]');
    // Valid JSON that printing again would not change, and text that is not JSON.
    expect(asJson('42')).toBeUndefined();
    expect(asJson('"x"')).toBeUndefined();
    expect(asJson('hello')).toBeUndefined();
    // A value cut at 64 KiB does not parse, and is shown as the text it is.
    expect(asJson('{"a": [1, 2')).toBeUndefined();
  });

  it('calls a value partial exactly when the server said what it cut', () => {
    expect(isPartial({ note: '200 of 250 elements shown.' })).toBe(true);
    expect(isPartial({ note: '' })).toBe(false);
    expect(isPartial({})).toBe(false);
  });
});

describe('the review of da9da8f', () => {
  it('shows a JSON value as stored when formatting it would change it', () => {
    // Numbers a browser cannot hold, trailing zeros, a key written twice.
    expect(asJson('{"id": 12345678901234567890}')).toBeUndefined();
    expect(asJson('{"price": 1.50}')).toBeUndefined();
    expect(asJson('{"a": 1, "a": 2}')).toBeUndefined();
    // Whitespace between tokens is the only thing formatting may change...
    expect(asJson('{ "a" : [ 1 , 2 ] ,\n "b" : "x y" }')).toBe('{\n  "a": [\n    1,\n    2\n  ],\n  "b": "x y"\n}');
    // ...and whitespace inside a string is the value's own.
    expect(asJson('{"a":"two  spaces"}')).toBe('{\n  "a": "two  spaces"\n}');
    expect(asJson('{"q":"say \\"hi\\""}')).toBe('{\n  "q": "say \\"hi\\""\n}');
  });

  it('draws a key ending in the separator by its whole name, never as a row with no name', () => {
    const tree = keyTree([row('user:'), row('user:1'), row(':')]);
    const labels = (items: readonly TreeItem[]): string[] =>
      items.flatMap((i) => (i.kind === 'key' ? [i.label] : labels(i.children)));
    expect(labels(tree)).not.toContain('');
    expect(labels(tree)).toEqual(expect.arrayContaining(['user:', '1', ':']));
  });
});
