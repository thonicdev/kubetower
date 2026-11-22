import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KEEP_MS, STALE_MS, type Counts, type Detail, type Overview, type Source, type Workload } from './data';
import { security } from './index';
import { securityShortcut } from './panel';
import { filterComponents, sbomQuery, verdictMark, type Component, type Sbom, type Signature } from './supply';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/**
 * The supply chain in Security Center's pages: the SBOM and signatures
 * sources, the two tiles and two columns of the workloads, and a workload's
 * SBOM tab. Every body is the shape of the Go route's answer.
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

function mount(path: string) {
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
const DETAIL = '/x/kind/security/workload/shop/Deployment/web';
const SBOM = `${DETAIL}/sbom`;
const NOW = '2026-11-13T12:00:00Z';
const c = (critical: number, high: number, medium: number, low: number): Counts => ({ critical, high, medium, low, unknown: 0 });
const src = (kind: Source['kind'], state: Source['state'], reports = 3, complete = true): Source => ({ kind, state, complete, reports });
const sig = (verdict: Signature['verdict'], ...images: string[]): Signature => ({ verdict, images: images.map((image) => ({ image, verdict })) });

function workload(name: string, over: Partial<Workload> = {}): Workload {
  return {
    namespace: 'shop',
    kind: 'Deployment',
    name,
    images: [`registry.example.com/${name}:1.0`],
    counts: c(0, 1, 1, 2),
    config: null,
    secrets: null,
    exposed: false,
    scanned: '2026-11-13T10:00:00Z',
    components: null,
    signature: null,
    ...over,
  };
}

const WORKLOADS = [
  workload('signed', { signature: sig('pass', 'registry.example.com/signed:1.0'), components: { total: 412, dependencies: 380, images: [{ container: 'app', image: 'registry.example.com/signed:1.0', components: 412, dependencies: 380 }] } }),
  workload('tampered', { signature: sig('fail', 'registry.example.com/tampered:1.0') }),
  workload('skipped', { signature: sig('skip', 'registry.example.com/skipped:1.0') }),
  workload('unknown'),
];

function overview(over: Partial<Overview> = {}): Overview {
  return {
    cluster: 'kind',
    read: NOW,
    operator: true,
    sources: [src('vulnerabilityreports', 'read'), src('configauditreports', 'read'), src('exposedsecretreports', 'read')],
    sbom: src('sbomreports', 'read', 1),
    signatures: src('signatures', 'read', 3),
    signatureFailed: 1,
    verified: 1,
    totals: c(0, 4, 4, 8),
    affected: { critical: 0, high: 4, medium: 4 },
    imageCount: 4,
    exposedCount: 0,
    secretCount: 0,
    namespaces: [{ namespace: 'shop', counts: c(0, 4, 4, 8), workloads: 4 }],
    images: [],
    workloads: WORKLOADS,
    total: WORKLOADS.length,
    capped: false,
    ...over,
  };
}

const rows = () => [...document.querySelectorAll('[data-workload-row]')].map((r) => r.getAttribute('data-workload-row')?.split('/')[2]);
const cell = (name: string, attr: string) => document.querySelector(`[data-workload-row="shop/Deployment/${name}"] [${attr}]`);

describe('the supply chain on the Security Center page', () => {
  it('names the SBOM and signatures sources with their states, — for no verdict at all', async () => {
    stub({ [PAGE]: { body: overview({ sbom: src('sbomreports', 'off', 0), signatures: src('signatures', 'absent', 0) }) } });
    mount('x/security');
    await screen.findByText('signed');
    const pill = (kind: string) => document.querySelector(`[data-source="${kind}"]`);
    expect(pill('sbomreports')?.getAttribute('data-state')).toBe('off');
    expect(pill('sbomreports')?.textContent).toBe('SBOMoff');
    expect(pill('signatures')?.getAttribute('data-state')).toBe('absent');
    expect(pill('signatures')?.textContent).toBe('Signatures—');
    // No verdict anywhere: no tile of the signatures, and never one of the unsigned.
    expect(document.querySelector('[data-tile="unsigned"]')).toBeNull();
    expect(document.querySelector('[data-tile="verified"]')).toBeNull();
  });

  it.each([
    ['denied', src('sbomreports', 'denied', 0)],
    ['unreadable', src('sbomreports', 'unreadable', 0)],
    ['partial', src('sbomreports', 'read', 2, false)],
  ] as const)('draws the SBOM source %s', async (state, sbom) => {
    stub({ [PAGE]: { body: overview({ sbom, signatures: src('signatures', state === 'partial' ? 'read' : state, 0, state !== 'partial') }) } });
    mount('x/security');
    await screen.findByText('signed');
    expect(document.querySelector('[data-source="sbomreports"]')?.getAttribute('data-state')).toBe(state);
    expect(document.querySelector('[data-source="signatures"]')?.getAttribute('data-state')).toBe(state);
  });

  it('draws Pass, Fail, Skipped and — in the Signature column, image by image in the title, and the component count', async () => {
    stub({ [PAGE]: { body: overview() } });
    mount('x/security');
    await screen.findByText('signed');
    expect(cell('signed', 'data-signature')?.textContent).toBe('Pass');
    expect(cell('signed', 'data-signature')?.getAttribute('title')).toBe('pass  registry.example.com/signed:1.0');
    expect(cell('tampered', 'data-signature')?.textContent).toBe('Fail');
    expect(cell('skipped', 'data-signature')?.textContent).toBe('Skipped');
    expect(cell('unknown', 'data-signature')?.textContent).toBe('—');
    expect(cell('unknown', 'data-signature')?.getAttribute('title')).toBe('No admission verdict');
    expect(cell('signed', 'data-component-count')?.textContent).toBe('412');
    expect(cell('signed', 'data-component-count')?.getAttribute('title')).toBe('380 dependencies\napp  412  registry.example.com/signed:1.0');
    expect(cell('tampered', 'data-component-count')?.textContent).toBe('—');
    expect(document.body.textContent).not.toMatch(/unsigned|not signed/i);
  });

  it('filters by Signature failed and Verified, one at a time, through the address', async () => {
    stub({ [PAGE]: { body: overview() } });
    const router = mount('x/security');
    await screen.findByText('signed');
    expect(document.querySelector('[data-tile="unsigned"]')?.getAttribute('data-count')).toBe('1');
    expect(document.querySelector('[data-tile="verified"]')?.getAttribute('data-count')).toBe('1');
    act(() => {
      fireEvent.click(document.querySelector('[data-tile="unsigned"]')!);
    });
    expect(rows()).toEqual(['tampered']);
    expect(router.state.location.search).toBe('?signature=fail');
    act(() => {
      fireEvent.click(document.querySelector('[data-tile="verified"]')!);
    });
    expect(rows()).toEqual(['signed']);
    expect(router.state.location.search).toBe('?signature=pass');
    act(() => {
      fireEvent.click(document.querySelector('[data-tile="verified"]')!);
    });
    expect(rows()).toHaveLength(4);
  });

  it('draws — for a workload of an answer without the supply chain', async () => {
    const plain: Workload[] = WORKLOADS.map((w) => {
      const out = { ...w };
      delete out.components;
      delete out.signature;
      return out;
    });
    const older = overview({ workloads: plain });
    delete older.sbom;
    delete older.signatures;
    delete older.signatureFailed;
    delete older.verified;
    stub({ [PAGE]: { body: older } });
    mount('x/security');
    await screen.findByText('signed');
    expect(cell('signed', 'data-signature')?.textContent).toBe('—');
    expect(cell('signed', 'data-component-count')?.textContent).toBe('—');
    expect(document.querySelector('[data-source="sbomreports"]')).toBeNull();
  });
});

/* -------------------------------------------------------------------------- */
/* The SBOM tab                                                                */
/* -------------------------------------------------------------------------- */

