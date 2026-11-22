import { cleanup, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage, ListPage } from '../api';
import { resetKeyboardStack } from '../keyboard/stack';
import { resetMenuMode, resetOpenGroups } from '../shell/menuMemory';
import { resetNavMemory } from '../shell/navMemory';
import { lastOpen, resetViewMemory } from '../shell/viewMemory';
import { mountApp, stubFetch, type StubReply } from '../testing/harness';

/**
 * `CanonicalCluster` while the cluster list is on its way, and when it fails.
 *
 * A redirect that draws the page first would mount it under the old spelling,
 * tear it down when the list answers, and mount it again under the canonical
 * one. On the Headlamp `--` spelling the reads would be made again under the
 * new name and `?open=` lost, because `ResourceList` consumes it on mount. The
 * harness's list normally answers before the lazy page mounts, which would
 * hide it.
 *
 * So the list is held back here, as a cold one is, and released by an event
 * rather than a timer: when `Pending` appears (the fixed component holding the
 * page) or when the page reads its own list (the old component, which drew the
 * page at once). A timer raced the lazy page's first load: a fixed 300 ms, and
 * then 1 s, could let the list answer before the old component's page mounted,
 * and the old behavior passed.
 */
const NAME = 'team@arn:aws:eks:eu-west-1:1:cluster/prod';
const PODS = '/c/team@arn:aws:eks:eu-west-1:1:cluster%2Fprod/pods';
const API = `/api/c/${encodeURIComponent(NAME)}`;
const OPEN = 'open=shop-prod%2Fstorefront-web';

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const column = {
  visible: true,
  sortable: true,
  secondary: false,
  gauged: false,
  copyable: false,
  prose: false,
  filterable: false,
} as const;

const pods: ListPage = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [
    { ...column, key: 'name', label: 'Name', order: 0, role: 'name' },
    { ...column, key: 'namespace', label: 'Namespace', order: 1, role: 'namespace' },
  ],
  rows: [
    {
      id: 'shop-prod/storefront-web',
      name: 'storefront-web',
      namespace: 'shop-prod',
      cells: { name: { text: 'storefront-web' }, namespace: { text: 'shop-prod' } },
    },
  ],
};

const detail: DetailPage = {
  kind: 'pods',
  namespace: 'shop-prod',
  name: 'storefront-web',
  title: 'Pod',
  tabs: [],
  sections: [
    {
      key: 'summary',
      label: 'Summary',
      state: 'ok',
      fields: [{ label: 'Status', value: 'Running', prose: false, pill: false }],
    },
  ],
};

const stubs = {
  '/api/session': session,
  '/api/clusters': {
    body: { state: 'ok', items: [{ name: NAME, label: 'team prod', state: 'ok' }] },
  },
  [`${API}/nav`]: { body: { state: 'ok', data: { groups: [] } } },
  [`${API}/extensions`]: { body: { state: 'empty', items: [] } },
  [`${API}/r/pods`]: { body: { state: 'ok', data: pods } },
  [`${API}/r/pods/shop-prod/storefront-web`]: { body: { state: 'ok', data: detail } },
} satisfies Record<string, StubReply>;

/** Every list read the page made, whether the cluster list had answered by
 *  then, and how long after the mount it came. */
interface Read {
  readonly path: string;
  readonly afterTheList: boolean;
  readonly at: number;
}

interface Controlled {
  readonly reads: Read[];
  readonly release: () => void;
}

let releaseHeld: () => void = () => undefined;

/**
 * The fixture, with `/api/clusters` answering `reply` - or, with `'held'`, not
 * answering until `release()` is called. `releaseOnRead` also releases it when
 * the page reads its own list, which only a page drawn before the list
 * answered can do.
 */
function clusterList(reply: StubReply | 'held', releaseOnRead = false): Controlled {
  stubFetch({ ...stubs, ...(reply === 'held' ? {} : { '/api/clusters': reply }) });
  const stubbed = globalThis.fetch;
  const reads: Read[] = [];
  const started = Date.now();
  let answered = reply !== 'held';
  const held = new Promise<void>((resolve) => {
    releaseHeld = resolve;
  });
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = (typeof input === 'string' ? input : String(input)).split('?')[0] ?? '';
    if (path === '/api/clusters' && reply === 'held') {
      await held;
      const answer = await stubbed(input, init);
      answered = true;
      return answer;
    }
    if (path.endsWith('/r/pods')) {
      reads.push({ path, afterTheList: answered, at: Date.now() - started });
      if (releaseOnRead) releaseHeld();
    }
    return stubbed(input, init);
  });
  return { reads, release: () => releaseHeld() };
}

