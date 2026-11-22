import { QueryClient } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  categoryIn,
  categoryOf,
  KEEP_MS,
  PENDING_POLL_MS,
  pageQuery,
  pagePath,
  shownSuggestions,
  viewOf,
  windowAge,
  windowPath,
  workloadTabQuery,
  type Group,
  type Page,
  type Row,
  type WorkloadTab,
} from './data';

/**
 * What the page derives from the scope's one answer: the view, the category
 * filter and the command block, which the route used to build for each view
 * and each filter. The Go test `TestFilteringTheGroupsDrawsWhatFilteringTheRowsDrew`
 * holds the same equivalence on the server's side.
 */

function row(resource: string, kind: Row['kind'], kubectl = ''): Row {
  return {
    workload: 'api',
    workloadKind: 'Deployment',
    namespace: 'shop',
    pod: 'api-1',
    podCount: 1,
    container: 'app',
    resource,
    kind,
    action: 'Reduce request',
    message: '',
    current: '',
    suggested: '',
    appliesToBoth: false,
    warnings: [],
    kubectl,
    scope: 'Deployment api · shop',
    confidence: '',
    insufficientData: false,
  };
}

const MEM = 'kubectl set resources deployment/api -c app --limits=memory=1Gi -n shop';
const CPU = 'kubectl set resources deployment/api -c app --requests=cpu=100m -n shop';

// The server's groups, in its order: by severity, then by resource rank.
const GROUPS: Group[] = [
  { kind: 'danger', label: 'critical', mark: '▲', rows: [row('Memory', 'danger', MEM)] },
  { kind: 'warning', label: 'warning', mark: '●', rows: [row('CPU — no limit', 'warning'), row('Ephemeral', 'warning')] },
  {
    kind: 'overkill',
    label: 'over-provisioned',
    mark: '▼',
    rows: [row('CPU', 'overkill', CPU), row('Memory — no limit', 'overkill', MEM), row('PVC', 'overkill'), row('EmptyDir', 'overkill')],
  },
];
const ANSWER = { groups: GROUPS, commands: [MEM, CPU] };

const drawn = (groups: Group[]) => groups.map((g) => `${g.kind}:${g.rows.map((r) => r.resource).join(',')}`);

describe('what the page draws from one answer', () => {
  it('maps every resource the server names onto its filter, as view.go does', () => {
    const want: Record<string, string> = {
      CPU: 'cpu',
      'CPU — no limit': 'cpu',
      'CPU — no request': 'cpu',
      Memory: 'memory',
      'Memory — no limit': 'memory',
      'Memory — no request': 'memory',
      Ephemeral: 'storage',
      'Ephemeral — no limit': 'storage',
      PVC: 'storage',
      EmptyDir: 'storage',
      GPU: '',
    };
    for (const [resource, cat] of Object.entries(want)) expect(categoryOf(resource), resource).toBe(cat);
  });

  it('keeps a filtered list in the server order, leaves empty groups out, and carries each command once', () => {
    // What the route answered for ?cat=… before the filter moved here.
    expect(shownSuggestions(ANSWER, 'cpu')).toEqual({
      groups: [
        { kind: 'warning', label: 'warning', mark: '●', rows: [row('CPU — no limit', 'warning')] },
        { kind: 'overkill', label: 'over-provisioned', mark: '▼', rows: [row('CPU', 'overkill', CPU)] },
      ],
      commands: [CPU],
    });
    const memory = shownSuggestions(ANSWER, 'memory');
    expect(drawn(memory.groups)).toEqual(['danger:Memory', 'overkill:Memory — no limit']);
    // One command from two findings: once, where it is first drawn.
    expect(memory.commands).toEqual([MEM]);
    const storage = shownSuggestions(ANSWER, 'storage');
    expect(drawn(storage.groups)).toEqual(['warning:Ephemeral', 'overkill:PVC,EmptyDir']);
    expect(storage.commands).toEqual([]);
  });

  it('draws everything as answered with no filter, and with one it does not know', () => {
    expect(shownSuggestions(ANSWER, '')).toEqual(ANSWER);
    expect(shownSuggestions(ANSWER, categoryIn('cpus'))).toEqual(ANSWER);
    expect(categoryIn('memory')).toBe('memory');
  });

  it('draws a namespace answer as that namespace, and the cluster one as the view asked for', () => {
    expect(viewOf({ namespace: 'shop' }, 'nodes')).toBe('namespace');
    expect(viewOf({ namespace: '' }, 'nodes')).toBe('nodes');
    expect(viewOf({ namespace: '' }, '')).toBe('overview');
    expect(viewOf({ namespace: '' }, 'bogus')).toBe('overview');
  });

  it('asks the route for the scope alone', () => {
    expect(pagePath('docker-desktop', '')).toBe('/x/docker-desktop/kubeadjust/');
    expect(pagePath('docker-desktop', 'shop')).toBe('/x/docker-desktop/kubeadjust/?ns=shop');
  });
});

