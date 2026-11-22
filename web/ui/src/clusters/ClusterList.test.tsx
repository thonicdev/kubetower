import { act, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubRoutes } from '../testing/harness';

/**
 * The chooser, mounted at `/` through the whole application, because what is
 * being claimed is what an operator sees: which figures are drawn, which are
 * a dash, and which clusters are never asked.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};

const CLUSTERS = [
  { name: 'prod', label: 'Production', initials: 'PR', colour: 'amber', state: 'ok', order: 0, bar: 1 },
  { name: 'lab', label: 'lab', initials: 'L', colour: 'teal-pale', state: 'ok', order: 1, bar: 1 },
  {
    name: 'edge',
    label: 'edge',
    initials: 'E',
    colour: 'red',
    state: 'unreachable',
    order: 2,
    bar: 2,
    reason: { code: 'cluster_unreachable', message: 'dial tcp 10.0.0.1:6443: i/o timeout' },
  },
];

function reading(value: number | null, text: string, trustworthy = true) {
  return { value, text, trustworthy };
}

const PROD = {
  state: 'ok',
  data: {
    cluster: 'prod',
    version: 'v1.31.2',
    nodesSummary: { text: '8 of 8 nodes healthy', href: '/c/prod/nodes' },
    nodesTone: 'ok',
    counts: { state: 'ok', items: [{ kind: 'pods', label: 'Pods', count: 64, healthy: 62, note: '2 not running' }] },
    capacity: {
      state: 'ok',
      items: [
        {
          label: 'CPU',
          // The strings `resources.CPUSize` and `MemorySize` really write.
          requested: reading(4000, '4.0 cores'),
          capacity: reading(8000, '8.0 cores'),
          used: reading(2000, '2.0 cores'),
        },
        {
          label: 'Memory',
          // Bytes, as the wire carries them.
          requested: reading(8 * 2 ** 30, '8.0 GiB'),
          capacity: reading(32 * 2 ** 30, '32.0 GiB'),
          used: reading(24 * 2 ** 30, '24.0 GiB'),
        },
      ],
    },
    alerts: {
      state: 'ok',
      items: [{ subject: { text: 'node-3', href: '/c/prod/nodes/node-3' }, summary: 'MemoryPressure' }],
    },
    history: {
      state: 'ok',
      data: {
        series: [
          {
            key: 'cpu-used',
            label: 'CPU used',
            data: { startedAt: '2026-10-21T10:00:00Z', stepSeconds: 60, values: [1000, 1500, null, 2000, 1800] },
          },
        ],
      },
    },
  },
};

/** A cluster with no metrics source: capacity is known, usage is not. */
const LAB = {
  state: 'ok',
  data: {
    cluster: 'lab',
    capacity: {
      state: 'ok',
      items: [
        {
          label: 'CPU',
          requested: reading(500, '500m'),
          capacity: reading(4000, '4.0 cores'),
          used: reading(null, 'metrics-server is not installed', false),
        },
      ],
    },
    alerts: { state: 'unreachable', reason: { code: 'timeout', message: 'the node list timed out' } },
    history: { state: 'absent', reason: { code: 'not_installed', message: 'no Prometheus' } },
  },
};

function routes(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: CLUSTERS } },
    '/api/preferences': { body: { rail: { barCount: 2, activeBar: 1, bars: [['prod', 'lab'], ['edge']], hidden: [] } } },
    '/api/capabilities': { body: { localShell: true } },
    '/api/c/prod/overview': { body: PROD },
    '/api/c/lab/overview': { body: LAB },
    ...extra,
  };
}

/** Every request path, in order, so a test can say what was never asked. */
function logged(stubbed: StubRoutes): string[] {
  stubFetch(stubbed);
  const inner = globalThis.fetch;
  const calls: string[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(String(input).split('?')[0] ?? '');
    return inner(input, init);
  });
  return calls;
}

function card(name: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(`#page [data-cluster-card="${name}"]`);
  if (element === null) throw new Error(`no card for ${name}`);
  return element;
}

