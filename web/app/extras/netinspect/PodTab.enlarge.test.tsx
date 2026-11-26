import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { installKeyboardStack } from '../../../ui/src/keyboard/stack';
import { Shortcuts } from '../../../ui/src/pages/object/Shortcuts';
import { ShellProvider } from '../../../ui/src/shell/ShellContext';
import { podPath, type FlowRow, type Flows, type PeerView, type PodAnswer, type RuleView } from './data';
import { netinspect } from './extension';
import { PodView } from './PodTab';

let release: () => void = () => undefined;
beforeAll(() => {
  release = installKeyboardStack();
});
afterAll(() => release());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const pods = (selector: string, ns: string, n = 3): PeerView => ({ kind: 'pods', label: `pods ${selector} in namespace ${ns}`, pods: n, namespaces: [ns] });
const rule = (i: number, peer: PeerView, ports: string[]): RuleView => ({ namespace: 'shop', name: `pol-${String(i)}`, kind: 'allow', rule: i, text: '', peers: [peer], ports });

/** Seven peers on ingress: the small graph folds them by namespace, the large one draws each. */
const ANSWER: PodAnswer = {
  mode: 'rules',
  complete: true,
  notes: [],
  cni: { name: 'Cilium', enforces: 'yes', detail: '' },
  unevaluated: [],
  pod: { namespace: 'shop', name: 'api-0', workload: 'api', ip: '10.0.0.10', in: true, out: true, hostNetwork: false, policies: 2 },
  ingress: {
    isolated: true,
    policies: [{ namespace: 'shop', name: 'pol-1' }],
    rules: Array.from({ length: 7 }, (_, i) => rule(i + 1, pods(`app=client-${String(i)}`, i < 4 ? 'shop' : 'edge'), [`${String(8000 + i)}/TCP`])),
  },
  egress: { isolated: true, policies: [{ namespace: 'shop', name: 'pol-9' }], rules: [rule(9, pods('app=ledger-db', 'payments', 1), ['5432/TCP'])] },
};

const row = (from: string, to: string, port: number, verdict: string, count: number): FlowRow => ({ from, to, port, protocol: 'TCP', verdict, deniedBy: verdict === 'DROPPED' ? ['shop/deny-batch'] : [], count, last: '2026-03-11T09:38:00Z' });
const flows = (rows: FlowRow[]): Flows => ({ mode: 'observed', source: 'hubble', state: 'read', window: 300, read: 10, narrowed: false, truncated: false, forwarded: 0, dropped: 0, rows, edges: [] });

