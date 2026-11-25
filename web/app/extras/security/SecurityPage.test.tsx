import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KEEP_MS, PAGE_ROWS, STALE_MS, detailQuery, filterWorkloads, overviewQuery, type Counts, type Detail, type Overview, type Workload } from './data';
import { security } from './index';
import { withinCeiling } from '../../../ui/src/testing/ceiling';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

/**
 * Security Center's pages mounted the way the console mounts them, with a
 * fetch stub standing in for `extras/security`. Every body is the shape of
 * the route's contract.
 */
type Replies = Record<string, { status?: number; body: unknown }>;

function stub(replies: Replies): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(path = 'x/security') {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: security.routes }], { initialEntries: [`/c/kind/${path}`] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const PAGE = '/x/kind/security/';
const NOW = '2026-11-11T12:00:00Z';
const c = (critical: number, high: number, medium: number, low: number): Counts => ({ critical, high, medium, low, unknown: 0 });

function workload(name: string, over: Partial<Workload> = {}): Workload {
  return {
    namespace: 'shop',
    kind: 'Deployment',
    name,
    images: [`registry.example.com/${name}:1.0`],
    counts: c(0, 0, 1, 2),
    config: c(0, 0, 1, 0),
    secrets: 0,
    exposed: false,
    scanned: '2026-11-11T10:00:00Z',
    ...over,
  };
}

const READ = [
  { kind: 'vulnerabilityreports', state: 'read', complete: true, reports: 3 },
  { kind: 'configauditreports', state: 'read', complete: true, reports: 3 },
  { kind: 'exposedsecretreports', state: 'read', complete: true, reports: 3 },
] as const;

function overview(workloads: Workload[], over: Partial<Overview> = {}): Overview {
  return {
    cluster: 'kind',
    read: NOW,
    operator: true,
    sources: READ.map((s) => ({ ...s })),
    totals: c(5, 12, 30, 40),
    affected: { critical: 2, high: 3, medium: 4 },
    imageCount: 3,
    exposedCount: 1,
    secretCount: 1,
    namespaces: [
      { namespace: 'shop', counts: c(5, 10, 20, 30), workloads: 4 },
      { namespace: 'data', counts: c(0, 2, 10, 10), workloads: 1 },
    ],
    images: [
      { image: 'nginx:1.21.6', counts: c(4, 10, 10, 10), workloads: 2 },
      { image: 'postgres:13.4', counts: c(1, 2, 3, 4), workloads: 1 },
    ],
    workloads,
    total: workloads.length,
    capped: false,
    ...over,
  };
}

const FIVE = [
  workload('checkout', { counts: c(3, 5, 4, 1), images: ['nginx:1.21.6', 'registry.example.com/checkout:1.0'], exposed: true, exposure: 'Ingress shop/checkout', secrets: 1 }),
  workload('storefront', { counts: c(1, 2, 0, 0), images: ['nginx:1.21.6'] }),
  workload('orders-db', { namespace: 'data', kind: 'StatefulSet', counts: c(0, 2, 3, 4), images: ['postgres:13.4'] }),
  workload('mailer', { counts: c(0, 0, 0, 0) }),
  workload('debug', { kind: 'Pod', counts: c(0, 0, 0, 0), config: null, secrets: null }),
];
// The last one is not covered by any vulnerability report.
delete FIVE[4]!.scanned;

const rows = () => [...document.querySelectorAll('[data-workload-row]')].map((r) => r.getAttribute('data-workload-row'));
const search = (router: ReturnType<typeof mount>) => router.state.location.search;

describe('the Security Center page', () => {
  it('reads once with a minute of freshness, five minutes of memory and no interval', () => {
    const page = overviewQuery('kind', '');
    expect(page.staleTime).toBe(STALE_MS);
    expect(STALE_MS).toBe(60_000);
    expect(page.gcTime).toBe(KEEP_MS);
    expect(KEEP_MS).toBe(300_000);
    expect('refetchInterval' in page).toBe(false);
    const detail = detailQuery('kind', { namespace: 'shop', kind: 'Deployment', name: 'web' });
    expect(detail.staleTime).toBe(STALE_MS);
    expect('refetchInterval' in detail).toBe(false);
  });

  it('draws one short state and nothing else when trivy-operator is absent', async () => {
    stub({
      [PAGE]: {
        body: overview([], {
          operator: false,
          sources: [
            { kind: 'vulnerabilityreports', state: 'absent', complete: true, reports: 0 },
            { kind: 'configauditreports', state: 'absent', complete: true, reports: 0 },
            { kind: 'exposedsecretreports', state: 'absent', complete: true, reports: 0 },
          ],
        }),
      },
    });
    mount();
    expect((await screen.findByText('trivy-operator absent')).getAttribute('data-operator')).toBe('absent');
    expect(document.querySelector('[data-tiles]')).toBeNull();
    expect(document.querySelector('[data-sources]')).toBeNull();
    expect(document.querySelector('[data-workloads]')).toBeNull();
    expect(document.querySelector('[data-namespaces]')).toBeNull();
  });

  describe('installing trivy-operator through the console', () => {
    const ABSENT = overview([], {
      operator: false,
      sources: [
        { kind: 'vulnerabilityreports', state: 'absent', complete: true, reports: 0 },
        { kind: 'configauditreports', state: 'absent', complete: true, reports: 0 },
        { kind: 'exposedsecretreports', state: 'absent', complete: true, reports: 0 },
      ],
    });
    const PREREQ = '/api/c/kind/prereq/trivy-operator';
    const CHART = { repo: 'https://aquasecurity.github.io/helm-charts', name: 'trivy-operator', version: '0.38.1', appVersion: '0.36.1', namespace: 'trivy-system' };
    const YAML = 'apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: trivy-operator\n  namespace: argocd\nspec:\n  source:\n    targetRevision: 0.38.1\n';
    const found = (over: Record<string, unknown> = {}) => ({
      state: 'ok',
      data: {
        key: 'trivy-operator',
        label: 'Trivy Operator',
        chart: CHART,
        argo: { state: 'found', namespace: 'argocd' },
        application: { state: 'absent', name: 'trivy-operator', namespace: 'argocd' },
        installable: true,
        ...over,
      },
    });
    const PLAN = {
      token: 'tok-1',
      expiresAt: '2026-11-11T12:02:00Z',
      sentence: 'Creates the Argo CD Application trivy-operator in argocd, which installs trivy-operator 0.38.1 into trivy-system.',
      facts: {
        verb: 'install',
        targets: [{ kind: 'applications.argoproj.io', namespace: 'argocd', name: 'trivy-operator' }],
        count: 1,
        destructive: false,
        headline: 'Creates the Argo CD Application trivy-operator in argocd, which installs trivy-operator 0.38.1 into trivy-system.',
        button: 'Install trivy-operator 0.38.1',
        request: YAML,
        confirmation: { kind: 'name', expected: 'trivy-operator', label: 'Type trivy-operator to install it' },
      },
    };

    /** The page's routes and the console's three, with every write's body kept. */
    function stubInstall(prereq: unknown): { asked: string[]; posted: Record<string, unknown> } {
      const asked: string[] = [];
      const posted: Record<string, unknown> = {};
      const replies: Record<string, unknown> = {
        [PAGE]: ABSENT,
        [PREREQ]: prereq,
        [`${PREREQ}/preview`]: PLAN,
        [`${PREREQ}/install`]: { succeeded: 1, failed: 0, results: [], summary: 'Created Argo CD Application trivy-operator in argocd.' },
      };
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
          const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
          asked.push(`${init?.method ?? 'GET'} ${url}`);
          if (typeof init?.body === 'string') posted[url] = JSON.parse(init.body);
          const reply = replies[url];
          if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
          return Promise.resolve(new Response(JSON.stringify(reply), { headers: { 'Content-Type': 'application/json' } }));
        }),
      );
      return { asked, posted };
    }

    it('offers Install beside the absent pill, and the dialog shows the chart, the version and the YAML sent', async () => {
      const { asked, posted } = stubInstall(found());
      mount();
      const button = await waitFor(() => {
        const b = document.querySelector<HTMLButtonElement>('[data-install="offer"]');
        expect(b).not.toBeNull();
        return b!;
      });
      expect(button.disabled).toBe(false);
      expect(button.title).toContain('trivy-operator 0.38.1');
      fireEvent.click(button);

      const yaml = await waitFor(() => {
        const pre = document.querySelector('[data-install-yaml]');
        expect(pre).not.toBeNull();
        return pre!;
      });
      expect(yaml.textContent).toBe(YAML);
      const chart = document.querySelector('[data-install-chart]')!.textContent ?? '';
      expect(chart).toContain('trivy-operator');
      expect(chart).toContain('0.38.1');
      expect(chart).toContain('0.36.1');
      expect(asked).toContain(`POST ${PREREQ}/preview`);

      // The server's gesture: the commit stays off until the name is typed.
      const commit = screen.getByRole('button', { name: 'Install trivy-operator 0.38.1' });
      expect((commit as HTMLButtonElement).disabled).toBe(true);
      fireEvent.change(screen.getByLabelText('Type trivy-operator to install it'), { target: { value: 'trivy-operator' } });
      expect((commit as HTMLButtonElement).disabled).toBe(false);
      fireEvent.click(commit);

      await waitFor(() => expect(posted[`${PREREQ}/install`]).toEqual({ token: 'tok-1', confirmed: 'trivy-operator' }));
      // The outcome is reported in the dialog, beside a face that already says so.
      expect((await screen.findByText('installing')).getAttribute('data-install')).toBe('installing');
      fireEvent.click((await screen.findAllByRole('button', { name: 'Close' })).at(-1)!);
      await waitFor(() => expect(document.querySelector('[data-install-yaml]')).toBeNull());
      expect((await screen.findByText('installing')).getAttribute('data-install')).toBe('installing');
      // Nothing was written by the page's own routes.
      expect(asked.filter((a) => a.startsWith('POST') && a.includes('/x/'))).toEqual([]);
    });

    it('draws the helm command, copyable, where there is no Argo CD to install through', async () => {
      const helm = { release: 'trivy-operator', repo: CHART.repo, chart: 'trivy-operator', version: '0.38.1', namespace: 'trivy-system' };
      stubInstall(found({ argo: { state: 'absent', note: 'not served' }, application: undefined, installable: false, helm, reason: { code: 'not_installed', message: 'Argo CD is not installed' } }));
      mount();
      const command = await waitFor(() => {
        const code = document.querySelector('[data-helm-command]');
        expect(code).not.toBeNull();
        return code!;
      });
      expect(command.textContent).toBe(
        'helm upgrade --install trivy-operator trivy-operator --repo https://aquasecurity.github.io/helm-charts --version 0.38.1 --namespace trivy-system --create-namespace',
      );
      expect(screen.getByRole('button', { name: 'Copy' })).not.toBeNull();
      expect(document.querySelector('[data-install="offer"]')).toBeNull();
    });

    it('draws Install disabled with the reason in its title where the person may not install', async () => {
      stubInstall(found({ installable: false, reason: { code: 'verb_denied', message: 'kind is marked read-only in KT_READONLY_CLUSTERS' } }));
      mount();
      const button = await waitFor(() => {
        const b = document.querySelector<HTMLButtonElement>('[data-install="blocked"]');
        expect(b).not.toBeNull();
        return b!;
      });
      expect(button.disabled).toBe(true);
      expect(button.title).toContain('read-only');
    });

    it('says installing, and offers nothing, where the Application exists', async () => {
      stubInstall(found({ installable: false, application: { state: 'present', name: 'trivy-operator', namespace: 'argocd' } }));
      mount();
      expect((await screen.findByText('installing')).getAttribute('data-install')).toBe('installing');
      expect(document.querySelector('[data-install="offer"]')).toBeNull();
    });

    it('asks nothing about an install where trivy-operator is there', async () => {
      const asked = stub({ [PAGE]: { body: overview(FIVE) } });
      mount();
      await screen.findByText('Workloads');
      expect(asked.some((a) => a.includes('/prereq/'))).toBe(false);
      expect(document.querySelector('[data-install]')).toBeNull();
    });
  });

  it('names each source with its state, partial where the read was narrowed', async () => {
    stub({
      [PAGE]: {
        body: overview(FIVE, {
          sources: [
            { kind: 'vulnerabilityreports', state: 'read', complete: false, note: 'narrowed to 2 namespaces', reports: 3 },
            { kind: 'configauditreports', state: 'denied', complete: false, note: 'you may not list them', reports: 0 },
            { kind: 'exposedsecretreports', state: 'unreadable', complete: false, reports: 0 },
          ],
        }),
      },
    });
    mount();
    await screen.findByText('checkout');
    const state = (kind: string) => document.querySelector(`[data-source="${kind}"]`);
    expect(state('vulnerabilityreports')?.getAttribute('data-state')).toBe('partial');
    expect(state('vulnerabilityreports')?.textContent).toBe('Vulnerabilitiespartial');
    expect(state('configauditreports')?.textContent).toBe('Configdenied');
    expect(state('vulnerabilityreports')?.getAttribute('title')).toContain('narrowed to 2 namespaces');
    expect(state('configauditreports')?.getAttribute('data-state')).toBe('denied');
    expect(state('exposedsecretreports')?.getAttribute('data-state')).toBe('unreadable');
  });

  it('a refused or failed read is drawn as a failure, never as an empty page', async () => {
    stub({ [PAGE]: { status: 502, body: { code: 'list_failed', message: "trivy-operator's reports could not be listed." } } });
    mount();
    expect((await screen.findByRole('alert')).textContent).toContain('could not be listed');
    expect(document.querySelector('[data-workloads]')).toBeNull();
    expect(document.querySelector('[data-none]')).toBeNull();
  });

  it('counts the tiles from the totals and the affected workloads', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    mount();
    await screen.findByText('checkout');
    const tile = (t: string) => document.querySelector(`[data-tile="${t}"]`);
    expect(tile('critical')?.getAttribute('data-count')).toBe('5');
    expect(tile('critical')?.querySelector('[data-legend]')?.textContent).toBe('2 workloads');
    expect(tile('high')?.getAttribute('data-count')).toBe('12');
    expect(tile('medium')?.getAttribute('data-count')).toBe('30');
    expect(tile('images')?.getAttribute('data-count')).toBe('3');
    expect(tile('exposed')?.getAttribute('data-count')).toBe('1');
    expect(tile('secrets')?.getAttribute('data-count')).toBe('1');
  });

  it('filters by one tile at a time, writes it in the address, and a second press releases it', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    const router = mount();
    await screen.findByText('checkout');
    expect(rows()).toHaveLength(5);

    fireEvent.click(document.querySelector('[data-tile="critical"]')!);
    expect(search(router)).toBe('?sev=critical');
    expect(rows()).toEqual(['shop/Deployment/checkout', 'shop/Deployment/storefront']);

    fireEvent.click(document.querySelector('[data-tile="exposed"]')!);
    expect(search(router)).toBe('?exposed=1');
    expect(rows()).toEqual(['shop/Deployment/checkout']);
    expect(document.querySelector('[data-tile="critical"]')?.getAttribute('aria-pressed')).toBe('false');

    fireEvent.click(document.querySelector('[data-tile="secrets"]')!);
    expect(search(router)).toBe('?secrets=1');
    expect(rows()).toEqual(['shop/Deployment/checkout']);

    fireEvent.click(document.querySelector('[data-tile="high"]')!);
    expect(rows()).toEqual(['shop/Deployment/checkout', 'shop/Deployment/storefront', 'data/StatefulSet/orders-db']);

    fireEvent.click(document.querySelector('[data-tile="high"]')!);
    expect(search(router)).toBe('');
    expect(rows()).toHaveLength(5);
  });

  it('opens on the filters the address carries, and an image bar filters by image', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    const router = mount('x/security?sev=high&image=nginx%3A1.21.6');
    await screen.findByText('checkout');
    expect(rows()).toEqual(['shop/Deployment/checkout', 'shop/Deployment/storefront']);

    fireEvent.click(document.querySelector('[data-image="nginx:1.21.6"]')!);
    expect(new URLSearchParams(search(router)).get('image')).toBeNull();
    expect(rows()).toHaveLength(3);

    fireEvent.click(document.querySelector('[data-image="postgres:13.4"]')!);
    expect(new URLSearchParams(search(router)).get('image')).toBe('postgres:13.4');
    expect(rows()).toEqual(['data/StatefulSet/orders-db']);
  });

  it('a namespace bar scopes the read itself, through the address', async () => {
    const asked = stub({ [PAGE]: { body: overview(FIVE) }, [`${PAGE}?ns=data`]: { body: overview([FIVE[2]!]) } });
    const router = mount();
    await screen.findByText('checkout');
    fireEvent.click(document.querySelector('[data-ns="data"]')!);
    expect(search(router)).toBe('?ns=data');
    await waitFor(() => expect(rows()).toEqual(['data/StatefulSet/orders-db']));
    expect(asked).toContain(`${PAGE}?ns=data`);
  });

  it('filters by text once typing settles, and keeps the text in the address', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    const router = mount();
    await screen.findByText('checkout');
    fireEvent.change(screen.getByLabelText('Filter workloads'), { target: { value: 'postgres' } });
    expect(search(router)).toBe('?q=postgres');
    await waitFor(() => expect(rows()).toEqual(['data/StatefulSet/orders-db']));
  });

  it('never draws a workload no report covers as clean', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    mount();
    await screen.findByText('debug');
    const debug = document.querySelector('[data-workload-row="shop/Pod/debug"]')!;
    expect(debug.querySelector('[data-unscanned]')?.textContent).toBe('Not scanned');
    expect(debug.querySelector('[data-clean]')).toBeNull();
    expect(debug.textContent).not.toContain('Clean');
    const mailer = document.querySelector('[data-workload-row="shop/Deployment/mailer"]')!;
    expect(mailer.querySelector('[data-clean]')?.textContent).toBe('Clean');
  });

  it('counts each failed check once, read for the rows on screen alone', async () => {
    const CONFIG = `${PAGE}config?w=shop%2FDeployment%2Fcheckout&w=shop%2FDeployment%2Fstorefront&w=data%2FStatefulSet%2Forders-db&w=shop%2FDeployment%2Fmailer`;
    const asked = stub({
      [PAGE]: { body: overview(FIVE.map((w) => (w.name === 'checkout' ? { ...w, config: c(0, 3, 0, 0) } : w))) },
      [CONFIG]: {
        body: {
          'shop/Deployment/checkout': c(0, 2, 0, 0),
          'shop/Deployment/storefront': c(0, 0, 1, 0),
          'data/StatefulSet/orders-db': null,
          'shop/Deployment/mailer': c(0, 0, 0, 0),
        },
      },
    });
    mount();
    await screen.findByText('checkout');
    const cell = (key: string) => document.querySelector(`[data-workload-row="${key}"] td:nth-child(6)`);
    await waitFor(() => expect(cell('shop/Deployment/checkout')?.querySelector('[data-config]')?.textContent).toBe('2'));
    expect(cell('shop/Deployment/checkout')?.querySelector('[data-config]')?.className).toContain('text-bad');
    expect(cell('shop/Deployment/storefront')?.textContent).toBe('1');
    expect(cell('data/StatefulSet/orders-db')?.querySelector('[data-config-unread]')).not.toBeNull();
    expect(cell('shop/Deployment/mailer')?.querySelector('[data-config]')?.className).toContain('text-ok');
    expect(cell('shop/Pod/debug')?.textContent).toBe('—');
    expect(asked.filter((u) => u.includes('/config?'))).toEqual([CONFIG]);
  });

  it('asks /config for the page shown, never for every row', async () => {
    const many = Array.from({ length: PAGE_ROWS + 7 }, (_, i) => workload(`w${String(i).padStart(3, '0')}`));
    const asked = stub({ [PAGE]: { body: overview(many) } });
    mount();
    await screen.findByText('w000');
    await waitFor(() => expect(asked.some((u) => u.includes('/config?'))).toBe(true));
    const config = asked.find((u) => u.includes('/config?'))!;
    expect(new URLSearchParams(config.slice(config.indexOf('?'))).getAll('w')).toHaveLength(PAGE_ROWS);
  });

  it('drops the namespace, the image and the scan age by the table\'s own width, the workload title keeping them', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    mount();
    await screen.findByText('checkout');
    const head = (label: string) => [...document.querySelectorAll('[data-workloads] thead th')].find((th) => th.textContent === label)!;
    expect(document.querySelector('[data-workloads]')?.parentElement?.className).toContain('@container');
    expect(head('Namespace').className).toContain('hidden @5xl:table-cell');
    expect(head('Scan').className).toContain('hidden @5xl:table-cell');
    expect(head('Image').className).toContain('hidden @7xl:table-cell');
    expect(head('Workload').className).not.toContain('hidden');
    const link = document.querySelector('[data-workload-row="shop/Deployment/checkout"] a');
    expect(link?.getAttribute('title')).toBe('shop/Deployment/checkout\nnginx:1.21.6\nregistry.example.com/checkout:1.0');
  });

  it('a row opens the workload', async () => {
    stub({ [PAGE]: { body: overview(FIVE) } });
    const router = mount();
    await screen.findByText('checkout');
    fireEvent.click(document.querySelector('[data-workload-row="data/StatefulSet/orders-db"] td')!);
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/kind/x/security/data/StatefulSet/orders-db'));
  });

  it('pages 100 rows at a time, and a filter goes back to the first page', async () => {
    const many = Array.from({ length: 250 }, (_, i) => workload(`w-${String(i).padStart(3, '0')}`, { counts: c(i % 2, 1, 0, 0) }));
    stub({ [PAGE]: { body: overview(many) } });
    mount();
    await screen.findByText('w-000');
    expect(rows()).toHaveLength(PAGE_ROWS);
    expect(document.querySelector('[data-range]')?.textContent).toBe('1–100 / 250');
    fireEvent.click(document.querySelector('[data-next]')!);
    fireEvent.click(document.querySelector('[data-next]')!);
    expect(rows()).toHaveLength(50);
    expect(rows()[0]).toBe('shop/Deployment/w-200');
    expect(document.querySelector('[data-next]')?.hasAttribute('disabled')).toBe(true);

    fireEvent.click(document.querySelector('[data-tile="critical"]')!);
    expect(rows()).toHaveLength(100);
    expect(rows()[0]).toBe('shop/Deployment/w-001');
    expect(document.querySelector('[data-range]')?.textContent).toBe('1–100 / 125');
  });

  it('draws 5 000 workloads with 100 rows in the DOM, under a time ceiling', async () => {
    const many = Array.from({ length: 5000 }, (_, i) =>
      workload(`w-${String(i)}`, { namespace: `ns-${String(i % 40)}`, counts: c(i % 3, i % 5, i % 7, i % 11), images: [`img-${String(i % 300)}:1`] }),
    );
    stub({ [PAGE]: { body: overview(many, { total: 5000 }) } });
    const started = performance.now();
    mount();
    await screen.findByText('w-0');
    const drawn = performance.now() - started;
    expect(document.querySelectorAll('[data-workload-row]')).toHaveLength(100);
    expect(document.querySelectorAll('tr')).toHaveLength(101);
    withinCeiling(drawn, 3000);

    // A filter over the whole set redraws the first page, and stays as light.
    const again = performance.now();
    act(() => {
      fireEvent.click(document.querySelector('[data-tile="critical"]')!);
    });
    withinCeiling(performance.now() - again, 1000);
    expect(document.querySelectorAll('[data-workload-row]')).toHaveLength(100);
  });

  it('filters by text over every field it shows', () => {
    const f = (q: string) => filterWorkloads(FIVE, { tile: '', image: '', q }).map((w) => w.name);
    expect(f('STATEFUL')).toEqual(['orders-db']);
    expect(f('data')).toEqual(['orders-db']);
    expect(f('nginx')).toEqual(['checkout', 'storefront']);
    expect(f('  ')).toHaveLength(5);
  });
});

