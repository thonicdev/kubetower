import { afterEach, describe, expect, it, vi } from 'vitest';

import type { InstanceAnswer, KeyDetail, Scan } from './data';
import { csvCell, download, fileName, keysCsv, keysJson, scanIsWhole, scanPartialNote, valueJson, valueRaw, type Origin } from './export';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/**
 * The quick exports: built in the browser from the
 * bounded data the page holds, partial said in the file as on the button, and
 * never a credential.
 */

const origin: Origin = { cluster: 'docker-desktop', instance: { namespace: 'data', service: 'redis-master', port: 6379 }, db: 0 };

const scan = (over: Partial<Scan> = {}): Scan => ({
  match: '',
  cursor: '0',
  next: '96',
  done: false,
  keys: [
    { name: 'session:1', type: 'string', ttl: '1m', memory: 72 },
    { name: 'bin:�', raw: 'YmluOv8', type: 'hash', ttl: 'no expiry' },
  ],
  ...over,
});

describe('the key list export', () => {
  it('is whole only for one round that went all the way round from the start', () => {
    expect(scanIsWhole({ done: true, cursor: '0' })).toBe(true);
    expect(scanIsWhole({ done: true, cursor: '' })).toBe(true);
    expect(scanIsWhole({ done: false, cursor: '0' })).toBe(false);
    // The last round of several: done, and still not every key.
    expect(scanIsWhole({ done: true, cursor: '96' })).toBe(false);
    expect(scanPartialNote({ done: true, cursor: '96', match: '' })).toContain('earlier rounds');
    expect(scanPartialNote({ done: false, cursor: '0', match: '' })).toContain('not finished');
  });

  it('writes CSV a spreadsheet reads back, quoting what needs it and never running a key as a formula', () => {
    expect(csvCell('plain')).toBe('plain');
    expect(csvCell('a,b')).toBe('"a,b"');
    expect(csvCell('say "hi"')).toBe('"say ""hi"""');
    expect(csvCell('two\nlines')).toBe('"two\nlines"');
    expect(csvCell('=HYPERLINK("x")')).toBe(`"'=HYPERLINK(""x"")"`);
    expect(csvCell('-1')).toBe("'-1");
    expect(csvCell('@sum')).toBe("'@sum");

    expect(keysCsv(scan().keys)).toBe(
      'key,type,ttl,memory_bytes,raw_base64url\r\nsession:1,string,1m,72,\r\nbin:�,hash,no expiry,,YmluOv8\r\n',
    );
  });

  it('writes JSON that says where the keys came from and that they are a sample', () => {
    const body = JSON.parse(keysJson(origin, scan())) as Record<string, unknown>;
    expect(body).toMatchObject({
      cluster: 'docker-desktop',
      instance: { namespace: 'data', service: 'redis-master', port: 6379 },
      db: 0,
      cursor: '0',
      next: '96',
      partial: true,
    });
    expect(body['note']).toContain('not finished');
    expect(body['keys']).toEqual([
      { name: 'session:1', type: 'string', ttl: '1m', memoryBytes: 72 },
      { name: 'bin:�', raw: 'YmluOv8', type: 'hash', ttl: 'no expiry' },
    ]);
    const whole = JSON.parse(keysJson(origin, { match: '', cursor: '0', done: true, keys: scan().keys })) as Record<string, unknown>;
    expect(whole['partial']).toBe(false);
    expect(whole['note']).toBeUndefined();
  });
});

const detail = (over: Partial<KeyDetail>): KeyDetail => ({ name: 'k', type: 'string', ttl: 'no expiry', entries: [], ...over });