describe('the cluster chooser', () => {
  it('lists every cluster, each card one link to its cluster, the unreachable one included', async () => {
    logged(routes());
    mountApp('/');

    await waitFor(() => {
      expect(document.querySelectorAll('#page a[data-cluster]')).toHaveLength(3);
    });
    const edge = document.querySelector('#page a[data-cluster="edge"]');
    expect(edge?.getAttribute('href')).toBe('/c/edge');
    expect(edge?.getAttribute('data-state')).toBe('unreachable');
    expect(card('edge').textContent).toContain('dial tcp 10.0.0.1:6443: i/o timeout');
    // A state other than reachable is said in words beside its dot.
    expect(card('edge').textContent).toContain('could not be reached');
    // The hotbar is the rail's business, not the card's.
    expect(card('edge').textContent).not.toContain('bar 2');
  });

  it("draws a reachable cluster's figures from its overview: used over allocatable, nodes, pods, version", async () => {
    logged(routes());
    mountApp('/');

    // Each tile is its bar and its percentage, and used
    // over allocatable - in the allocatable's one unit - is the hover.
    await waitFor(() => {
      expect(card('prod').querySelector('[data-capacity="CPU"] [data-share]')?.textContent).toBe('25%');
    });
    const prod = card('prod');
    const cpu = prod.querySelector('[data-capacity="CPU"] [data-measure]');
    expect(cpu?.getAttribute('title')).toBe('2 / 8 cores');
    expect(cpu?.querySelector('.sr-only')?.textContent).toBe('2 / 8 cores');
    const memory = prod.querySelector('[data-capacity="Memory"] [data-measure]');
    expect(memory?.querySelector('[data-share]')?.textContent).toBe('75%');
    expect(memory?.querySelector('[data-percent]')?.getAttribute('data-percent')).toBe('75');
    expect(memory?.getAttribute('title')).toBe('24 / 32 GiB');
    // The bar first, then its percentage, on one line; and no quantity drawn.
    expect(memory?.firstElementChild?.querySelector('[data-gauged]')).not.toBeNull();
    expect(memory?.className).not.toContain('flex-col');
    const drawn = prod.querySelector('[data-capacity="Memory"]')?.cloneNode(true) as HTMLElement;
    drawn.querySelectorAll('.sr-only').forEach((node) => {
      node.remove();
    });
    expect(drawn.textContent).toBe('Memory used75%');
    expect(prod.textContent).toContain('8 of 8 nodes healthy');
    expect(prod.textContent).toContain('64');
    expect(prod.textContent).toContain('v1.31.2');
    // Not on the card: the probe time, the alerts bell, the hotbar and the
    // word beside the green dot.
    expect(prod.textContent).not.toContain('probe');
    expect(prod.textContent).not.toContain('bar 1');
    // Said to a screen reader only, beside the dot.
    const said = [...prod.querySelectorAll('span')].filter((span) => span.textContent === 'reachable');
    expect(said.map((span) => span.className)).toEqual(['sr-only']);
    expect(prod.querySelector('[title*="node conditions"]')).toBeNull();
    // The CPU line, broken at the gap rather than drawn across it.
    expect(prod.querySelectorAll('figure svg polyline').length).toBe(2);
  });

  it('never asks the overview of a cluster the probe could not reach', async () => {
    const calls = logged(routes());
    mountApp('/');

    await waitFor(() => {
      expect(calls).toContain('/api/c/prod/overview');
      expect(calls).toContain('/api/c/lab/overview');
    });
    expect(calls).not.toContain('/api/c/edge/overview');
    expect(card('edge').querySelector('[data-capacity]')).toBeNull();
  });

  it('draws no usage tile and no line where nothing measured them', async () => {
    logged(routes());
    mountApp('/');

    await waitFor(() => {
      expect(card('prod').querySelector('[data-capacity="CPU"]')).not.toBeNull();
      expect(card('lab').textContent).not.toContain('Reading its overview');
    });
    const lab = card('lab');
    // lab's capacity answered, with no usage figure: no tile, and no sentence either.
    expect(lab.querySelector('[data-capacity]')).toBeNull();
    expect(lab.textContent).not.toContain('not measured');
    expect(lab.querySelector('figure')).toBeNull();
  });

  it('says an overview was not read, with the reason on hover, and keeps the card', async () => {
    const refused = { state: 'forbidden', reason: { code: 'read_denied', message: 'nodes is forbidden' } };
    logged(routes({ '/api/c/lab/overview': { body: refused } }));
    mountApp('/');

    await waitFor(() => {
      expect(card('lab').textContent).toContain('overview not read');
    });
    expect(card('lab').querySelector('[data-state="incomplete"]')?.getAttribute('title')).toBe('nodes is forbidden');
    expect(document.querySelector('#page a[data-cluster="lab"]')).not.toBeNull();
  });

  it('offers a shell on a reachable cluster only, and only where the profile has one', async () => {
    logged(routes());
    mountApp('/');

    await waitFor(() => {
      expect(card('prod').querySelector('button[aria-label="Open a shell on Production"]')).not.toBeNull();
    });
    expect(card('edge').querySelector('button')).toBeNull();
  });

  it('draws no shell where the profile does not offer one', async () => {
    logged(routes({ '/api/capabilities': { body: { localShell: false } } }));
    mountApp('/');

    await waitFor(() => {
      expect(card('prod').querySelector('[data-capacity="CPU"]')).not.toBeNull();
    });
    expect(document.querySelector('#page [data-cluster-card] button')).toBeNull();
  });

  it('has one link that names each card, and an Open beside it out of the keyboard walk', async () => {
    logged(routes());
    mountApp('/');

    await waitFor(() => {
      expect(card('prod').querySelectorAll('a[data-cluster]')).toHaveLength(1);
    });
    const links = [...card('prod').querySelectorAll('a')];
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['/c/prod', '/c/prod']);
    const open = links[1];
    expect(open?.getAttribute('tabindex')).toBe('-1');
    expect(open?.getAttribute('aria-hidden')).toBe('true');
    expect(open?.hasAttribute('data-cluster')).toBe(false);
  });

  it('counts a cluster not asked yet as neither reachable nor down', async () => {
    const pending = CLUSTERS.map((cluster) => ({ ...cluster, state: 'pending', reason: undefined }));
    logged(routes({ '/api/clusters': { body: { state: 'ok', items: pending } } }));
    mountApp('/');

    await waitFor(() => {
      expect(document.querySelector('#page [data-pill]')).not.toBeNull();
    });
    const pill = document.querySelector('#page [data-pill]');
    expect(pill?.textContent).toBe('0 reachable');
    // Neutral, not the red of three clusters down.
    expect(pill?.getAttribute('data-pill')).toBe('muted');
  });

  it('names a CPU line whose read failed, and dashes pods it could not count', async () => {
    const failing = {
      state: 'ok',
      data: {
        ...LAB.data,
        history: { state: 'unreadable', reason: { code: 'timeout', message: 'Prometheus timed out' } },
        counts: { state: 'unreachable', reason: { code: 'timeout', message: 'the pod list timed out' } },
      },
    };
    logged(routes({ '/api/c/lab/overview': { body: failing } }));
    mountApp('/');

    await waitFor(() => {
      expect(card('lab').textContent).toContain('CPU used — not read');
    });
    expect(card('lab').querySelector('[title="Prometheus timed out"]')).not.toBeNull();
    expect(card('lab').querySelector('[title="Pods not read"]')?.textContent).toBe('—');
  });

  it('draws no panel under the cards', async () => {
    logged(routes());
    mountApp('/');

    await waitFor(() => {
      expect(card('prod').querySelector('[data-capacity="CPU"]')).not.toBeNull();
    });
    expect(document.querySelector('#page section')).toBeNull();
    expect(document.querySelector('#page h2')?.textContent).not.toBe('Nodes');
  });

  it('opens the cluster on a click anywhere on its card, and leaves its own controls and modified clicks alone', async () => {
    logged(routes());
    const { router } = mountApp('/');

    await waitFor(() => {
      expect(card('prod').querySelector('[data-capacity="CPU"]')).not.toBeNull();
    });
    // A modified click is the name link's to answer, not the card's.
    // A navigation settles after the click, so each "stayed" is read a beat later.
    const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 50)));
    fireEvent.click(card('prod').querySelector('[data-capacity="CPU"]') as Element, { metaKey: true });
    await settle();
    expect(router.state.location.pathname).toBe('/');
    // A control of the card's own answers its click alone: Open navigates
    // once, not once for itself and again for the card under it. Not covered
    // here: the Shell button, the one control that does not prevent the
    // default - clicking it starts an xterm, which jsdom cannot host.
    const navigations = vi.spyOn(router, 'navigate');
    fireEvent.click(card('prod').querySelector('a[aria-hidden="true"]') as Element);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod');
    });
    await settle();
    expect(navigations).toHaveBeenCalledTimes(1);
    navigations.mockRestore();
    await act(() => router.navigate('/'));
    await waitFor(() => {
      expect(card('prod').querySelector('[data-capacity="CPU"]')).not.toBeNull();
    });
    // A click on the card's body opens the cluster.
    fireEvent.click(card('prod').querySelector('[data-capacity="CPU"]') as Element);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod');
    });
  });

  it('opens an unreachable cluster too, where its page says why', async () => {
    logged(routes());
    const { router } = mountApp('/');

    await waitFor(() => {
      expect(card('edge').textContent).toContain('could not be reached');
    });
    fireEvent.click(card('edge'));
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/edge');
    });
  });

  it('draws nothing about usage where the metrics read failed, as where there is none', async () => {
    const refused = {
      state: 'ok',
      data: {
        ...LAB.data,
        capacity: {
          state: 'ok',
          items: [
            {
              label: 'CPU',
              requested: reading(500, '500m'),
              capacity: reading(4000, '4.0 cores'),
              used: reading(null, 'metrics-server refused the read: nodes.metrics.k8s.io is forbidden', false),
            },
          ],
        },
        top: { state: 'forbidden', reason: { code: 'read_denied', message: 'nodes.metrics.k8s.io is forbidden' } },
      },
    };
    logged(routes({ '/api/c/lab/overview': { body: refused } }));
    mountApp('/');

    await waitFor(() => {
      expect(card('lab').textContent).not.toContain('Reading its overview');
    });
    expect(card('lab').querySelector('[data-capacity]')).toBeNull();
    expect(card('lab').textContent).not.toContain('not read');
    expect(card('lab').querySelector('[data-state="incomplete"]')).toBeNull();
  });
});