function holding(): boolean {
  return screen.queryByText('Reading this cluster…') !== null;
}

function forgetEverything(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  resetViewMemory();
  localStorage.clear();
}

beforeEach(forgetEverything);

afterEach(() => {
  // A list still held would leave a fetch pending past the test.
  releaseHeld();
  cleanup();
  vi.unstubAllGlobals();
  forgetEverything();
});

describe('an old spelling arriving before the cluster list', () => {
  it.each([
    ['the front end spelling', `/c/${encodeURIComponent(NAME)}/pods?${OPEN}`],
    ['the Headlamp spelling', `/c/team@arn:aws:eks:eu-west-1:1:cluster--prod/pods?${OPEN}`],
  ])('on %s, mounts the page once, keeps open, and opens the drawer', async (_label, address) => {
    const { reads, release } = clusterList('held', true);
    const { router } = mountApp(address);
    const seen: string[] = [];
    const unsubscribe = router.subscribe((state) => {
      seen.push(`${state.location.pathname}${state.location.search}`);
    });
    // Released by whichever comes first: `Pending`, or the page's own read.
    await waitFor(() => {
      expect(holding() || reads.length > 0).toBe(true);
    });
    release();

    // The drawer opens: `open` reached the page that stayed.
    await waitFor(
      () => {
        expect(document.getElementById('drawer')?.textContent ?? '').toContain('Running');
      },
      { timeout: 3000 },
    );
    unsubscribe();

    // The address ends canonical, and it was canonical with `open` still on it
    // before the list took `open` off: the redirect carried it.
    expect(router.state.location.pathname).toBe(PODS);
    expect(seen).toContain(`${PODS}?${OPEN}`);
    expect(lastOpen(NAME)).toEqual({ kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' });

    // Mounted once: the page read its list only after the cluster list had
    // answered, only under the canonical name, and only once.
    expect(reads.map(({ path, afterTheList }) => ({ path, afterTheList }))).toEqual([
      { path: `${API}/r/pods`, afterTheList: true },
    ]);
  });
});

const problem = (status: number): StubReply => ({
  status,
  contentType: 'application/problem+json',
  body: { status, title: 'Internal Server Error', code: 'internal', detail: 'the list failed' },
});

describe('a cluster list that cannot resolve the address draws the page as it is', () => {
  const OLD = `/c/${encodeURIComponent(NAME)}/pods`;

  it.each([
    ['answers 500', problem(500)],
    ['is empty', { body: { state: 'empty', items: [] } }],
    [
      'is unreadable',
      { body: { state: 'unreadable', reason: { code: 'undecodable', message: 'bad file' } } },
    ],
    [
      'does not name the cluster',
      { body: { state: 'ok', items: [{ name: 'staging', label: 'staging', state: 'ok' }] } },
    ],
  ] satisfies [string, StubReply][])('when the list %s', async (_label, reply) => {
    const { reads } = clusterList(reply);
    const { router } = mountApp(OLD);

    // The page draws and reads its list; the hold does not outlast the list.
    await waitFor(
      () => {
        expect(reads.length).toBeGreaterThan(0);
      },
      { timeout: 4000 },
    );
    expect(holding()).toBe(false);
    // Nothing to resolve against, so nothing was redirected.
    expect(router.state.location.pathname).toBe(OLD);
  });

  it('draws on the first failure, not after the retries are spent', async () => {
    // The query client retries a 5xx twice, 1 s and then 2 s later, and the
    // query is `pending` throughout. Holding until then kept the page back for
    // about 3 s.
    const { reads } = clusterList(problem(500));
    mountApp(OLD);
    await waitFor(
      () => {
        expect(reads.length).toBeGreaterThan(0);
      },
      { timeout: 4000 },
    );
    expect(reads[0]?.at).toBeLessThan(1500);
  });
});

describe('a canonical address never waits on the cluster list', () => {
  it('draws the page while the list is still held back', async () => {
    const { reads } = clusterList('held');
    const { router } = mountApp(PODS);

    await waitFor(() => {
      expect(reads.length).toBeGreaterThan(0);
    });
    expect(reads[0]?.afterTheList).toBe(false);
    expect(holding()).toBe(false);
    expect(router.state.location.pathname).toBe(PODS);
  });
});