function detail(over: Partial<Detail> = {}): Detail {
  return {
    cluster: 'kind',
    read: NOW,
    workload: { namespace: 'shop', kind: 'Deployment', name: 'web' },
    sources: [src('vulnerabilityreports', 'read', 1)],
    containers: [],
    exposure: [],
    cves: [],
    cvesTotal: 0,
    checks: [],
    secrets: [],
    signature: sig('pass', 'registry.example.com/web:1.0'),
    ...over,
  };
}

const LICENSES = ['MIT', 'Apache-2.0', 'GPL-2.0-only', 'BSD-3-Clause', 'LGPL-2.1', 'ISC', 'MPL-2.0', 'Zlib', 'BSD-2-Clause', 'OpenSSL', 'Artistic-2.0', 'PSF-2.0'];

function component(i: number): Component {
  return {
    name: `lib-${String(i).padStart(5, '0')}`,
    version: `1.${String(i)}.0`,
    type: i % 10 === 0 ? 'operating-system' : 'library',
    license: i % 13 === 12 ? '' : LICENSES[i % LICENSES.length]!,
    purl: `pkg:deb/debian/lib-${String(i)}@1.${String(i)}.0`,
    container: i % 2 === 0 ? 'app' : 'proxy',
  };
}

function sbom(count: number, total = count, over: Partial<Sbom> = {}): Sbom {
  const components = Array.from({ length: count }, (_, i) => component(i));
  const tally = (key: (x: Component) => string) => {
    const m = new Map<string, number>();
    for (const x of components) m.set(key(x), (m.get(key(x)) ?? 0) + 1);
    return [...m].map(([name, n]) => ({ name, count: n })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  };
  return {
    cluster: 'kind',
    read: NOW,
    workload: { namespace: 'shop', kind: 'Deployment', name: 'web' },
    source: src('sbomreports', 'read', 2),
    images: [
      { container: 'app', image: 'registry.example.com/web:1.0', digest: `sha256:${'ab'.repeat(32)}`, components: Math.ceil(total / 2), dependencies: 10 },
      { container: 'proxy', image: 'nginx:1.27', digest: `sha256:${'cd'.repeat(32)}`, components: Math.floor(total / 2), dependencies: 5 },
    ],
    components,
    total,
    types: tally((x) => x.type),
    licenses: tally((x) => x.license),
    notes: [],
    ...over,
  };
}

const componentRows = () => document.querySelectorAll('[data-component]').length;

describe("a workload's SBOM tab", () => {
  it('reads with a minute of freshness, five minutes of memory and no interval', () => {
    const q = sbomQuery('kind', { namespace: 'shop', kind: 'Deployment', name: 'web' });
    expect(q.staleTime).toBe(STALE_MS);
    expect(q.gcTime).toBe(KEEP_MS);
    expect('refetchInterval' in q).toBe(false);
  });

  it('is not read while its tab is closed', async () => {
    const asked = stub({ [DETAIL]: { body: detail() }, [SBOM]: { body: sbom(10) } });
    mount('x/security/shop/Deployment/web');
    await waitFor(() => expect(document.querySelector('[data-tab="sbom"]')).not.toBeNull());
    expect(asked).not.toContain(SBOM);
    act(() => {
      fireEvent.click(document.querySelector('[data-tab="sbom"]')!);
    });
    await waitFor(() => expect(document.querySelector('[data-sbom]')).not.toBeNull());
    expect(asked.filter((u) => u === SBOM)).toHaveLength(1);
  });

  it('draws 5 000 components with 100 rows in the DOM, the cut, the images, the types and the ten licenses', async () => {
    stub({ [DETAIL]: { body: detail() }, [SBOM]: { body: sbom(5000, 6000) } });
    const started = performance.now();
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(componentRows()).toBe(100));
    expect(performance.now() - started).toBeLessThan(3000);
    expect(document.querySelector('[data-cut]')?.textContent).toBe('5,000 / 6,000');
    expect(document.querySelector('[data-range]')?.textContent).toBe('1–100 / 5,000');
    expect(document.querySelector('[data-sbom-image="app"]')?.textContent).toContain(`sha256:${'ab'.repeat(32)}`);
    expect(document.querySelectorAll('[data-licenses] [data-license]')).toHaveLength(10);
    expect(document.querySelector('[data-type="library"]')?.textContent).toBe('library4,500');
    expect(document.querySelector('[data-signature]')?.textContent).toBe('Signature Pass');
    expect(document.querySelector('[data-component="lib-00000"]')?.getAttribute('title')).toBe('pkg:deb/debian/lib-0@1.0.0');
  });

  it('filters by text and by a license clicked, back on the first page', async () => {
    stub({ [DETAIL]: { body: detail() }, [SBOM]: { body: sbom(5000) } });
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(componentRows()).toBe(100));
    act(() => {
      fireEvent.click(document.querySelector('[data-next]')!);
    });
    expect(document.querySelector('[data-range]')?.textContent).toBe('101–200 / 5,000');
    act(() => {
      fireEvent.click(document.querySelector('[data-license="MIT"]')!);
    });
    const mit = filterComponents(sbom(5000).components, { q: '', license: 'MIT' }).length;
    expect(document.querySelector('[data-range]')?.textContent).toBe(`1–100 / ${mit.toLocaleString('en-US')}`);
    expect([...document.querySelectorAll('[data-component] td:nth-child(4)')].every((td) => td.textContent === 'MIT')).toBe(true);
    // Pressed again, the license is released.
    act(() => {
      fireEvent.click(document.querySelector('[data-license="MIT"]')!);
    });
    expect(document.querySelector('[data-range]')?.textContent).toBe('1–100 / 5,000');
    fireEvent.change(screen.getByLabelText('Filter components'), { target: { value: 'lib-04999' } });
    await waitFor(() => expect(componentRows()).toBe(1));
  });

  it.each([
    ['off', src('sbomreports', 'off', 0), 'SBOM off'],
    ['absent', src('sbomreports', 'absent', 0), 'SBOM absent'],
    ['denied', src('sbomreports', 'denied', 0), 'denied'],
    ['unreadable', src('sbomreports', 'unreadable', 0), 'unreadable'],
  ] as const)('a source %s says so, never an empty list', async (state, source, word) => {
    stub({ [DETAIL]: { body: detail({ signature: null }) }, [SBOM]: { body: sbom(0, 0, { source, images: [] }) } });
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(document.querySelector('[data-sbom-state]')).not.toBeNull());
    expect(document.querySelector('[data-sbom-state]')?.getAttribute('data-sbom-state')).toBe(state);
    expect(document.querySelector('[data-sbom-state]')?.textContent).toBe(word);
    expect(document.querySelector('[data-components]')).toBeNull();
  });

  it('draws — for a workload with no SBOM report, and no None', async () => {
    const images = [{ container: 'app', image: 'busybox:1.36', components: 0, dependencies: 0, absent: true }];
    stub({ [DETAIL]: { body: detail() }, [SBOM]: { body: sbom(0, 0, { images }) } });
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(document.querySelector('[data-sbom]')).not.toBeNull());
    expect(document.querySelector('[data-total]')?.textContent).toBe('— components');
    expect(document.querySelector('[data-total]')?.getAttribute('data-total')).toBe('absent');
    expect(document.querySelector('[data-sbom-image="app"] [data-absent]')?.textContent).toBe('—');
    expect(document.querySelector('[data-none]')).toBeNull();
    expect(document.querySelector('[data-components]')).toBeNull();
  });

  it('keeps 0 components and None for a report that lists none', async () => {
    const images = [{ container: 'app', image: 'scratch:1', components: 0, dependencies: 0 }];
    stub({ [DETAIL]: { body: detail() }, [SBOM]: { body: sbom(0, 0, { images }) } });
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(document.querySelector('[data-sbom]')).not.toBeNull());
    expect(document.querySelector('[data-total]')?.textContent).toBe('0 components');
    expect(document.querySelector('[data-sbom-image="app"]')?.textContent).toContain('0');
    expect(document.querySelector('[data-absent]')).toBeNull();
    expect(document.querySelector('[data-none]')?.textContent).toBe('None');
  });

  it('draws — in the row of a container without a report, and one count for the image that has one', async () => {
    const images = [
      { container: 'app', image: 'nginx:1.27-alpine', components: 69, dependencies: 30 },
      { container: 'init', image: 'busybox:1.36', components: 0, dependencies: 0, absent: true },
    ];
    stub({ [DETAIL]: { body: detail() }, [SBOM]: { body: sbom(69, 69, { images }) } });
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(componentRows()).toBe(69));
    expect(document.querySelector('[data-total]')?.textContent).toBe('69 components');
    expect(document.querySelector('[data-sbom-image="app"] td:last-child')?.textContent).toBe('69');
    expect(document.querySelector('[data-sbom-image="init"] [data-absent]')?.textContent).toBe('—');
    expect(document.querySelector('[data-range]')).toBeNull();
  });

  it('says — for a workload the admission said nothing about', async () => {
    stub({ [DETAIL]: { body: detail({ signature: null }) }, [SBOM]: { body: sbom(3) } });
    mount('x/security/shop/Deployment/web?tab=sbom');
    await waitFor(() => expect(componentRows()).toBe(3));
    expect(document.querySelector('[data-signature]')?.textContent).toBe('Signature —');
    expect(document.querySelector('[data-signature]')?.getAttribute('title')).toBe('No admission verdict');
  });
});

describe('the verdict on the panel tile', () => {
  const BRIEF = '/x/prod/security/brief/shop/web?kind=deployments';
  const object = { cluster: 'prod', kind: 'deployments', namespace: 'shop', name: 'web' };
  const brief = { scanned: true, workload: { namespace: 'shop', kind: 'Deployment', name: 'web' }, counts: c(0, 1, 0, 0), href: '/c/prod/x/security/shop/Deployment/web' };
  it.each([
    ['pass', ['Trivy', '✓']],
    ['fail', ['Trivy', '✗']],
    ['skip', ['Trivy']],
    [undefined, ['Trivy']],
  ] as const)('%s draws %j', async (verdict, badges) => {
    stub({ [BRIEF]: { body: verdict === undefined ? brief : { ...brief, signature: sig(verdict, 'x:1') } } });
    const tile = await securityShortcut(object, new AbortController().signal);
    expect(tile?.badges).toEqual(badges);
  });

  it('marks — for no verdict', () => {
    expect(verdictMark(null)).toBe('—');
    expect(verdictMark(undefined)).toBe('—');
  });
});