function stubFlows(): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    if (url.startsWith('/x/kind/netinspect/flows')) {
      const longer = url.includes('window=900');
      return json(
        flows([
          row('shop/web-1', 'shop/api-0', 8080, 'FORWARDED', longer ? 900 : 120),
          row('jobs/batch-1', 'shop/api-0', 8080, 'DROPPED', 4),
          row('shop/api-0', 'payments/ledger-db-0', 5432, 'FORWARDED', 60),
          row('shop/web-1', 'shop/cart-0', 6379, 'FORWARDED', 999),
        ]),
      );
    }
    // The Flow UI buttons read their own list: left pending.
    return new Promise<Response>(() => undefined);
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(answer: PodAnswer = ANSWER): void {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <PodView cluster="kind" answer={answer} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const dialog = () => document.querySelector('[data-pod-enlarged]');
const inDialog = (selector: string) => [...(dialog()?.querySelectorAll(selector) ?? [])];

describe('the enlarged pod picture', () => {
  it('opens from the small graph in a dialog, larger and with every peer, and Escape closes it', async () => {
    stubFlows();
    mount();
    expect(document.querySelectorAll('[data-mini-graph] [data-arrow][data-side="ingress"]')).toHaveLength(2);
    fireEvent.click(document.querySelector('[data-enlarge]') as Element);
    expect(dialog()).not.toBeNull();
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('api-0');
    const big = inDialog('[data-large-graph] [data-arrow][data-side="ingress"]');
    expect(big).toHaveLength(7);
    expect(big[0]?.querySelector('[data-arrow-name]')?.textContent).toBe('app=client-4 · edge');
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => expect(dialog()).toBeNull());
  });

  it('opens on a press of the graph itself, not on a press of an arrow', () => {
    stubFlows();
    mount();
    fireEvent.click(document.querySelector('[data-mini-graph] [data-arrow][data-side="egress"]') as Element);
    expect(dialog()).toBeNull();
    fireEvent.click(document.querySelector('[data-mini-graph]') as Element);
    expect(dialog()).not.toBeNull();
  });

  it('lights and fixes an arrow in the dialog, and the first Escape lets go of it only', async () => {
    stubFlows();
    mount();
    fireEvent.click(document.querySelector('[data-enlarge]') as Element);
    const arrow = inDialog('[data-arrow][data-side="ingress"]')[0] as Element;
    fireEvent.mouseEnter(arrow);
    expect(inDialog('[data-direction="ingress"] [data-flow]').filter((r) => r.getAttribute('data-highlight') === 'on')).toHaveLength(1);
    fireEvent.click(arrow);
    fireEvent.mouseLeave(arrow);
    expect(arrow.getAttribute('aria-pressed')).toBe('true');
    expect(inDialog('[data-direction="ingress"] [data-flow]')).toHaveLength(1);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(arrow.getAttribute('aria-pressed')).toBe('false');
    expect(dialog()).not.toBeNull();
    expect(inDialog('[data-direction="ingress"] [data-flow]')).toHaveLength(7);
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => expect(dialog()).toBeNull());
  });

  it('filters both directions and the flows from one field', async () => {
    stubFlows();
    mount();
    fireEvent.click(document.querySelector('[data-enlarge]') as Element);
    await waitFor(() => expect(inDialog('[data-pod-flow]')).toHaveLength(3));
    fireEvent.change(dialog()?.querySelector('input[name="podq"]') as Element, { target: { value: '5432' } });
    expect(inDialog('[data-direction="ingress"] [data-flow]')).toHaveLength(0);
    expect(inDialog('[data-direction="egress"] [data-flow]')).toHaveLength(1);
    expect(inDialog('[data-pod-flow]').map((r) => r.getAttribute('data-verdict'))).toEqual(['FORWARDED']);
  });

  it('shows the pod flows only, and reads them again over the period picked', async () => {
    const fetch = stubFlows();
    mount();
    fireEvent.click(document.querySelector('[data-enlarge]') as Element);
    await waitFor(() => expect(inDialog('[data-pod-flow]')).toHaveLength(3));
    expect(dialog()?.querySelector('[data-pod-flow-counts]')?.textContent).toBe('1804');
    fireEvent.click(dialog()?.querySelector('[data-window="900"]') as Element);
    await waitFor(() => expect(dialog()?.querySelector('[data-pod-flow-counts]')?.textContent).toBe('9604'));
    expect(fetch.mock.calls.map((c) => String(c[0]))).toEqual(expect.arrayContaining(['/x/kind/netinspect/flows?ns=shop', '/x/kind/netinspect/flows?ns=shop&window=900']));
    const first = inDialog('[data-pod-flow]')[0] as Element;
    fireEvent.click(first);
    expect(first.getAttribute('aria-selected')).toBe('true');
  });

  it("keeps a host-network pod's flows, which the flow source names by its node's address", async () => {
    const node = '192.168.1.40';
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        if (!url.startsWith('/x/kind/netinspect/flows')) return new Promise<Response>(() => undefined);
        const body = flows([row('shop/web-1', node, 9100, 'FORWARDED', 30), row(node, 'kube-system/dns-0', 53, 'FORWARDED', 5), row('shop/web-1', 'shop/cart-0', 6379, 'FORWARDED', 999)]);
        return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
      }),
    );
    mount({ ...ANSWER, pod: { ...ANSWER.pod, name: 'node-exporter-x', ip: node, hostNetwork: true } });
    fireEvent.click(document.querySelector('[data-enlarge]') as Element);
    await waitFor(() => expect(inDialog('[data-pod-flow]')).toHaveLength(2));
    expect(dialog()?.querySelector('[data-pod-flow-counts]')?.textContent).toBe('350');
  });
});

describe('the Details line', () => {
  it('opens the enlarged view in a dialog rather than another page', async () => {
    const answers: Record<string, unknown> = {
      '/api/c/kind/nav': { state: 'ok', data: { groups: [{ key: 'extra', label: 'Extra', items: [{ text: 'Network Inspector', href: '/c/kind/x/netinspect' }], deferred: false }] } },
      [podPath('kind', 'shop', 'api-0')]: ANSWER,
    };
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        if (!(url in answers)) return new Promise<Response>(() => undefined);
        return Promise.resolve(new Response(JSON.stringify(answers[url]), { status: 200, headers: { 'Content-Type': 'application/json' } }));
      }),
    );
    const where: string[] = [];
    function Where(): null {
      where.push(useLocation().pathname);
      return null;
    }
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={['/c/kind/pods/shop/api-0']}>
          <ShellProvider extensions={[netinspect]}>
            <Shortcuts identity={{ cluster: 'kind', kind: 'pods', namespace: 'shop', name: 'api-0' }} marks={{ labels: {}, annotations: {} }} />
            <Where />
          </ShellProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const line = await waitFor(() => {
      const found = document.querySelector('[data-shortcut="x.netinspect.pod"] button');
      expect(found).not.toBeNull();
      return found as Element;
    });
    expect(document.querySelector('[data-shortcut="x.netinspect.pod"] a')).toBeNull();
    fireEvent.click(line);
    await waitFor(() => expect(document.querySelector('[data-shortcut-dialog="x.netinspect.pod"] [data-pod-enlarged], [data-shortcut-dialog="x.netinspect.pod"]')).not.toBeNull());
    await waitFor(() => expect(document.querySelector('[data-large-graph]')).not.toBeNull());
    expect(new Set(where)).toEqual(new Set(['/c/kind/pods/shop/api-0']));
  });
});

describe('what the small graph held', () => {
  it('is let go when the dialog opens, so nothing stays pressed behind it', async () => {
    stubFlows();
    mount();
    const small = () => document.querySelector('[data-mini-graph] [data-arrow][data-side="egress"]') as Element;
    fireEvent.click(small());
    expect(small().getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(document.querySelector('[data-enlarge]') as Element);
    expect(small().getAttribute('aria-pressed')).toBe('false');
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => expect(dialog()).toBeNull());
    expect(small().getAttribute('aria-pressed')).toBe('false');
  });
});