describe('the value export', () => {
  it('writes each type the way it reads: a string, a hash as an object, a sorted set with its scores', () => {
    const value = (d: KeyDetail) => (JSON.parse(valueJson(origin, d)) as { value: unknown }).value;
    expect(value(detail({ entries: [{ value: '{"a":1}' }] }))).toBe('{"a":1}');
    expect(value(detail({ type: 'hash', entries: [{ field: 'name', value: 'ada' }, { field: 'lang', value: 'en' }] }))).toEqual({
      name: 'ada',
      lang: 'en',
    });
    expect(value(detail({ type: 'zset', entries: [{ value: 'p1', score: '1.5' }] }))).toEqual([{ member: 'p1', score: '1.5' }]);
    expect(value(detail({ type: 'list', entries: [{ value: 'a' }, { value: 'b' }] }))).toEqual(['a', 'b']);

    expect(valueRaw(detail({ entries: [{ value: 'hello' }] }))).toBe('hello');
    expect(valueRaw(detail({ type: 'hash', entries: [{ field: 'name', value: 'ada' }] }))).toBe('name\tada\n');
    expect(valueRaw(detail({ type: 'zset', entries: [{ value: 'p1', score: '2' }] }))).toBe('p1\t2\n');
    expect(valueRaw(detail({ type: 'stream', entries: [] }))).toBe('');
  });

  it('says in the file what the server cut, and what was not text', () => {
    const body = JSON.parse(
      valueJson(
        origin,
        detail({
          type: 'list',
          length: 250,
          entries: [{ value: '�', binary: true }],
          note: '200 of 250 elements shown.',
        }),
      ),
    ) as Record<string, unknown>;
    expect(body['partial']).toBe(true);
    expect(body['note']).toBe('200 of 250 elements shown.');
    expect(body['length']).toBe(250);
    expect(body['binary']).toContain('not text');
    const whole = JSON.parse(valueJson(origin, detail({ entries: [{ value: 'x' }] }))) as Record<string, unknown>;
    expect(whole['partial']).toBe(false);
  });

  it('never carries a credential, whatever the answer it was drawn from held', () => {
    // An answer as the route writes it, plus value-shaped fields it never
    // sends: none may reach a file.
    const answer: InstanceAnswer & { password: string } = {
      instance: { namespace: 'data', service: 'redis-master', port: 6379, why: 'the port is named redis' },
      pod: 'redis-0',
      db: 0,
      server: { engine: 'Redis', version: '7.4', mode: 'standalone', databases: 16 },
      credential: { secret: 'redis-secret', key: 'redis-password', source: 'REDIS_PASSWORD', pod: 'redis-0' },
      password: 'hunter2',
      scan: scan(),
      key: detail({ entries: [{ value: 'v' }] }),
    };
    const from: Origin = { cluster: 'docker-desktop', instance: answer.instance, db: answer.db };
    const files = [
      keysCsv(answer.scan?.keys ?? []),
      keysJson(from, answer.scan ?? scan()),
      valueJson(from, answer.key ?? detail({})),
      valueRaw(answer.key ?? detail({})),
    ];
    for (const text of files) {
      for (const secret of ['hunter2', 'redis-secret', 'redis-password', 'REDIS_PASSWORD', 'credential', 'the port is named']) {
        expect(text).not.toContain(secret);
      }
    }
  });
});

describe('the file', () => {
  it('is named so a browser accepts it', () => {
    expect(fileName(['data', 'redis-master', 'db0', 'user:1/ä'], 'json')).toBe('data-redis-master-db0-user_1_.json');
    expect(fileName(['', '::'], 'csv')).toBe('_.csv');
    expect(fileName([], 'txt')).toBe('redis.txt');
  });

  it('is handed to the browser as a download, and says when the browser cannot make one', async () => {
    const made: Blob[] = [];
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    const saved = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };
    try {
      URL.createObjectURL = (b: Blob) => {
        made.push(b);
        return 'blob:x';
      };
      URL.revokeObjectURL = () => undefined;
      expect(download('a,b', 'keys.csv', 'text/csv')).toBe(true);
      expect(click).toHaveBeenCalledTimes(1);
      expect(await made[0]?.text()).toBe('a,b');
      expect(made[0]?.type).toBe('text/csv');

      // A browser with no object URLs: said, not silently nothing.
      (URL as unknown as { createObjectURL: unknown }).createObjectURL = undefined;
      expect(download('x', 'x.txt', 'text/plain')).toBe(false);
    } finally {
      URL.createObjectURL = saved.create;
      URL.revokeObjectURL = saved.revoke;
    }
  });
});

describe('the review of da9da8f', () => {
  it('keeps the raw column decodable: no formula guard on base64url', () => {
    const csv = keysCsv([{ name: 'x', raw: '-_8', type: 'string', ttl: 'no expiry' }]);
    expect(csv.split('\r\n')[1]).toBe('x,string,no expiry,,-_8');
  });

  it('writes a hash whose fields read the same as pairs, losing none', () => {
    const d = detail({ type: 'hash', entries: [{ field: '�', value: '1', binary: true }, { field: '�', value: '2', binary: true }] });
    const body = JSON.parse(valueJson(origin, d)) as { value: unknown };
    expect(body.value).toEqual([
      { field: '�', value: '1' },
      { field: '�', value: '2' },
    ]);
  });
});

describe('a value read in pages', () => {
  it('says in the JSON how much of the key is loaded while pages are left, and nothing once it is whole', () => {
    const more = JSON.parse(valueJson(origin, detail({ type: 'hash', length: 250, next: 'h:42', entries: [{ field: 'a', value: '1' }] }))) as Record<string, unknown>;
    expect(more['partial']).toBe(true);
    expect(more['loaded']).toBe('1 of 250');
    const whole = JSON.parse(valueJson(origin, detail({ type: 'hash', length: 1, entries: [{ field: 'a', value: '1' }] }))) as Record<string, unknown>;
    expect(whole['partial']).toBe(false);
    expect(whole).not.toHaveProperty('loaded');
  });

  it('writes a stream entry by entry: its ID and its fields', () => {
    const d = detail({ type: 'stream', entries: [{ field: '1-0', value: '{"user":"42"}' }, { field: '1-1', value: '{"a":"�' }] });
    expect((JSON.parse(valueJson(origin, d)) as { value: unknown }).value).toEqual([
      { id: '1-0', fields: { user: '42' } },
      { id: '1-1', fields: '{"a":"�' },
    ]);
    expect(valueRaw(d)).toBe('1-0\t{"user":"42"}\n1-1\t{"a":"�\n');
  });
});