/**
 * The read-only guardrail, beside the clusters it marks:
 * three states, three different words, and the markings that matched nothing.
 */
describe('the read-only guardrail on the cluster chooser', () => {
  function withReadOnly(readOnly: Record<string, unknown>): StubRoutes {
    return routes({
      '/api/capabilities': {
        body: { localShell: true, readOnly: { writesRefused: false, detailWritesRefused: false, ...readOnly } },
      },
    });
  }

  async function fence(): Promise<HTMLElement> {
    let element: HTMLElement | null = null;
    await waitFor(() => {
      element = document.querySelector<HTMLElement>('#page [data-readonly-fence]');
      expect(element).not.toBeNull();
    });
    return element as unknown as HTMLElement;
  }

  const SAID = {
    on: 'measured at start-up: each refused a mutating verb when asked.',
    off: 'kubectl is NOT behind the read-only overrides: a session resolves /usr/bin/kubectl.',
    unknown: 'the probe resolved neither kubectl nor helm, so nothing refused anything.',
  };

  it('says on, off and unknown in three different words, each with its tone', async () => {
    const seen = new Map<string, string>();
    for (const state of ['on', 'off', 'unknown'] as const) {
      stubFetch(withReadOnly({ fence: state, measured: state !== 'unknown', detail: SAID[state] }));
      const { view } = mountApp('/');
      const element = await fence();
      expect(element.getAttribute('data-readonly-fence')).toBe(state);
      const pill = element.querySelector('[data-pill]');
      expect(pill?.getAttribute('data-tone')).toBe({ on: 'ok', off: 'bad', unknown: 'warn' }[state]);
      expect(pill?.getAttribute('title')).toBe(SAID[state]);
      seen.set(state, pill?.textContent ?? '');
      // `on` keeps its sentence on hover; the two an operator acts on say it
      // on the page.
      const detail = element.querySelector('[data-readonly-detail]');
      if (state === 'on') expect(detail).toBeNull();
      else expect(detail?.textContent).toBe(SAID[state]);
      view.unmount();
      vi.unstubAllGlobals();
    }
    expect(new Set(seen.values()).size).toBe(3);
    expect(seen.get('unknown')).toContain('not measured');
    expect(seen.get('off')).toContain('NOT in place');
  });

  it('names every read-only marking that matched no context', async () => {
    stubFetch(withReadOnly({ fence: 'on', measured: true, detail: SAID.on, unmatched: ['prdo', 'stagign'] }));
    mountApp('/');
    const element = await fence();
    const unmatched = element.querySelector('[data-readonly-unmatched]');
    expect(unmatched?.textContent).toContain('"prdo", "stagign"');
    expect(unmatched?.textContent).toContain('nothing is protected by those entries');
  });

  it('says nothing about unmatched markings when every one matched', async () => {
    stubFetch(withReadOnly({ fence: 'on', measured: true, detail: SAID.on }));
    mountApp('/');
    const element = await fence();
    expect(element.querySelector('[data-readonly-unmatched]')).toBeNull();
  });
});
