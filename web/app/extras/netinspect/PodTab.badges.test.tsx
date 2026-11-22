import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Direction, PeerView, PodAnswer, RuleView } from './data';
import PodTab from './PodTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

const LONG = 'app.kubernetes.io/name=checkout-api-gateway-internal-v2 in namespace team-platform-engineering-production-eu-west-1';

const peer = (kind: PeerView['kind'], label: string): PeerView => ({ kind, label, pods: kind === 'pods' ? 2 : 0, namespaces: kind === 'pods' ? ['shop'] : [] });
const rule = (i: number, peers: PeerView[], kind: RuleView['kind'] = 'allow'): RuleView => ({ namespace: 'shop', name: `p${String(i)}`, kind, rule: i, text: '', peers, ports: ['443/TCP'] });
const side = (rules: RuleView[]): Direction => ({ isolated: true, policies: [{ namespace: 'shop', name: 'p1' }], rules });

function mount(over: Partial<PodAnswer>): void {
  const body: PodAnswer = {
    mode: 'rules',
    complete: true,
    notes: [],
    cni: { name: 'Cilium', enforces: 'yes', detail: '' },
    unevaluated: [],
    pod: { namespace: 'shop', name: 'api', workload: 'api', ip: '10.0.0.10', in: true, out: true, hostNetwork: false, policies: 1 },
    ingress: side([]),
    egress: side([]),
    ...over,
  };
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <PodTab cluster="kind" kind="pods" namespace="shop" name="api" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const egress = (): HTMLElement => document.querySelector('[data-direction="egress"]') as HTMLElement;

describe('badges in the pod Network tab', () => {
  it('shortens a long peer with an ellipsis and keeps its whole text in the tooltip', async () => {
    mount({ egress: side([rule(1, [peer('cidr', LONG)])]) });
    await waitFor(() => expect(egress()).not.toBeNull());
    const name = egress().querySelector('[data-rule] [data-peer]') as HTMLElement;
    expect(name.textContent).toContain('…');
    expect(name.textContent?.length).toBeLessThan(LONG.length);
    expect(name.getAttribute('title')).toContain(LONG);
    const graph = document.querySelector('[data-mini-graph]');
    const labels = [...(graph?.querySelectorAll('[data-arrow-name]') ?? [])].map((t) => t.textContent ?? '');
    expect(labels.some((t) => t.includes('…'))).toBe(true);
    expect(graph?.querySelector('[data-arrow] > title')?.textContent).toContain(LONG);
  });

  it('gives each peer of a rule its own row and arrow', async () => {
    const peers = ['a', 'b', 'c', 'd', 'e'].map((x) => peer('cidr', `10.${x}.0.0/16`));
    mount({ egress: side([rule(1, peers)]) });
    await waitFor(() => expect(egress()).not.toBeNull());
    expect(egress().querySelectorAll('[data-rule]').length).toBe(5);
    expect(document.querySelectorAll('[data-arrow][data-side="egress"]').length).toBe(5);
  });

  it('folds rows past the eighth into Show all', async () => {
    const rules = Array.from({ length: 12 }, (_, i) => rule(i + 1, [peer('pods', `app=s${String(i)}`)]));
    mount({ ingress: side(rules) });
    await waitFor(() => expect(document.querySelector('[data-direction="ingress"]')).not.toBeNull());
    const box = document.querySelector('[data-direction="ingress"]') as HTMLElement;
    expect(box.querySelectorAll('[data-rule]').length).toBe(8);
    expect(box.querySelector('[data-show-all]')?.textContent).toBe('Show all 12');
  });

  it('draws Cilium entities and a rule not evaluated', async () => {
    mount({
      unevaluated: ['A', 'B', 'C', 'D', 'E'].map((kind) => ({ kind, group: 'x', count: 'at least 1' })),
      egress: side([rule(1, [peer('entity', 'world')]), rule(2, [peer('entity', 'toFQDNs api.example.com')]), rule(3, [peer('unevaluated', 'CiliumClusterwideNetworkPolicy x')], 'unevaluated')]),
    });
    await waitFor(() => expect(egress()).not.toBeNull());
    expect(egress().textContent).toContain('world');
    expect(egress().textContent).toContain('toFQDNs api.example.com');
    expect(egress().querySelector('[data-kind="unevaluated"]')?.textContent).toContain('not evaluated');
    const note = document.querySelector('[data-unevaluated]') as HTMLElement;
    expect(note.textContent).toContain('Not evaluated: A, B, C');
    expect(note.textContent).toContain('+2');
  });

  it('draws open and host-network pods', async () => {
    mount({ egress: { isolated: false, policies: [], rules: [] } });
    await waitFor(() => expect(egress().textContent).toContain('open'));
  });
});
