import { gzipSync } from 'node:zlib';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Navigate, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { redis } from './index';

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * One instance's page, against the bodies `extras/redis` writes. The template
 * tests of the prototype that read `redis.html` - the strip carries every
 * fact, it names the Secret and never its contents, a truncated scan says it
 * is a sample - are ported here, with the box and the way back.
 */

type Replies = Record<string, { status?: number; body: unknown }>;

/** The Service route, which the page reads for the deployment's other members:
 *  discovery only, it dials nothing, so `asked` - the reads that open a
 *  tunnel - leaves it out, and `services` records it. */
const SERVICES = '/x/docker-desktop/redis/services/';
let services: string[] = [];
/** How each URL was asked: its method and whether it carried the console's
 *  mutation header. */
let methods: Record<string, string> = {};

function stub(replies: Replies): string[] {
  const asked: string[] = [];
  services = [];
  methods = {};
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const marked = new Headers(init?.headers).get('X-KubeTower-Request') === '1';
      methods[url] = `${init?.method ?? 'GET'}${marked ? ' marked' : ''}`;
      if (url.startsWith(SERVICES)) services.push(url);
      else asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(
        new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }),
      );
    }),
  );
  return asked;
}

function mount(path: string) {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: redis.routes }], {
    initialEntries: [`/c/docker-desktop/${path}`],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const ROUTE = '/x/docker-desktop/redis/instances/cache/redis-master/6379';

const answer = (over: Record<string, unknown> = {}) => ({
  instance: { namespace: 'cache', service: 'redis-master', port: 6379, portName: 'tcp-redis', why: 'the port is named tcp-redis' },
  pod: 'redis-master-0',
  db: 0,
  server: { engine: 'Valkey', version: '8.0.1', mode: 'standalone', databases: 16 },
  credential: {
    secret: 'redis',
    key: 'redis-password',
    source: 'the REDIS_PASSWORD variable of container redis in pod redis-master-0',
    pod: 'redis-master-0',
    // A value-shaped field the route never sends: the page must not draw it
    // even if one arrived.
    password: 'hunter2-must-not-render',
    value: 'hunter2-must-not-render',
  },
  overview: { keys: 42, usedMemory: '1.0 MiB', keyspace: [{ index: 0, keys: 42, expires: 3 }, { index: 2, keys: 1, expires: 0 }] },
  scan: {
    match: '',
    cursor: '0',
    next: '96',
    done: false,
    keys: [
      { name: 'session:1', type: 'string', ttl: '1m' },
      { name: 'bin:��', raw: 'YmluOv_-', type: 'hash', ttl: 'no expiry' },
    ],
  },
  ...over,
});

describe('one instance', () => {
  it('carries every fact on its strip, and names the Secret and never its contents', async () => {
    stub({ [ROUTE]: { body: answer() } });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('session:1')).toBeTruthy();

    const strip = screen.getByLabelText('This instance').textContent ?? '';
    for (const fact of [
      '42',
      '1.0 MiB',
      'redis-master-0',
      'db0 42',
      'db2 1',
      'redis-password',
      'Secret redis',
    ]) {
      expect(strip).toContain(fact);
    }
    expect(document.body.textContent).not.toContain('hunter2');
    // Each database of the keyspace is a control, the one read pressed.
    expect(document.querySelector('[data-db="0"]')?.getAttribute('aria-pressed')).toBe('true');
    expect(document.querySelector('[data-db="2"]')?.getAttribute('title')).toBe('1 key, 0 expiring');
    expect(document.querySelector('[data-fact="password"]')?.getAttribute('title')).toBe(
      'Found as the REDIS_PASSWORD variable of container redis in pod redis-master-0',
    );
    // The engine is on the header, in the tile's pills; why it was offered on
    // the name's hover.
    const header = document.querySelector('header')?.textContent ?? '';
    expect(header).toContain('Valkey 8.0.1');
    expect(header).toContain('reachable');
    expect(document.querySelector('h1')?.getAttribute('title')).toContain('the port is named tcp-redis');
  });

  it('says a scan that is not done is a sample, and an empty round is not an empty database', async () => {
    stub({ [ROUTE]: { body: answer() } });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(document.querySelector('[data-scan="sample"]')?.textContent).toContain('not all of it');
    expect(document.querySelector('[data-more]')?.getAttribute('data-more')).toBe('96');
    cleanup();

    stub({ [ROUTE]: { body: answer({ scan: { match: '', cursor: '0', next: '7', done: false, keys: [] } }) } });
    mount('x/redis/cache/redis-master/6379');
    expect((await screen.findByText(/No keys in this slice/)).getAttribute('data-scan')).toBe('round-empty');
    expect(screen.getByText(/not an empty database/)).toBeTruthy();
    cleanup();

    stub({ [`${ROUTE}?match=gone%3A*`]: { body: answer({ scan: { match: 'gone:*', cursor: '0', done: true, keys: [] } }) } });
    mount('x/redis/cache/redis-master/6379?match=gone:*');
    expect((await screen.findByText(/the scan is complete/)).getAttribute('data-scan')).toBe('done-empty');
    expect(screen.getByText('gone:*')).toBeTruthy();
  });

  it('sends the pattern on Enter only, into the address with no new history entry, and drops the cursor', async () => {
    const asked = stub({
      [`${ROUTE}?cursor=96`]: { body: answer() },
      [`${ROUTE}?match=session%3A*`]: { body: answer({ scan: { match: 'session:*', cursor: '0', done: true, keys: [{ name: 'session:1', type: 'string', ttl: '1m' }] } }) },
    });
    const router = mount('x/redis/cache/redis-master/6379?cursor=96');
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(asked).toEqual([`${ROUTE}?cursor=96`]);

    const box = screen.getByLabelText('Key pattern');
    expect(box.getAttribute('name')).toBe('match');
    fireEvent.change(box, { target: { value: 'session:*' } });
    fireEvent.change(box, { target: { value: 'session:**' } });
    fireEvent.change(box, { target: { value: 'session:*' } });
    expect(asked).toHaveLength(1);

    fireEvent.submit(box.closest('form') as HTMLFormElement);
    await waitFor(() => expect(asked).toContain(`${ROUTE}?match=session%3A*`));
    expect(asked).toHaveLength(2);
    expect(router.state.location.search).toBe('?match=session%3A*');
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('reads the next slice with the cursor the server handed back', async () => {
    const asked = stub({ [ROUTE]: { body: answer() }, [`${ROUTE}?cursor=96`]: { body: answer() } });
    const router = mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('session:1')).toBeTruthy();
    fireEvent.click(screen.getByText('Next slice'));
    await waitFor(() => expect(asked).toContain(`${ROUTE}?cursor=96`));
    expect(router.state.location.search).toBe('?cursor=96');
  });

  it('goes back to the inventory it came from: `scope`, not the instance\'s own namespace', async () => {
    stub({ [ROUTE]: { body: answer() } });
    mount('x/redis/cache/redis-master/6379?scope=cache');
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(document.querySelector('[data-back]')?.getAttribute('href')).toBe('/c/docker-desktop/x/redis?ns=cache');
    cleanup();

    stub({ [ROUTE]: { body: answer() } });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(document.querySelector('[data-back]')?.getAttribute('href')).toBe('/c/docker-desktop/x/redis');
  });

  it('opens another database in place, replacing the address and dropping the cursor', async () => {
    const asked = stub({ [`${ROUTE}?cursor=96`]: { body: answer() }, [`${ROUTE}?db=2`]: { body: answer({ db: 2 }) } });
    const router = mount('x/redis/cache/redis-master/6379?cursor=96');
    await waitFor(() => expect(document.querySelector('[data-db="2"]')).toBeTruthy());
    fireEvent.click(document.querySelector('[data-db="2"]') as HTMLButtonElement);
    await waitFor(() => expect(asked).toContain(`${ROUTE}?db=2`));
    expect(router.state.location.search).toBe('?db=2');
    expect(router.state.historyAction).toBe('REPLACE');
    await waitFor(() => expect(document.querySelector('[data-db="2"]')?.getAttribute('aria-pressed')).toBe('true'));
  });

  it('keeps the arrow a link to the inventory on a pasted link, whatever the page does next', async () => {
    stub({ [ROUTE]: { body: answer() }, [`${ROUTE}?db=2`]: { body: answer({ db: 2 }) } });
    mount('x/redis/cache/redis-master/6379?scope=cache');
    await waitFor(() => expect(document.querySelector('[data-db="2"]')).toBeTruthy());
    expect(document.querySelector('[data-back]')?.tagName).toBe('A');
    fireEvent.click(document.querySelector('[data-db="2"]') as HTMLButtonElement);
    await waitFor(() => expect(document.querySelector('[data-db="2"]')?.getAttribute('aria-pressed')).toBe('true'));
    // A replace has a key of its own; the arrow must not become a Back out
    // of the console (review of 9af5926).
    expect(document.querySelector('[data-back]')?.tagName).toBe('A');
    expect(document.querySelector('[data-back]')?.getAttribute('href')).toBe('/c/docker-desktop/x/redis?ns=cache');
  });

  it('keeps the arrow a link after a redirect that replaced into the first entry of the tab', async () => {
    // The browser router's own state on the tab's first entry, as signing in
    // and being sent on with `replace` leaves it: a fresh key, index 0.
    window.history.replaceState({ usr: null, key: 'fresh', idx: 0 }, '');
    try {
      stub({ [ROUTE]: { body: answer() } });
      const router = createMemoryRouter(
        [
          { path: 'login', element: <Navigate replace to="/c/docker-desktop/x/redis/cache/redis-master/6379" /> },
          { path: 'c/:cluster', children: redis.routes },
        ],
        { initialEntries: ['/login'] },
      );
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(
        <QueryClientProvider client={client}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
      expect(await screen.findByText('session:1')).toBeTruthy();
      expect(router.state.location.key).not.toBe('default');
      expect(document.querySelector('[data-back]')?.tagName).toBe('A');
    } finally {
      window.history.replaceState(null, '');
    }
  });

  it('goes back to where it was opened from, as the browser does, across a member and a database', async () => {
    stub({
      [ROUTE]: { body: answer() },
      [`${ROUTE}?db=2`]: { body: answer({ db: 2 }) },
      [`${SERVICES}cache/redis-master`]: {
        body: {
          namespace: 'cache',
          service: 'redis-master',
          deployments: [
            {
              key: 'cache/redis',
              name: 'redis',
              evidence: ['release'],
              workloads: [],
              members: [
                { key: 'm', pods: ['redis-master-0'], behind: '', ready: 1, reachable: true, endpointless: false, workloads: [], role: 'primary', doors: [{ namespace: 'cache', service: 'redis-master', port: 6379, why: 'w' }] },
                { key: 'r', pods: ['redis-replicas-0'], behind: '', ready: 1, reachable: true, endpointless: false, workloads: [], role: 'replica', doors: [{ namespace: 'cache', service: 'redis-replicas', port: 6379, why: 'w' }] },
              ],
            },
          ],
        },
      },
    });
    const router = createMemoryRouter(
      [
        { path: 'c/:cluster/services', element: <p>the services list</p> },
        { path: 'c/:cluster', children: redis.routes },
      ],
      { initialEntries: ['/c/docker-desktop/services?open=cache%2Fredis-master', `/c/docker-desktop/x/redis/cache/redis-master/6379`], initialIndex: 1 },
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(document.querySelector('[data-db="2"]')).toBeTruthy());
    // A button, not a link to the inventory: there is a page behind this one.
    expect(document.querySelector('[data-back]')?.tagName).toBe('BUTTON');
    fireEvent.click(document.querySelector('[data-db="2"]') as HTMLButtonElement);
    await waitFor(() => expect(router.state.location.search).toBe('?db=2'));
    fireEvent.click(document.querySelector('[data-member-door="redis-replicas:6379"]') as HTMLAnchorElement);
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/docker-desktop/x/redis/cache/redis-replicas/6379'));
    expect(router.state.historyAction).toBe('REPLACE');
    fireEvent.click(document.querySelector('[data-back]') as HTMLButtonElement);
    await screen.findByText('the services list');
    expect(router.state.location.search).toBe('?open=cache%2Fredis-master');
  });

  it('says an instance discovery no longer finds is gone, not unreachable', async () => {
    stub({
      [ROUTE]: { status: 404, body: { code: 'instance_gone', kind: 'absent', message: 'There is no Redis or Valkey at cache/redis-master:6379 …' } },
    });
    mount('x/redis/cache/redis-master/6379');
    expect((await screen.findByText(/any more/, { selector: 'p.font-medium' })).closest('[data-state]')?.getAttribute('data-state')).toBe('gone');
    expect(screen.getByText('not here any more')).toBeTruthy();
    expect(screen.queryByText('could not open')).toBeNull();
  });

  it('links the other members of its deployment, read from discovery only', async () => {
    const member = (key: string, service: string, role: string, extra: Record<string, unknown> = {}) => ({
      key,
      pods: [`${key}-0`],
      behind: `${key}-0`,
      ready: 1,
      reachable: true,
      endpointless: false,
      workloads: [],
      role,
      roleFrom: 'labels',
      doors: [{ namespace: 'cache', service, port: 6379, why: 'the port is named tcp-redis' }],
      ...extra,
    });
    const asked = stub({
      [ROUTE]: { body: answer() },
      [`${SERVICES}cache/redis-master`]: {
        body: {
          namespace: 'cache',
          service: 'redis-master',
          deployments: [
            {
              key: 'd',
              name: 'redis',
              evidence: ['release'],
              workloads: [],
              members: [
                member('m', 'redis-master', 'primary'),
                member('r', 'redis-replicas', 'replica'),
                { ...member('s', 'redis', 'sentinel', { sentinel: true }), doors: [{ namespace: 'cache', service: 'redis', port: 26379, why: 'the port is named tcp-sentinel' }] },
              ],
            },
          ],
        },
      },
    });
    mount('x/redis/cache/redis-master/6379?scope=cache');
    await waitFor(() => expect(document.querySelector('[data-members="redis"]')).toBeTruthy());
    const doors = [...document.querySelectorAll('[data-member-door]')];
    expect(doors.map((d) => d.getAttribute('data-member-door'))).toEqual(['redis-master:6379', 'redis-replicas:6379', 'redis:26379']);
    expect(doors[0]?.getAttribute('aria-current')).toBe('page');
    expect(doors[1]?.getAttribute('href')).toBe('/c/docker-desktop/x/redis/cache/redis-replicas/6379?scope=cache');
    // A Sentinel is named, never a link.
    expect(doors[2]?.tagName).toBe('SPAN');
    expect(services).toEqual([`${SERVICES}cache/redis-master`]);
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(asked).toEqual([ROUTE]);
  });

  it('says when the other members could not be read, and draws no members for a lone instance', async () => {
    stub({ [ROUTE]: { body: answer() }, [`${SERVICES}cache/redis-master`]: { status: 502, body: { code: 'list_failed', kind: 'failure', message: 'The Services could not be read.' } } });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('The other members of this deployment could not be read.')).toBeTruthy();
    cleanup();

    stub({ [ROUTE]: { body: answer() }, [`${SERVICES}cache/redis-master`]: { status: 404, body: { code: 'not_redis', kind: 'absent', message: 'Not a Redis.' } } });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(document.querySelector('[data-members]')).toBeNull();
  });

  it('draws a refusal as a refusal, with its own sentence', async () => {
    stub({
      [ROUTE]: {
        status: 409,
        body: {
          code: 'password_refused',
          kind: 'refusal',
          message: 'This instance refused the password in Secret redis, key redis-password - read from the REDIS_PASSWORD variable.',
        },
      },
    });
    mount('x/redis/cache/redis-master/6379');
    const said = await screen.findByText(/refused the password in Secret redis/);
    expect(said.closest('[data-problem]')?.getAttribute('data-kind')).toBe('refusal');
    expect(screen.getByText('Refused')).toBeTruthy();
  });

  it('opens a key bounded, by its raw bytes when its name is not text, with the note on what was cut', async () => {
    const asked = stub({
      [ROUTE]: { body: answer() },
      [`${ROUTE}/key?raw=YmluOv_-`]: {
        body: answer({
          key: {
            name: 'bin:��',
            raw: 'YmluOv_-',
            type: 'hash',
            ttl: 'no expiry',
            length: 250,
            entries: [
              { field: 'a', value: '1' },
              { field: 'b', value: '\u0000\u0001', binary: true },
              { field: 'c', value: 'x'.repeat(16), clipped: true, bytes: 102400 },
            ],
            note: '200 of 250 fields shown. One element is longer than 8.0 KiB and only its start is shown.',
          },
        }),
      },
    });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('session:1')).toBeTruthy();
    expect(asked.some((u) => u.includes('/key'))).toBe(false);

    fireEvent.click(screen.getByText('bin:��'));
    expect(await screen.findByText('200 of 250 fields shown. One element is longer than 8.0 KiB and only its start is shown.')).toBeTruthy();
    expect(asked).toContain(`${ROUTE}/key?raw=YmluOv_-`);
    expect(screen.getByText('not text')).toBeTruthy();
    expect(screen.getByText('clipped: 100 KiB in all').hasAttribute('data-clipped')).toBe(true);
    // The reads that dial are POSTs with the console's header, so a
    // cross-site link cannot make the console dial.
    expect(methods[ROUTE]).toBe('POST marked');
    expect(methods[`${ROUTE}/key?raw=YmluOv_-`]).toBe('POST marked');
  });
});