describe('a page whose window is still being read', () => {
  type Poll = (query: { state: { data: { window: string } | undefined } }) => number | false;
  const poll = (window: string | undefined): number | false =>
    (pageQuery('kind', '').refetchInterval as unknown as Poll)({ state: { data: window === undefined ? undefined : { window } } });

  it('asks again while pending, and stops once read', () => {
    expect(poll('pending')).toBe(PENDING_POLL_MS);
    expect(poll('read')).toBe(false);
    expect(poll('no-prometheus')).toBe(false);
    expect(poll(undefined)).toBe(false);
  });

  it('keeps the last answer long enough to draw it on coming back', () => {
    expect(pageQuery('kind', '').gcTime).toBe(KEEP_MS);
    expect(workloadTabQuery('kind', 'deployments', 'shop', 'api').gcTime).toBe(KEEP_MS);
  });
});

describe('a poll while the window is pending', () => {
  afterEach(() => vi.unstubAllGlobals());

  const PAGE = '/x/kind/kubeadjust/?ns=shop';
  const WINDOW = '/x/kind/kubeadjust/window?ns=shop';
  const WORKLOAD = '/x/kind/kubeadjust/workload/Deployment/shop/api';

  function stub(replies: Record<string, unknown>): string[] {
    const asked: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        asked.push(url);
        const reply = replies[url];
        if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
        return Promise.resolve(new Response(JSON.stringify(reply), { status: 200, headers: { 'Content-Type': 'application/json' } }));
      }),
    );
    return asked;
  }

  const answer = (window: string): Page => ({ notes: [], ready: true, namespace: 'shop', window }) as unknown as Page;

  it('asks only where the window stands, and keeps the last answer while it is still pending', async () => {
    expect(windowPath('kind', 'shop')).toBe(WINDOW);
    expect(windowPath('kind', '')).toBe('/x/kind/kubeadjust/window');
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const query = pageQuery('kind', 'shop');
    const last = answer('pending');
    client.setQueryData(query.queryKey, last);
    const asked = stub({ [WINDOW]: { window: 'pending' }, [PAGE]: answer('read') });
    for (let i = 0; i < 3; i++) await client.fetchQuery({ ...query, staleTime: 0 });
    expect(asked).toEqual([WINDOW, WINDOW, WINDOW]);
    expect(client.getQueryData(query.queryKey)).toBe(last);
  });

  it('reads the page again once the window is no longer pending', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const query = pageQuery('kind', 'shop');
    client.setQueryData(query.queryKey, answer('pending'));
    const asked = stub({ [WINDOW]: { window: 'read' }, [PAGE]: answer('read') });
    const got = await client.fetchQuery({ ...query, staleTime: 0 });
    expect(asked).toEqual([WINDOW, PAGE]);
    expect(got.window).toBe('read');
  });

  it('reads the page when the state cannot be read, and on a first display', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const query = pageQuery('kind', 'shop');
    let asked = stub({ [PAGE]: answer('pending') });
    await client.fetchQuery(query);
    expect(asked).toEqual([PAGE]);
    asked = stub({ [PAGE]: answer('read') });
    await client.fetchQuery({ ...query, staleTime: 0 });
    expect(asked).toEqual([WINDOW, PAGE]);
  });

  it('polls the workload tab the same way, on its namespace', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const query = workloadTabQuery('kind', 'Deployment', 'shop', 'api');
    const last = { notes: [], commands: [], window: 'pending' } as unknown as WorkloadTab;
    client.setQueryData(query.queryKey, last);
    const asked = stub({ [WINDOW]: { window: 'pending' }, [WORKLOAD]: { notes: [], commands: [], window: 'read' } });
    expect(await client.fetchQuery({ ...query, staleTime: 0 })).toBe(last);
    expect(asked).toEqual([WINDOW]);
  });
});

describe('the age of a history', () => {
  const now = Date.parse('2026-11-10T12:00:00Z');
  it('is said in words once it is a minute old, in the largest whole unit', () => {
    expect(windowAge('2026-11-10T11:48:00Z', now)).toBe('History read 12 minutes ago');
    expect(windowAge('2026-11-10T11:59:00Z', now)).toBe('History read 1 minute ago');
    expect(windowAge('2026-11-10T10:30:00Z', now)).toBe('History read 1 hour ago');
    expect(windowAge('2026-11-08T11:00:00Z', now)).toBe('History read 2 days ago');
  });
  it('is not said for a history just read, or whose time is unknown', () => {
    expect(windowAge('2026-11-10T11:59:30Z', now)).toBe('');
    expect(windowAge('2026-11-10T12:00:30Z', now)).toBe('');
    expect(windowAge(undefined, now)).toBe('');
    expect(windowAge('', now)).toBe('');
    expect(windowAge('not a time', now)).toBe('');
  });
});
