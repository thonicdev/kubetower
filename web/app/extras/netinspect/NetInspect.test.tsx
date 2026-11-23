import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Flows, Graph, NamespaceView, Overview, PodRow, PolicyRow, Verdict } from './data';
import { netinspect } from './index';

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The page, mounted the way the console mounts it, with a fetch stub standing
 * in for `extras/netinspect`. Every body is the shape the Go route writes
 * (`view.go`, `verdict.go`); the Go tests hold what the server says, these
 * hold what the page draws of it.
 */

const BASE = '/x/kind/netinspect/';

type Reply = Overview | Verdict | Flows | { status: number; body: string; contentType?: string };

function stub(replies: Record<string, Reply>): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const reply = replies[url];
    if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    if ('status' in reply && 'body' in reply && typeof reply.body === 'string') {
      return Promise.resolve(new Response(reply.body, { status: reply.status, headers: { 'Content-Type': reply.contentType ?? 'application/json' } }));
    }
    return Promise.resolve(new Response(JSON.stringify(reply), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(search = ''): ReturnType<typeof createMemoryRouter> {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: netinspect.routes }], {
    initialEntries: [`/c/kind/x/netinspect${search}`],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

function pod(i: number, over: Partial<PodRow> = {}): PodRow {
  return { namespace: 'shop', name: `api-${String(i)}`, workload: 'api', ip: `10.0.0.${String(i % 250)}`, in: true, out: false, hostNetwork: false, policies: 1, ...over };
}

function policy(i: number, over: Partial<PolicyRow> = {}): PolicyRow {
  return { kind: 'NetworkPolicy', namespace: 'shop', name: `p-${String(i)}`, selector: 'app=api', types: ['Ingress'], selects: 1, ingress: ['from pods app=web in this namespace, on http'], egress: [], broken: [], ...over };
}

const GRAPH: Graph = {
  scope: 'cluster',
  nodes: [
    { id: 'ns:shop', label: 'shop', kind: 'namespace', namespace: 'shop', pods: 3, isolatedIn: 3, isolatedOut: 1, internal: 2 },
    { id: 'ns:ops', label: 'ops', kind: 'namespace', namespace: 'ops', pods: 1, isolatedIn: 0, isolatedOut: 0, internal: 0 },
  ],
  edges: [{ kind: 'allow', from: 'ns:ops', to: 'ns:shop', side: 'ingress', ports: ['9090/TCP'], policies: ['api-from-ops'], rules: 1 }],
  folded: 0,
  edgesTotal: 1,
};

function overview(over: Partial<Overview> = {}): Overview {
  return {
    mode: 'rules',
    namespace: '',
    complete: true,
    notes: [],
    cni: { name: 'kindnet', enforces: 'yes', detail: 'Recognized from its DaemonSet: kindnet applies NetworkPolicies.' },
    unevaluated: [],
    summary: { pods: 4, isolatedIn: 3, isolatedOut: 1, open: 1, hostNetwork: 0, policies: 3, defaultDeny: 1, idle: 1 },
    namespaces: [
      { name: 'ops', pods: 1, isolatedIn: 0, isolatedOut: 0, policies: 0, defaultDenyIn: false, defaultDenyOut: false },
      { name: 'shop', pods: 3, isolatedIn: 3, isolatedOut: 1, policies: 3, defaultDenyIn: true, defaultDenyOut: false },
    ],
    namespacesTotal: 2,
    policies: [policy(1), policy(2, { name: 'legacy-db', selector: 'app=db', selects: 0 })],
    policiesTotal: 2,
    pods: { total: 2, offset: 0, rows: [pod(1), pod(2, { name: 'web', in: false })] },
    graph: GRAPH,
    cilium: { served: false, policies: 0, hostPolicies: 0, notEvaluated: [] },
    ...over,
  };
}

describe('the Network Inspector page', () => {
  it('draws every namespace against every namespace, and a heading opens one', async () => {
    stub({ [BASE]: overview(), [`${BASE}?ns=ops`]: overview({ namespace: 'ops' }) });
    const router = mount();
    await waitFor(() => expect(document.querySelector('[data-matrix]')).not.toBeNull());
    expect(document.querySelector('[data-mode="rules"]')?.textContent).toBe('Rules');
    const cell = (key: string) => document.querySelector(`[data-cell="${key}"]`);
    // ops may reach shop by a rule; shop's own pods by theirs; shop is isolated, so
    // nothing else gets in; ops is open.
    expect(cell('ns:ops>ns:shop')?.getAttribute('data-state')).toBe('allowed');
    expect(cell('ns:ops>ns:shop')?.getAttribute('title')).toContain('api-from-ops');
    expect(cell('ns:shop>ns:shop')?.getAttribute('data-state')).toBe('inside');
    expect(cell('ns:shop>ns:ops')?.getAttribute('data-state')).toBe('open');
    expect(cell('ns:ops>ns:ops')?.getAttribute('data-state')).toBe('open');
    expect(document.querySelector('[data-graph]')).toBeNull();
    fireEvent.click(document.querySelector('tbody [data-heading="ops"]') as Element);
    await waitFor(() => expect(router.state.location.search).toBe('?ns=ops'));
  });

  it('names the policy engines it evaluates beside the title', async () => {
    stub({ [BASE]: overview() });
    mount();
    await waitFor(() => expect(document.querySelector('[data-matrix]')).not.toBeNull());
    const engines = [...document.querySelectorAll('[data-engines] [data-engine]')].map((chip) => chip.textContent);
    expect(engines).toEqual(['Kubernetes NetworkPolicy', 'Cilium', 'Calico']);
  });

  it('draws a Calico tier policy, an AdminNetworkPolicy and a pass rule in the policies list by their kinds', async () => {
    stub({
      [BASE]: overview({
        policies: [
          policy(1, { kind: 'Calico GlobalNetworkPolicy', namespace: '', name: 'deny-web', ingress: ['deny from endpoints app == "web", on every port', 'pass from everyone, on every port'] }),
          policy(2, { kind: 'AdminNetworkPolicy', namespace: '', name: 'guard' }),
          policy(3, { kind: 'BaselineAdminNetworkPolicy', namespace: '', name: 'default' }),
        ],
        policiesTotal: 3,
      }),
    });
    mount('?list=policies');
    await waitFor(() => expect(document.querySelector('[data-policy="/deny-web"]')).not.toBeNull());
    const kind = (key: string) => document.querySelector(`[data-policy="${key}"] [title]`);
    expect(kind('/deny-web')?.getAttribute('title')).toBe('Calico GlobalNetworkPolicy');
    expect(kind('/deny-web')?.textContent).toBe('Calico GNP');
    expect(kind('/guard')?.textContent).toBe('ANP');
    expect(kind('/default')?.getAttribute('title')).toBe('BaselineAdminNetworkPolicy');
  });

  it('says refused where a namespace is isolated and no rule lets the other in', async () => {
    const graph: Graph = { ...GRAPH, edges: [] };
    stub({ [BASE]: overview({ graph }) });
    mount();
    await waitFor(() => expect(document.querySelector('[data-matrix]')).not.toBeNull());
    expect(document.querySelector('[data-cell="ns:ops>ns:shop"]')?.getAttribute('data-state')).toBe('refused');
  });

  it('filters the pods from a tile, and the namespaces and policies from theirs', async () => {
    const fetch = stub({
      [BASE]: overview(),
      [`${BASE}?show=in`]: overview({ pods: { total: 1, offset: 0, rows: [pod(1)] } }),
    });
    const router = mount();
    fireEvent.click(await screen.findByRole('button', { name: /Isolated in/ }));
    await waitFor(() => expect(router.state.location.search).toBe('?show=in'));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(`${BASE}?show=in`, expect.anything()));
    await waitFor(() => expect(document.querySelectorAll('[data-pod]')).toHaveLength(1));

    fireEvent.click(screen.getByRole('button', { name: /Policies selecting nothing/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-policy]')).toHaveLength(1));
    expect(document.querySelector('[data-policy="shop/legacy-db"]')).not.toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /Default-deny namespaces/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-namespace]')).toHaveLength(1));
  });

  it('moves from one namespace to another without going back through All namespaces', async () => {
    const shopOnly = { namespaces: [overview().namespaces[1]!], namespacesTotal: 1, choices: ['ops', 'shop'] };
    const fetch = stub({
      [`${BASE}?ns=shop`]: overview({ namespace: 'shop', ...shopOnly }),
      [`${BASE}?ns=ops`]: overview({ namespace: 'ops', namespaces: [overview().namespaces[0]!], namespacesTotal: 1, choices: ['ops', 'shop'] }),
    });
    const router = mount('?ns=shop');
    const menu = (await waitFor(() => {
      const found = document.querySelector('[data-namespace-menu]');
      expect(found).not.toBeNull();
      expect(found?.querySelector('option[value="ops"]')).not.toBeNull();
      return found;
    })) as HTMLSelectElement;
    expect([...menu.options].map((o) => o.value)).toEqual(['', 'ops', 'shop']);
    fireEvent.change(menu, { target: { value: 'ops' } });
    await waitFor(() => expect(router.state.location.search).toBe('?ns=ops'));
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(`${BASE}?ns=ops`, expect.anything()));
  });

  it("draws one namespace as the pod tab does: from where and which label in, to where out, and each workload", async () => {
    const view: NamespaceView = {
      ingress: {
        open: [],
        openPods: 0,
        lanes: [
          {
            namespace: 'shop', name: 'api-from-ops', kind: 'allow', rule: 1, text: 'from pods app=probe in namespace ops, on 9090/TCP',
            peers: [{ kind: 'pods', label: 'pods app=probe in namespace ops', pods: 1, namespaces: ['ops'] }], ports: ['9090/TCP'], targets: ['api'], pods: 1,
          },
          {
            namespace: 'shop', name: 'block-legacy', kind: 'deny', rule: 1, text: 'from 10.9.0.0/16',
            peers: [{ kind: 'cidr', label: '10.9.0.0/16', pods: 0, namespaces: [] }], ports: ['every port'], targets: ['api'], pods: 1,
          },
        ],
        lanesTotal: 2,
        closed: ['web'],
      },
      egress: { open: ['api', 'web'], openPods: 3, lanes: [], lanesTotal: 0, closed: [] },
      workloads: [
        { name: 'api', pods: 1, isolatedIn: 1, isolatedOut: 0, hostNetwork: 0, from: ['pods app=probe in namespace ops'], to: [] },
        { name: 'web', pods: 2, isolatedIn: 2, isolatedOut: 0, hostNetwork: 0, from: [], to: [] },
      ],
      workloadsTotal: 2,
    };
    const flows: Flows = {
      mode: 'observed', source: 'hubble', state: 'read', window: 300, read: 3, narrowed: false, truncated: false, forwarded: 30, dropped: 2, rows: [],
      edges: [
        { from: 'ns:ops', to: 'wl:shop/api', forwarded: 30, dropped: 2, ports: ['9090/TCP'], deniedBy: [] },
        { from: 'wl:shop/web', to: 'wl:shop/api', forwarded: 9, dropped: 0, ports: ['80/TCP'], deniedBy: [] },
      ],
    };
    stub({ [`${BASE}?ns=shop`]: overview({ namespace: 'shop', view }), [`${BASE}flows?ns=shop`]: flows });
    const router = mount('?ns=shop');
    await waitFor(() => expect(document.querySelector('[data-namespace-view="shop"]')).not.toBeNull());
    expect(document.querySelector('[data-graph]')).toBeNull();
    // The picture: the peers in by where and which label, a deny in the refusal color, anyone out.
    expect(document.querySelector('[data-peer-in="ops · app=probe"]')?.getAttribute('data-tone')).toBe('allow');
    expect(document.querySelector('[data-peer-in="10.9.0.0/16"]')?.getAttribute('data-tone')).toBe('deny');
    expect(document.querySelector('[data-peer-out="anyone"]')?.getAttribute('data-tone')).toBe('open');
    // The sides as lists.
    expect(document.querySelector('[data-side="ingress"] [data-lane="api-from-ops/1"]')?.textContent).toContain('ops · app=probe');
    expect(document.querySelector('[data-side="ingress"] [data-lane="closed"]')?.textContent).toBe('nothing · web');
    expect(document.querySelector('[data-side="egress"] [data-lane="open"]')?.textContent).toContain('api, web');
    // What Hubble saw cross into the namespace, by where it came from; a flow inside is not counted.
    await waitFor(() => expect(document.querySelector('[data-seen="ingress"]')?.textContent).toBe('seenops 30 · 2'));
    // Each workload; its name filters the pods below.
    const api = document.querySelector('[data-workload="api"]');
    expect(api?.textContent).toBe('api1isolatedops · app=probeopenanyone');
    expect(document.querySelector('[data-workload="web"]')?.textContent).toBe('web2isolatednothingopenanyone');
    fireEvent.click(screen.getByRole('button', { name: 'api' }));
    await waitFor(() => expect(router.state.location.search).toBe('?ns=shop&q=api'));
  });

  it('tells a cluster without rules from rules it may not read, and from a read that failed', async () => {
    stub({ [BASE]: overview({ summary: { pods: 4, isolatedIn: 0, isolatedOut: 0, open: 4, hostNetwork: 0, policies: 0, defaultDeny: 0, idle: 0 }, policies: [], policiesTotal: 0 }) });
    mount();
    await waitFor(() => expect(document.querySelector('[data-empty]')).not.toBeNull());
  });

  it('says a refusal as one', async () => {
    stub({ [BASE]: { status: 403, body: JSON.stringify({ code: 'list_forbidden', message: 'The cluster says you may not list its NetworkPolicies, so which rules apply cannot be shown. This is not a cluster without rules.' }) } });
    mount();
    await waitFor(() => expect(document.querySelector('[data-refused]')?.getAttribute('title')).toContain('This is not a cluster without rules'));
  });

  it('says a failed read as a failure', async () => {
    stub({ [BASE]: { status: 502, body: JSON.stringify({ code: 'list_failed', message: "This cluster's NetworkPolicies could not be read." }) } });
    mount();
    expect((await screen.findByRole('alert')).textContent).toMatch(/could not be read/);
    expect(document.querySelector('[data-refused]')).toBeNull();
  });

  it('warns where the rules are not applied, and where other policies are not evaluated', async () => {
    stub({
      [BASE]: overview({
        cni: { name: 'flannel', enforces: 'no', detail: 'flannel does not apply NetworkPolicies on its own.' },
        unevaluated: [{ kind: 'CiliumNetworkPolicy', group: 'cilium.io', count: 'at least 1' }],
        complete: false,
        notes: ['Pods: the listing stopped before its end. What is drawn covers only what was read.'],
      }),
    });
    mount();
    await waitFor(() => expect(document.querySelector('[data-cni="no"]')?.textContent).toContain('flannel'));
    expect(document.querySelector('[data-unevaluated]')?.textContent).toContain('CiliumNetworkPolicy');
    expect(document.querySelector('[data-partial]')?.getAttribute('title')).toContain('stopped before its end');
  });

  it('asks the verdict from the address and draws both sides', async () => {
    const verdict: Verdict = {
      mode: 'rules',
      answer: 'no',
      summary: 'The rules refuse it: the destination may not receive it.',
      from: 'shop/stranger',
      to: 'shop/api',
      port: 80,
      protocol: 'TCP',
      egress: { direction: 'egress', pod: 'shop/stranger', state: 'open', isolating: [], reason: 'No NetworkPolicy selects this pod for egress.', caveats: [] },
      ingress: { direction: 'ingress', pod: 'shop/api', state: 'denied', isolating: [{ namespace: 'shop', name: 'default-deny-ingress' }], reason: 'Isolated for ingress by default-deny-ingress.', caveats: [] },
      complete: true,
      notes: [],
      cni: overview().cni,
      unevaluated: [],
      policy: '',
    };
    stub({ [BASE]: overview(), [`${BASE}verdict?from=shop%2Fstranger&to=shop%2Fapi&port=80`]: verdict });
    mount('?from=shop/stranger&to=shop/api&port=80');
    await waitFor(() => expect(document.querySelector('[data-verdict="no"]')).not.toBeNull());
    expect(document.querySelector('[data-side="egress"]')?.getAttribute('data-state')).toBe('open');
    expect(document.querySelector('[data-side="ingress"]')?.getAttribute('data-state')).toBe('denied');
  });

  it('answers a Service pod by pod and offers a policy to copy', async () => {
    const side = (direction: 'egress' | 'ingress', pod: string, state: 'open' | 'denied') => ({
      direction,
      pod,
      state,
      isolating: [],
      reason: '',
      caveats: [],
    });
    const backend = (pod: string, port: number) => ({
      pod,
      port,
      answer: 'no' as const,
      egress: side('egress', 'shop/web', 'open'),
      ingress: side('ingress', pod, 'denied'),
    });
    const verdict: Verdict = {
      mode: 'rules',
      answer: 'no',
      summary: 'The rules refuse it.',
      from: 'shop/web',
      to: 'shop/api',
      port: 80,
      protocol: 'TCP',
      egress: side('egress', 'shop/web', 'open'),
      ingress: side('ingress', 'shop/api', 'denied'),
      complete: true,
      notes: [],
      cni: overview().cni,
      unevaluated: [],
      policy: 'apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\n',
      service: {
        name: 'shop/api',
        port: 80,
        protocol: 'TCP',
        targetPort: 'http',
        selector: 'app=api',
        backends: [backend('shop/api-1', 80), backend('shop/api-2', 8080)],
        total: 2,
        capped: false,
      },
    };
    stub({ [BASE]: overview(), [`${BASE}verdict?from=shop%2Fweb&to=svc%3Ashop%2Fapi&port=80`]: verdict });
    mount('?from=shop/web&to=svc:shop/api&port=80');
    await waitFor(() => expect(document.querySelectorAll('[data-backend]')).toHaveLength(2));
    expect(document.querySelector('[data-backend="shop/api-2"]')?.textContent).toContain('8080');
    expect(document.querySelector('[data-side]')).toBeNull();
    expect(document.querySelector('[data-policy-copy] pre')?.textContent).toContain('kind: NetworkPolicy');
    expect(document.querySelector('[data-backends-capped]')).toBeNull();
  });
});

/**
 * Bounded rendering on a large cluster: whatever the answer holds, the page
 * draws at most a page of pods and a capped table of policies, and reads once
 * - no loop of re-renders asking again.
 */
describe('on a cluster of 5 000 pods and 1 000 policies', () => {
  it('draws bounded tables and reads once', async () => {
    const rows = Array.from({ length: 5000 }, (_, i) => pod(i));
    const policies = Array.from({ length: 1000 }, (_, i) => policy(i));
    const nodes = Array.from({ length: 40 }, (_, i) => ({ id: `ns:n${String(i)}`, label: `n${String(i)}`, kind: 'namespace' as const, namespace: `n${String(i)}`, pods: 125, isolatedIn: 60, isolatedOut: 20, internal: 1 }));
    const edges = nodes.flatMap((a, i) => nodes.filter((_, j) => j !== i && (i + j) % 3 === 0).map((b) => ({ kind: 'allow' as const, from: a.id, to: b.id, side: 'ingress' as const, ports: ['8080/TCP'], policies: ['p'], rules: 2 })));
    const big = overview({
      summary: { pods: 5000, isolatedIn: 2400, isolatedOut: 800, open: 2600, hostNetwork: 0, policies: 1000, defaultDeny: 100, idle: 0 },
      pods: { total: 5000, offset: 0, rows },
      policies,
      policiesTotal: 1000,
      graph: { scope: 'cluster', nodes, edges, folded: 60, edgesTotal: edges.length },
    });
    const fetch = stub({ [BASE]: big });
    const started = performance.now();
    mount();
    await waitFor(() => expect(document.querySelector('[data-matrix]')).not.toBeNull());
    await waitFor(() => expect(document.querySelectorAll('[data-pod]').length).toBeGreaterThan(0));
    expect(document.querySelectorAll('[data-pod]').length).toBeLessThanOrEqual(300);
    // The 24 busiest namespaces and one "others" holding the 16 left.
    expect(document.querySelectorAll('[data-cell]')).toHaveLength(25 * 25);
    expect(document.querySelector('[data-matrix-count]')?.textContent).toBe('24 / 40');

    const tab = document.querySelector('[data-list-tab="policies"]');
    if (tab === null) throw new Error('no Policies tab');
    fireEvent.click(tab);
    await waitFor(() => expect(document.querySelectorAll('[data-policy]').length).toBeGreaterThan(0));
    expect(document.querySelectorAll('[data-policy]').length).toBeLessThanOrEqual(200);
    expect(performance.now() - started).toBeLessThan(10_000);
    // One read: switching tables is drawn from the answer already here.
    expect(fetch.mock.calls.filter(([url]) => String(url).startsWith(BASE) && !String(url).includes('/flows') && !String(url).includes('/uis'))).toHaveLength(1);
  }, 30_000);
});

describe('Cilium on the page', () => {
  it('draws a deny apart from an allow, and a rule it does not evaluate as neither', async () => {
    const graph: Graph = {
      ...GRAPH,
      nodes: [...GRAPH.nodes, { id: 'unevaluated:toFQDNs', label: 'toFQDNs', kind: 'unevaluated', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 }],
      edges: [
        ...GRAPH.edges,
        { kind: 'deny', from: 'ns:ops', to: 'ns:shop', side: 'ingress', ports: ['5432/TCP'], policies: ['db-deny'], rules: 1 },
        { kind: 'unevaluated', from: 'ns:shop', to: 'unevaluated:toFQDNs', side: 'egress', ports: ['443/TCP'], policies: ['to-forge'], rules: 1 },
      ],
    };
    stub({
      [BASE]: overview({ graph, cilium: { served: true, policies: 3, hostPolicies: 0, notEvaluated: ['toFQDNs: 1'] } }),
    });
    mount();
    await waitFor(() => expect(document.querySelector('[data-matrix]')).not.toBeNull());
    // A deny outweighs an allow on the same pair; a rule not evaluated is neither.
    const deny = document.querySelector('[data-cell="ns:ops>ns:shop"]');
    expect(deny?.getAttribute('data-state')).toBe('denied');
    expect(deny?.getAttribute('title')).toContain('deny 5432/TCP · db-deny');
    expect(deny?.getAttribute('title')).toContain('allow 9090/TCP · api-from-ops');
    const unevaluated = document.querySelector('[data-cell="ns:shop>unevaluated:toFQDNs"]');
    expect(unevaluated?.getAttribute('data-state')).toBe('unevaluated');
    expect(unevaluated?.getAttribute('title')).toContain('not evaluated');
    expect(document.querySelector('[data-cilium-unevaluated]')?.textContent).toBe('Not evaluated: toFQDNs: 1');
  });

  it('answers a verdict resting on a rule it does not evaluate as not evaluated', async () => {
    const verdict: Verdict = {
      mode: 'rules',
      answer: 'unevaluated',
      summary: 'A rule that could allow it is not evaluated here.',
      from: 'shop/web',
      to: '198.51.100.7',
      port: 443,
      protocol: 'TCP',
      egress: {
        direction: 'egress',
        pod: 'shop/web',
        state: 'unevaluated',
        isolating: [{ namespace: '', name: 'baseline' }],
        decided: { namespace: 'shop', name: 'to-forge', kind: 'unevaluated', rule: 1, text: 'toFQDNs, on 443/TCP' },
        reason: '',
        caveats: [],
      },
      ingress: { direction: 'ingress', pod: '198.51.100.7', state: 'outside', isolating: [], reason: '', caveats: [] },
      complete: true,
      notes: [],
      cni: overview().cni,
      unevaluated: [],
      policy: '',
    };
    stub({ [BASE]: overview(), [`${BASE}verdict?from=shop%2Fweb&to=198.51.100.7&port=443`]: verdict });
    mount('?from=shop/web&to=198.51.100.7&port=443');
    await waitFor(() => expect(document.querySelector('[data-verdict="unevaluated"]')?.textContent).toContain('Not evaluated'));
    expect(document.querySelector('[data-side="egress"]')?.getAttribute('data-state')).toBe('unevaluated');
  });
});

describe('observed traffic', () => {
  const flows = (over: Partial<Flows> = {}): Flows => ({
    mode: 'observed',
    source: 'hubble',
    state: 'read',
    window: 300,
    read: 3,
    narrowed: false,
    truncated: false,
    forwarded: 2,
    dropped: 1,
    rows: [
      { from: 'shop/web', to: 'shop/api', port: 80, protocol: 'TCP', verdict: 'FORWARDED', deniedBy: [], count: 2, last: '2026-11-11T09:00:00Z' },
      { from: 'ops/probe', to: 'shop/api', port: 80, protocol: 'TCP', verdict: 'DROPPED', reason: 'POLICY_DENIED', deniedBy: ['shop/default-deny'], count: 1, last: '2026-11-11T09:00:00Z' },
    ],
    edges: [
      { from: 'ns:ops', to: 'ns:shop', forwarded: 0, dropped: 1, ports: ['80/TCP'], deniedBy: ['shop/default-deny'] },
      { from: 'ns:shop', to: 'entity:world', forwarded: 2, dropped: 0, ports: ['443/TCP'], deniedBy: [] },
    ],
    ...over,
  });

  it('marks what Hubble saw in each cell beside the rules, and lists the flows', async () => {
    const fetch = stub({ [BASE]: overview(), [`${BASE}flows`]: flows() });
    mount();
    await waitFor(() => expect(document.querySelector('[data-flows="read"]')?.textContent).toContain('3 · 5 min'));
    await waitFor(() => expect(document.querySelector('[data-cell="ns:ops>ns:shop"]')?.getAttribute('data-seen')).toBe('dropped'));
    expect(document.querySelector('[data-cell="ns:ops>ns:shop"]')?.getAttribute('title')).toContain('seen 0 forwarded, 1 dropped');
    // The world, which no rule names here, gets a row and a column.
    expect(document.querySelector('[data-cell="ns:shop>entity:world"]')?.getAttribute('data-seen')).toBe('forwarded');
    const post = fetch.mock.calls.find(([url]) => String(url).endsWith('/flows'));
    expect((post?.[1] as RequestInit | undefined)?.method).toBe('POST');
    const tab = document.querySelector('[data-list-tab="flows"]');
    if (tab === null) throw new Error('no Flows tab');
    fireEvent.click(tab);
    await waitFor(() => expect(document.querySelectorAll('[data-flow-row]')).toHaveLength(2));
    // Each layer can be hidden.
    fireEvent.click(document.querySelector('[data-flows]') as Element);
    await waitFor(() => expect(document.querySelector('[data-seen]')).toBeNull());
    expect(document.querySelector('[data-cell="ns:ops>ns:shop"]')?.getAttribute('data-state')).toBe('allowed');
  });

  it('draws a bounded table of 10 000 flow rows', async () => {
    const rows = Array.from({ length: 10_000 }, (_, i) => ({
      from: `shop/web-${String(i)}`,
      to: 'shop/api',
      port: 80,
      protocol: 'TCP',
      verdict: i % 2 === 0 ? 'FORWARDED' : 'DROPPED',
      deniedBy: [],
      count: 1,
      last: '2026-11-11T09:00:00Z',
    }));
    stub({ [BASE]: overview(), [`${BASE}flows`]: flows({ rows, read: 10_000, truncated: true }) });
    mount('?list=flows');
    await waitFor(() => expect(document.querySelectorAll('[data-flow-row]').length).toBeGreaterThan(0));
    expect(document.querySelectorAll('[data-flow-row]').length).toBeLessThanOrEqual(200);
  }, 30_000);

  it('says why there are no flows, apart from none', async () => {
    stub({ [BASE]: overview(), [`${BASE}flows`]: flows({ state: 'absent', detail: 'No kube-system/hubble-relay Service.', rows: [], edges: [], read: 0, forwarded: 0, dropped: 0 }) });
    mount();
    await waitFor(() => expect(document.querySelector('[data-flows="absent"]')?.textContent).toContain('no flow source'));
    expect(document.querySelector('[data-flows="absent"]')?.hasAttribute('disabled')).toBe(true);
  });

  it("draws what Calico's Goldmane saw as it draws Hubble's, and names the source", async () => {
    stub({ [BASE]: overview(), [`${BASE}flows`]: flows({ source: 'calico' }) });
    mount();
    await waitFor(() => expect(document.querySelector('[data-flows="read"]')?.getAttribute('data-source')).toBe('calico'));
    expect(document.querySelector('[data-flows="read"]')?.getAttribute('title')).toContain('Calico');
    await waitFor(() => expect(document.querySelector('[data-cell="ns:ops>ns:shop"]')?.getAttribute('data-seen')).toBe('dropped'));
    expect(document.querySelector('[data-cell="ns:shop>entity:world"]')?.getAttribute('data-seen')).toBe('forwarded');
  });

  it("tells each way Goldmane cannot be read apart, in a word", async () => {
    const words: [Flows['state'], string][] = [
      ['refused', 'not allowed'],
      ['tls', 'TLS failed'],
      ['unreachable', 'unreachable'],
    ];
    for (const [state, word] of words) {
      cleanup();
      stub({ [BASE]: overview(), [`${BASE}flows`]: flows({ source: 'calico', state, detail: 'why', rows: [], edges: [], read: 0, forwarded: 0, dropped: 0 }) });
      mount();
      await waitFor(() => expect(document.querySelector(`[data-flows="${state}"]`)?.textContent).toContain(word));
      expect(document.querySelector(`[data-flows="${state}"]`)?.hasAttribute('disabled')).toBe(true);
    }
  });

  it('says an empty window as none seen', async () => {
    stub({ [BASE]: overview(), [`${BASE}flows`]: flows({ state: 'empty', rows: [], edges: [], read: 0, forwarded: 0, dropped: 0 }) });
    mount();
    await waitFor(() => expect(document.querySelector('[data-flows="empty"]')?.textContent).toContain('0 · 5 min'));
    expect(document.querySelector('[data-list-tab="flows"]')).toBeNull();
  });
});