/* -------------------------------------------------------------------------- */
/* One workload                                                                */
/* -------------------------------------------------------------------------- */

const DETAIL = '/x/kind/security/workload/shop/Deployment/checkout';

function detail(over: Partial<Detail> = {}): Detail {
  const cves = Array.from({ length: 130 }, (_, i) => ({
    id: `CVE-2024-${String(1000 + i)}`,
    severity: (['CRITICAL', 'HIGH', 'MEDIUM', 'LOW'] as const)[Math.min(3, Math.floor(i / 10))]!,
    score: 7.5,
    pkg: 'openssl',
    installed: '1.1.1n',
    fixed: i % 2 === 0 ? '1.1.1w' : '',
    title: `issue ${String(i)}`,
    link: i === 0 ? 'https://avd.aquasec.com/nvd/cve-2024-1000' : 'javascript:alert(1)',
    container: 'api',
  }));
  return {
    cluster: 'kind',
    read: NOW,
    workload: { namespace: 'shop', kind: 'Deployment', name: 'checkout' },
    sources: READ.map((s) => ({ ...s })),
    containers: [
      { container: 'api', image: 'registry.example.com/checkout:1.0', os: 'debian 11.6', scanned: '2026-11-11T10:00:00Z', counts: c(10, 10, 10, 100) },
      { container: 'proxy', image: 'nginx:1.21.6', os: 'debian 10.12', eosl: true, scanned: '2026-11-11T10:00:00Z', counts: c(0, 0, 0, 0) },
    ],
    exposure: ['Ingress shop/checkout'],
    cves,
    cvesTotal: 2400,
    checks: [{ id: 'KSV014', title: 'Root file system is not read-only', severity: 'HIGH', category: 'Kubernetes Security Check' }],
    secrets: [{ rule: 'aws-access-key-id', title: 'AWS Access Key ID', category: 'AWS', severity: 'CRITICAL', target: '/app/.env' }],
    ...over,
  };
}