describe('the key list and the value view', () => {
  const many = {
    match: '',
    cursor: '0',
    next: '96',
    done: false,
    keys: [
      { name: 'user:1:name', type: 'string', ttl: 'no expiry', memory: 56 },
      { name: 'user:1:prefs', type: 'hash', ttl: '2h', memory: 1536 },
      { name: 'user:2:name', type: 'string', ttl: 'no expiry', memory: 56 },
      { name: 'board', type: 'zset', ttl: 'no expiry' },
    ],
  };

  it('folds the round into a tree by `:` prefix with a badge, the expiry and the memory, and flattens on request', async () => {
    stub({ [ROUTE]: { body: answer({ scan: many }) } });
    mount('x/redis/cache/redis-master/6379');
    expect(await screen.findByText('user:')).toBeTruthy();
    const folders = [...document.querySelectorAll('[data-folder]')].map((f) => f.getAttribute('data-folder'));
    expect(folders).toEqual(['user:', 'user:1:', 'user:2:']);
    const prefs = document.querySelector('[data-key="user:1:prefs"]');
    expect(prefs?.querySelector('[data-type="hash"]')?.textContent).toBe('hash');
    expect(prefs?.textContent).toContain('2h');
    expect(prefs?.textContent).toContain('1.5 KiB');
    // The leaf is drawn by its last segment, and still opens by its whole name.
    expect(prefs?.querySelector('button')?.textContent).toBe('prefs');

    fireEvent.click(screen.getByText('collapse all'));
    expect(document.querySelector('[data-key="user:1:prefs"]')).toBeNull();
    expect(document.querySelector('[data-key="board"]')).toBeTruthy();

    fireEvent.click(document.querySelector('[data-view="flat"]') as HTMLButtonElement);
    expect(document.querySelectorAll('[data-folder]')).toHaveLength(0);
    expect(screen.getByText('user:1:prefs')).toBeTruthy();
  });

  it('pretty-prints a JSON string, keeps the stored text one click away, and lets it be copied', async () => {
    stub({
      [ROUTE]: { body: answer() },
      [`${ROUTE}/key?key=session%3A1`]: {
        body: answer({ key: { name: 'session:1', type: 'string', ttl: '1m', length: 15, entries: [{ value: '{"user":"ada"}' }] } }),
      },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    const pre = await waitFor(() => {
      const found = document.querySelector('[data-string]');
      if (found === null) throw new Error('no value yet');
      return found;
    });
    expect(pre.getAttribute('data-string')).toBe('json');
    expect(pre.textContent).toBe('{\n  "user": "ada"\n}');
    fireEvent.click(document.querySelector('[data-string-view="raw"]') as HTMLButtonElement);
    expect(document.querySelector('[data-string]')?.textContent).toBe('{"user":"ada"}');
    // The value's copy is in its `⋯` now; the key's name keeps its own button.
    expect(screen.queryByLabelText('Copy the value as shown')).toBeNull();
    expect(screen.getByLabelText("Copy the key's name")).toBeTruthy();
  });

  it('draws a hash as a field table and a sorted set with its scores', async () => {
    stub({
      [ROUTE]: { body: answer({ scan: many }) },
      [`${ROUTE}/key?key=user%3A1%3Aprefs`]: {
        body: answer({ key: { name: 'user:1:prefs', type: 'hash', ttl: '2h', length: 2, entries: [{ field: 'lang', value: 'en' }, { field: 'theme', value: 'dark' }] } }),
      },
      [`${ROUTE}/key?key=board`]: {
        body: answer({ key: { name: 'board', type: 'zset', ttl: 'no expiry', length: 2, entries: [{ value: 'p1', score: '1.5' }, { value: 'p2', score: '2.5' }] } }),
      },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('prefs'));
    const hash = await waitFor(() => {
      const t = document.querySelector('[data-entries="hash"]');
      if (t === null) throw new Error('no table yet');
      return t;
    });
    const cells = [...hash.querySelectorAll('tbody tr')].map((r) => [...r.querySelectorAll('td')].slice(0, 2).map((c) => c.textContent));
    expect(cells).toEqual([
      ['lang', 'en'],
      ['theme', 'dark'],
    ]);

    fireEvent.click(screen.getByText('board'));
    const zset = await waitFor(() => {
      const t = document.querySelector('[data-entries="zset"]');
      if (t === null) throw new Error('no table yet');
      return t;
    });
    expect([...zset.querySelectorAll('tbody tr')].map((r) => [...r.querySelectorAll('td')].slice(0, 2).map((c) => c.textContent))).toEqual([
      ['1.5', 'p1'],
      ['2.5', 'p2'],
    ]);
  });

  it('exports what is on screen, says when it is partial, and asks the cluster for nothing more', async () => {
    const asked = stub({
      [ROUTE]: { body: answer() },
      [`${ROUTE}/key?key=session%3A1`]: {
        body: answer({ key: { name: 'session:1', type: 'string', ttl: '1m', entries: [{ value: 'x'.repeat(10) }], note: 'This value is 80.0 KiB; the first 64.0 KiB are shown.' } }),
      },
    });
    const made: { text: Promise<string>; type: string }[] = [];
    const saved = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    URL.createObjectURL = (b: Blob) => {
      made.push({ text: b.text(), type: b.type });
      return 'blob:x';
    };
    URL.revokeObjectURL = () => undefined;
    try {
      mount('x/redis/cache/redis-master/6379');
      expect(await screen.findByText('session:1')).toBeTruthy();
      const keys = document.querySelector('[data-exports="Export keys"]');
      expect(keys?.querySelector('[data-export-partial]')).toBeTruthy();
      fireEvent.click(screen.getByRole('button', { name: 'Export keys' }));
      expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Export CSV', 'Export JSON']);
      fireEvent.click(screen.getByRole('menuitem', { name: 'Export JSON' }));
      const listed = JSON.parse(await (made[0]?.text ?? Promise.resolve('{}'))) as Record<string, unknown>;
      expect(listed['partial']).toBe(true);
      expect((listed['keys'] as unknown[]).length).toBe(2);
      expect(JSON.stringify(listed)).not.toContain('redis-password');

      fireEvent.click(screen.getByText('session:1'));
      await screen.findByText('This value is 80.0 KiB; the first 64.0 KiB are shown.');
      const value = document.querySelector('[data-exports="Value actions"]');
      expect(value?.querySelector('[data-export-partial]')).toBeTruthy();
      fireEvent.click(screen.getByRole('button', { name: 'Value actions' }));
      fireEvent.click(screen.getByRole('menuitem', { name: 'Export RAW' }));
      expect(await made[1]?.text).toBe('xxxxxxxxxx');
      expect(made[1]?.type).toBe('text/plain');
      expect(click).toHaveBeenCalledTimes(2);
      // Two reads: the instance and the key. The exports asked for nothing.
      expect(asked).toEqual([ROUTE, `${ROUTE}/key?key=session%3A1`]);
    } finally {
      URL.createObjectURL = saved.create;
      URL.revokeObjectURL = saved.revoke;
    }
  });
});

/**
 * The key panel read in pages, with values decoded before they are drawn, and
 * its copy and exports behind one `⋯`.
 */
describe('the key panel: pages, decoding and the value menu', () => {
  const KEY = `${ROUTE}/key?key=session%3A1`;
  const page = (n: number, from: number) => Array.from({ length: n }, (_, i) => ({ field: `f${String(from + i)}`, value: `v${String(from + i)}` }));
  const hash = (entries: unknown[], over: Record<string, unknown> = {}) =>
    answer({ key: { name: 'session:1', type: 'hash', ttl: 'no expiry', length: 250, pageSize: 100, entries, ...over } });

  /** The value cells of the hash table, in order. */
  const fields = () => [...document.querySelectorAll('[data-entries] [data-field]')].map((c) => c.textContent);

  it('appends the next page on « Load more », and the button goes on the last page', async () => {
    const asked = stub({
      [ROUTE]: { body: answer() },
      [KEY]: { body: hash(page(100, 0), { next: 'h:42' }) },
      [`${KEY}&page=h%3A42`]: { body: hash(page(100, 100), { next: 'h:77' }) },
      [`${KEY}&page=h%3A77`]: { body: hash(page(50, 200)) },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    expect(await screen.findByText('100 of 250 loaded')).toBeTruthy();
    expect(fields()).toHaveLength(100);

    fireEvent.click(screen.getByText('Load more'));
    expect(await screen.findByText('200 of 250 loaded')).toBeTruthy();
    expect(fields()).toHaveLength(200);
    expect(fields()[100]).toBe('f100');

    fireEvent.click(screen.getByText('Load more'));
    await waitFor(() => expect(fields()).toHaveLength(250));
    expect(fields()[249]).toBe('f249');
    expect(screen.queryByText('Load more')).toBeNull();
    expect(document.querySelector('[data-paging]')).toBeNull();
    // The whole key is held: not partial any more.
    expect(document.querySelector('[data-exports="Value actions"] [data-export-partial]')).toBeNull();
    expect(asked.filter((u) => u.includes('/key'))).toEqual([KEY, `${KEY}&page=h%3A42`, `${KEY}&page=h%3A77`]);
    expect(methods[`${KEY}&page=h%3A42`]).toBe('POST marked');
  });

  it('says a page that failed beside the button, and keeps what is loaded', async () => {
    stub({
      [ROUTE]: { body: answer() },
      [KEY]: { body: hash(page(100, 0), { next: 'h:42' }) },
      [`${KEY}&page=h%3A42`]: { status: 502, body: { code: 'read_failed', kind: 'failure', message: 'The server stopped answering.' } },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    await screen.findByText('100 of 250 loaded');
    fireEvent.click(screen.getByText('Load more'));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toBe('The next page could not be read: The server stopped answering.');
    expect(fields()).toHaveLength(100);
    expect(screen.getByText('Load more')).toBeTruthy();
    expect(screen.getByText('100 of 250 loaded')).toBeTruthy();
  });

  it('starts another key from its first page', async () => {
    stub({
      [ROUTE]: { body: answer({ scan: { match: '', cursor: '0', done: true, keys: [{ name: 'session:1', type: 'hash', ttl: '-' }, { name: 'other', type: 'hash', ttl: '-' }] } }) },
      [KEY]: { body: hash(page(100, 0), { next: 'h:42' }) },
      [`${KEY}&page=h%3A42`]: { body: hash(page(100, 100), { next: 'h:77' }) },
      [`${ROUTE}/key?key=other`]: { body: answer({ key: { name: 'other', type: 'hash', ttl: '-', length: 120, entries: page(100, 0), next: 'h:9' } }) },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    await screen.findByText('100 of 250 loaded');
    fireEvent.click(screen.getByText('Load more'));
    await screen.findByText('200 of 250 loaded');
    fireEvent.click(screen.getByText('other'));
    expect(await screen.findByText('100 of 120 loaded')).toBeTruthy();
    expect(fields()).toHaveLength(100);
  });

  it('draws a stream entry by entry, its ID in the first column', async () => {
    stub({
      [ROUTE]: { body: answer() },
      [KEY]: {
        body: answer({
          key: {
            name: 'session:1',
            type: 'stream',
            ttl: 'no expiry',
            length: 2,
            entries: [
              { field: '1730800000000-0', value: '{"user":"42","action":"login"}' },
              { field: '1730800000001-0', value: '{"user":"7"}' },
            ],
          },
        }),
      },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    await waitFor(() => expect(fields()).toEqual(['1730800000000-0', '1730800000001-0']));
    const table = document.querySelector('[data-entries="stream"]');
    expect([...(table?.querySelectorAll('th') ?? [])].map((h) => h.textContent)).toEqual(['id', 'entry', '']);
    expect(table?.textContent).toContain('"user": "7"');
  });

  it('cuts a field name to one line with the whole name in its title', async () => {
    const long = 'a-very-long-field-name-'.repeat(10);
    stub({ [ROUTE]: { body: answer() }, [KEY]: { body: hash([{ field: long, value: 'x' }]) } });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    const cell = await waitFor(() => {
      const c = document.querySelector('[data-field]');
      if (c === null) throw new Error('no field yet');
      return c;
    });
    expect(cell.getAttribute('title')).toBe(long);
    expect(cell.className).toContain('truncate');
    expect(cell.className).toContain('max-w-[calc(40cqw-1rem)]'); // style-allow: the panel's cap, asserted
  });

  it('shows the start of a long value, unfolds it whole on a click, and folds it back', async () => {
    const long = 'word '.repeat(100);
    stub({ [ROUTE]: { body: answer() }, [KEY]: { body: hash([{ field: 'f', value: long }]) } });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    const unfold = await waitFor(() => {
      const b = document.querySelector('[data-unfold]');
      if (b === null) throw new Error('no fold yet');
      return b as HTMLButtonElement;
    });
    const cell = () => unfold.closest('td')?.textContent ?? '';
    expect(cell()).toContain(`${long.slice(0, 200)}…`);
    expect(cell()).not.toContain(long);
    expect(unfold.textContent).toBe('show all (500 characters)');
    fireEvent.click(unfold);
    expect(cell()).toContain(long);
    expect(unfold.getAttribute('aria-expanded')).toBe('true');
    fireEvent.click(unfold);
    expect(cell()).not.toContain(long);
  });

  it('decodes base64 JSON and gzip, marks what was decoded, and draws bytes that read as nothing as their size and hex', async () => {
    const bytes = new Uint8Array(2355).map((_, i) => (i * 37 + 0x80) % 256);
    const b64 = (b: Uint8Array | string) => Buffer.from(b).toString('base64');
    stub({
      [ROUTE]: { body: answer() },
      [KEY]: {
        body: hash([
          { field: 'json', value: b64('{"a":1}') },
          { field: 'zipped', value: '�', binary: true, raw: b64(gzipSync(Buffer.from('hello there'))) },
          { field: 'noise', value: '��', binary: true, raw: b64(bytes) },
        ]),
      },
    });
    mount('x/redis/cache/redis-master/6379');
    fireEvent.click(await screen.findByText('session:1'));
    expect(await screen.findByText('base64 → JSON')).toBeTruthy();
    expect(await screen.findByText('gzip → text')).toBeTruthy();
    expect(screen.getByText('hello there')).toBeTruthy();

    const binary = screen.getByText('binary, 2.3 KiB');
    // The bytes are not drawn as characters.
    expect(binary.closest('td')?.textContent).not.toContain('�');
    expect(document.querySelector('[data-hex]')).toBeNull();
    fireEvent.click(binary);
    const hex = document.querySelector('[data-hex]')?.textContent ?? '';
    expect(hex.split('\n')[0]).toBe('00000000  80 a5 ca ef 14 39 5e 83  a8 cd f2 17 3c 61 86 ab  .....9^.....<a..');
  });

  it('holds Copy, Export JSON and Export RAW in one `⋯`, and the separate buttons are gone', async () => {
    const written: string[] = [];
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (t: string) => (written.push(t), Promise.resolve()) },
    });
    stub({ [ROUTE]: { body: answer() }, [KEY]: { body: hash(page(2, 0), { next: 'h:1', length: 9 }) } });
    try {
      mount('x/redis/cache/redis-master/6379');
      fireEvent.click(await screen.findByText('session:1'));
      await screen.findByText('2 of 9 loaded');
      expect(document.querySelectorAll('[data-export]')).toHaveLength(0);
      fireEvent.click(screen.getByRole('button', { name: 'Value actions' }));
      expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Copy', 'Export JSON', 'Export RAW']);
      fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }));
      // Copy holds what Export RAW writes: the loaded elements, as stored.
      await waitFor(() => expect(written).toEqual(['f0\tv0\nf1\tv1\n']));
      expect(await screen.findByText('Copied')).toBeTruthy();
      // Each element's own copy button, and the key name's, are as they were.
      expect(screen.getByLabelText('Copy the value of f0')).toBeTruthy();
      expect(screen.getByLabelText("Copy the key's name")).toBeTruthy();
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard');
    }
  });

  it('exports what is loaded, and says in the JSON how much of the key that is', async () => {
    const made: Promise<string>[] = [];
    const saved = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    URL.createObjectURL = (b: Blob) => {
      made.push(b.text());
      return 'blob:x';
    };
    URL.revokeObjectURL = () => undefined;
    stub({
      [ROUTE]: { body: answer() },
      [KEY]: { body: hash(page(100, 0), { next: 'h:42' }) },
      [`${KEY}&page=h%3A42`]: { body: hash(page(100, 100), { next: 'h:77' }) },
    });
    try {
      mount('x/redis/cache/redis-master/6379');
      fireEvent.click(await screen.findByText('session:1'));
      await screen.findByText('100 of 250 loaded');
      fireEvent.click(screen.getByText('Load more'));
      await screen.findByText('200 of 250 loaded');
      fireEvent.click(screen.getByRole('button', { name: 'Value actions' }));
      fireEvent.click(screen.getByRole('menuitem', { name: 'Export JSON' }));
      const body = JSON.parse(await (made[0] ?? Promise.resolve('{}'))) as Record<string, unknown>;
      expect(body['partial']).toBe(true);
      expect(body['loaded']).toBe('200 of 250');
      expect(Object.keys(body['value'] as object)).toHaveLength(200);
    } finally {
      URL.createObjectURL = saved.create;
      URL.revokeObjectURL = saved.revoke;
    }
  });
});
