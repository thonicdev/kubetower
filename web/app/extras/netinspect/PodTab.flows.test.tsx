import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { PeerView, PodAnswer, RuleView } from './data';
import { PodView } from './PodTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const pods = (selector: string, ns: string, n = 3): PeerView => ({ kind: 'pods', label: `pods ${selector} in namespace ${ns}`, pods: n, namespaces: [ns] });
const rule = (i: number, peer: PeerView, ports: string[], kind: RuleView['kind'] = 'allow'): RuleView => ({ namespace: 'shop', name: `pol-${String(i)}`, kind, rule: i, text: '', peers: [peer], ports });

function answer(ingress: RuleView[], egress: RuleView[] = []): PodAnswer {
  return {
    mode: 'rules',
    complete: true,
    notes: [],
    cni: { name: 'kindnet', enforces: 'yes', detail: '' },
    unevaluated: [],
    pod: { namespace: 'shop', name: 'api', workload: 'api', ip: '10.0.0.10', in: true, out: true, hostNetwork: false, policies: 2 },
    ingress: { isolated: true, policies: [{ namespace: 'shop', name: 'pol-1' }], rules: ingress },
    egress: { isolated: true, policies: [{ namespace: 'shop', name: 'pol-1' }], rules: egress },
  };
}

function mount(a: PodAnswer): void {
  // The Flow UI buttons read their own list: left pending.
  vi.stubGlobal('fetch', vi.fn(() => new Promise(() => undefined)));
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <PodView cluster="kind" answer={a} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const arrows = (side = 'ingress') => [...document.querySelectorAll(`[data-arrow][data-side="${side}"]`)];
const rows = (side = 'ingress') => [...document.querySelectorAll(`[data-direction="${side}"] [data-flow]`)];
const many = (n: number, ns = (i: number) => `ns-${String(i)}`) => Array.from({ length: n }, (_, i) => rule(i + 1, pods(`app=s${String(i)}`, ns(i)), ['80/TCP']));

describe('the graph', () => {
  const simple = answer([rule(1, pods('app=frontend', 'shop'), ['80/TCP', '443/TCP']), rule(2, pods('app=batch', 'jobs', 1), ['8080/TCP', '9090/TCP', '9091/TCP'])]);

  it('writes the protocol and ports on each arrow, who it is, and the pod count apart', () => {
    mount(simple);
    // Namespace order: jobs, then shop.
    expect(arrows()[1]?.querySelector('[data-arrow-ports]')?.textContent).toBe('TCP 80, 443');
    expect(arrows()[0]?.querySelector('[data-arrow-ports]')?.textContent).toBe('8080, 9090 +1');
    expect(arrows()[1]?.querySelector('[data-arrow-name]')?.textContent).toBe('app=frontend · shop');
    expect(arrows()[1]?.textContent).toContain('3 pods');
    expect(arrows()[0]?.querySelector('title')?.textContent).toContain('8080/TCP, 9090/TCP, 9091/TCP');
  });

  it('says every port for a rule with no filter', () => {
    mount(answer([rule(1, pods('app=frontend', 'shop'), [])]));
    expect(arrows()[0]?.querySelector('[data-arrow-ports]')?.textContent).toBe('every port');
  });

  it('lights the rows of an arrow on hover and dims the others', () => {
    mount(simple);
    fireEvent.mouseEnter(arrows()[0] as Element);
    expect(rows().map((r) => r.getAttribute('data-highlight'))).toEqual(['on', 'dim']);
    expect(arrows()[0]?.hasAttribute('data-active')).toBe(true);
    expect(arrows()[1]?.hasAttribute('data-dimmed')).toBe(true);
    fireEvent.mouseLeave(arrows()[0] as Element);
    expect(rows().map((r) => r.getAttribute('data-highlight'))).toEqual([null, null]);
  });

  it('lights the arrow of a row on hover', () => {
    mount(simple);
    fireEvent.mouseEnter(rows()[1] as Element);
    expect(arrows().map((a) => a.hasAttribute('data-active'))).toEqual([false, true]);
  });

  it('fixes a click, and Escape lets go', () => {
    mount(simple);
    fireEvent.click(arrows()[0] as Element);
    fireEvent.mouseLeave(arrows()[0] as Element);
    expect(arrows()[0]?.getAttribute('aria-pressed')).toBe('true');
    // Only what the arrow stands for is listed, and lit.
    expect(rows().map((r) => r.getAttribute('data-highlight'))).toEqual(['on']);
    expect(document.querySelector('[data-direction="ingress"] [data-clear-filter]')).not.toBeNull();
    fireEvent.click(arrows()[0] as Element);
    expect(arrows()[0]?.getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(arrows()[0] as Element);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(arrows()[0]?.getAttribute('aria-pressed')).toBe('false');
    expect(rows().map((r) => r.getAttribute('data-highlight'))).toEqual([null, null]);
    expect(document.querySelector('[data-clear-filter]')).toBeNull();
  });

  it('says how many rows a hovered arrow stands for', () => {
    mount(simple);
    fireEvent.mouseEnter(arrows()[0] as Element);
    expect(document.querySelector('[data-direction="ingress"] [data-match-count]')?.textContent).toBe('1 of 2');
  });

  it('answers the keyboard like a click', () => {
    mount(simple);
    fireEvent.keyDown(arrows()[1] as Element, { key: 'Enter' });
    expect(arrows()[1]?.getAttribute('aria-pressed')).toBe('true');
  });

  it('keeps an open direction as one dotted anyone with nothing to press', () => {
    const a = answer([]);
    a.ingress = { isolated: false, policies: [], rules: [] };
    mount(a);
    expect(arrows()).toHaveLength(1);
    expect(arrows()[0]?.getAttribute('role')).toBeNull();
    expect(arrows()[0]?.querySelector('[data-arrow-name]')?.textContent).toBe('anyone (open)');
    expect(document.querySelector('[data-direction="ingress"]')?.textContent).toContain('open');
  });

  it('says nothing allowed when no rule allows anything', () => {
    mount(answer([]));
    expect(document.querySelector('[data-mini-graph]')?.textContent).toContain('nothing allowed');
  });
});

describe('a side with many peers', () => {
  it('draws five arrows for five peers and one a namespace for six', () => {
    mount(answer(many(5)));
    expect(arrows()).toHaveLength(5);
    expect(arrows().every((a) => a.getAttribute('data-arrow')?.startsWith('pods:'))).toBe(true);
    cleanup();
    mount(answer(many(6, (i) => `ns-${String(i % 2)}`)));
    expect(arrows()).toHaveLength(2);
    expect(arrows()[0]?.querySelector('[data-arrow-name]')?.textContent).toBe('ns-0 · 9 pods');
  });

  it('draws the four largest namespaces and a +N past five', () => {
    mount(answer(many(12)));
    expect(arrows()).toHaveLength(5);
    expect(arrows()[4]?.querySelector('[data-arrow-name]')?.textContent).toBe('+8 more');
  });

  it('filters the rows to what an aggregate stands for, and gives them back', () => {
    mount(answer(many(12)));
    expect(rows()).toHaveLength(8);
    fireEvent.click(arrows()[4] as Element);
    expect(rows()).toHaveLength(8);
    expect(document.querySelector('[data-direction="ingress"] [data-clear-filter]')).not.toBeNull();
    fireEvent.click(document.querySelector('[data-direction="ingress"] [data-clear-filter]') as Element);
    expect(document.querySelector('[data-clear-filter]')).toBeNull();
    fireEvent.click(arrows()[0] as Element);
    expect(rows().filter((r) => r.getAttribute('data-highlight') === 'on')).toHaveLength(1);
  });

  it('draws ports once for the namespace', () => {
    const peers = Array.from({ length: 6 }, (_, i) => rule(i + 1, pods(`app=s${String(i)}`, 'shop', 1), [`${String(80 + i)}/TCP`, `${String(90 + i)}/TCP`]));
    mount(answer(peers));
    expect(arrows()).toHaveLength(1);
    expect(arrows()[0]?.querySelector('[data-arrow-ports]')?.textContent).toBe('80, 90 +10');
  });
});

describe('the rows', () => {
  it('lines up peer, ports and policy', () => {
    mount(answer([rule(1, pods('app=frontend', 'shop'), ['80/TCP', '443/TCP']), rule(2, pods('app=batch', 'jobs', 1), ['every port'], 'deny')]));
    const first = rows()[0] as Element;
    expect(first.getAttribute('data-kind')).toBe('deny');
    expect(first.querySelector('[data-peer]')?.textContent).toBe('app=batch · jobs');
    expect(first.querySelector('[data-ports]')?.textContent).toBe('every port');
    expect(first.querySelector('[data-policy]')?.textContent).toBe('deny · pol-2 · rule 2');
    expect(rows()[1]?.querySelector('[data-policy]')?.textContent).toBe('pol-1 · rule 1');
  });

  it('shows eight rows and a Show all past eight, with a filter', () => {
    mount(answer(many(8)));
    expect(rows()).toHaveLength(8);
    expect(document.querySelector('[data-show-all]')).toBeNull();
    expect(document.querySelector('[data-filter]')).toBeNull();
    cleanup();
    mount(answer(many(9)));
    expect(rows()).toHaveLength(8);
    const button = document.querySelector('[data-direction="ingress"] [data-show-all]') as HTMLElement;
    expect(button.textContent).toBe('Show all 9');
    fireEvent.click(button);
    expect(rows()).toHaveLength(9);
    expect(button.textContent).toBe('Show fewer');
    expect(document.querySelector('[data-filter]')?.getAttribute('placeholder')).toBe('Filter');
  });

  it('filters by peer, port and policy', () => {
    mount(answer([...many(9), rule(10, pods('app=special', 'shop'), ['5432/TCP'])]));
    const input = document.querySelector('[data-direction="ingress"] [data-filter]') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'special' } });
    expect(rows()).toHaveLength(1);
    fireEvent.change(input, { target: { value: '5432' } });
    expect(rows()).toHaveLength(1);
    fireEvent.change(input, { target: { value: 'pol-3' } });
    expect(rows()).toHaveLength(1);
    fireEvent.change(input, { target: { value: 'nope' } });
    expect(rows()).toHaveLength(0);
    expect(document.querySelector('[data-no-match]')).not.toBeNull();
  });

  it('scrolls inside a bounded height past forty rows', () => {
    mount(answer(many(45)));
    expect(document.querySelector('[data-direction="ingress"] [data-scroll]')).toBeNull();
    fireEvent.click(document.querySelector('[data-direction="ingress"] [data-show-all]') as Element);
    expect(rows()).toHaveLength(45);
    expect(document.querySelector('[data-direction="ingress"] [data-scroll]')?.className).toContain('max-h-');
  });
});