const cveRows = () => document.querySelectorAll('[data-cve-row]').length;

describe('a workload of Security Center', () => {
  it('draws its containers, OS, EOSL and exposure, then the CVEs 100 at a time', async () => {
    stub({ [DETAIL]: { body: detail() } });
    mount('x/security/shop/Deployment/checkout');
    await screen.findByText('registry.example.com/checkout:1.0');
    expect(document.querySelector('[data-container="proxy"] [data-eosl]')?.textContent).toBe('EOSL');
    expect(document.querySelector('[data-container="proxy"] [data-clean]')).not.toBeNull();
    expect(document.querySelector('[data-exposure]')?.textContent).toBe('Ingress shop/checkout');
    expect(cveRows()).toBe(100);
    expect(document.querySelector('[data-cut]')?.textContent).toBe('130 / 2,400');
    expect(document.querySelector('[data-range]')?.textContent).toBe('1–100 / 130');
    // Only an http(s) link leaves the console.
    expect(document.querySelector('[data-cve-row="CVE-2024-1000"] a')?.getAttribute('href')).toBe('https://avd.aquasec.com/nvd/cve-2024-1000');
    expect(document.querySelector('[data-cve-row="CVE-2024-1001"] a')).toBeNull();
  });

  it('filters the CVEs by severity and by fixable, back on the first page', async () => {
    stub({ [DETAIL]: { body: detail() } });
    mount('x/security/shop/Deployment/checkout');
    await screen.findByText('registry.example.com/checkout:1.0');
    fireEvent.click(document.querySelector('[data-next]')!);
    fireEvent.click(document.querySelector('[data-sev-filter="critical"]')!);
    expect(cveRows()).toBe(10);
    expect(document.querySelector('[data-pager]')).toBeNull();
    fireEvent.click(document.querySelector('[data-fixable]')!);
    expect(cveRows()).toBe(5);
    fireEvent.click(document.querySelector('[data-sev-filter="critical"]')!);
    expect(cveRows()).toBe(65);
    expect(document.querySelector('[data-range]')).toBeNull();
  });

  it('draws the failed checks and the secrets in their tabs, never a match', async () => {
    stub({ [DETAIL]: { body: detail() } });
    const router = mount('x/security/shop/Deployment/checkout');
    await screen.findByText('registry.example.com/checkout:1.0');
    fireEvent.click(document.querySelector('[data-tab="config"]')!);
    expect(search(router)).toBe('?tab=config');
    expect(document.querySelector('[data-check="KSV014"]')?.textContent).toContain('Root file system is not read-only');
    fireEvent.click(document.querySelector('[data-tab="secrets"]')!);
    const secret = document.querySelector('[data-secret="aws-access-key-id"]');
    expect(secret?.textContent).toContain('/app/.env');
    expect(secret?.textContent).toContain('AWS');
  });

  it('a tab whose source was refused says so, never an empty list', async () => {
    stub({
      [DETAIL]: {
        body: detail({
          sources: [
            { kind: 'vulnerabilityreports', state: 'read', complete: true, reports: 1 },
            { kind: 'configauditreports', state: 'denied', complete: false, reports: 0 },
            { kind: 'exposedsecretreports', state: 'absent', complete: true, reports: 0 },
          ],
          checks: [],
          secrets: [],
        }),
      },
    });
    mount('x/security/shop/Deployment/checkout?tab=config');
    await waitFor(() => expect(document.querySelector('[data-unread]')?.textContent).toBe('denied'));
    expect(document.querySelector('[data-none]')).toBeNull();
  });

  it('a workload no report covers reads not scanned, never clean', async () => {
    stub({
      [DETAIL]: {
        body: detail({
          containers: [{ container: 'api', image: 'busybox:1.35', counts: c(0, 0, 0, 0) }],
          cves: [],
          cvesTotal: 0,
        }),
      },
    });
    mount('x/security/shop/Deployment/checkout');
    await screen.findByText('busybox:1.35');
    expect(document.querySelector('[data-container="api"] [data-unscanned]')?.textContent).toBe('not scanned');
    expect(document.querySelector('[data-clean]')).toBeNull();
    expect(document.querySelector('[data-none]')?.textContent).toBe('not scanned');
  });
});
