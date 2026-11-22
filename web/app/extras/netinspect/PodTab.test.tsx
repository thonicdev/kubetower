import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { isolationCells, podShortcut, type PodAnswer } from './data';
import PodTab from './PodTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

const ROUTE = '/x/kind/netinspect/pods/shop/api';

function stub(status: number, body: unknown): ReturnType<typeof vi.fn> {
  const fetch = vi.fn(() => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })));
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function answer(over: Partial<PodAnswer> = {}): PodAnswer {
  return {
    mode: 'rules',
    complete: true,
    notes: [],
    cni: { name: 'kindnet', enforces: 'yes', detail: 'Recognized from its DaemonSet: kindnet applies NetworkPolicies.' },
    unevaluated: [],
    pod: { namespace: 'shop', name: 'api', workload: 'api', ip: '10.0.0.10', in: true, out: false, hostNetwork: false, policies: 2 },
    ingress: {
      isolated: true,
      policies: [
        { namespace: 'shop', name: 'default-deny-ingress' },
        { namespace: 'shop', name: 'api-from-web' },
      ],
      rules: [
        {
          namespace: 'shop',
          name: 'api-from-web',
          kind: 'allow',
          rule: 1,
          text: 'pods app=web in this namespace, on http',
          peers: [{ kind: 'pods', label: 'pods app=web in this namespace', pods: 3, namespaces: ['shop'] }],
          ports: ['http'],
        },
      ],
    },
    egress: { isolated: false, policies: [], rules: [] },
    ...over,
  };
}

function mount(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <PodTab cluster="kind" kind="pods" namespace="shop" name="api" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("a pod's Network tab", () => {
  it('draws its two isolations, its rules dashed, and says they are rules', async () => {
    const fetch = stub(200, answer());
    mount();
    await waitFor(() => expect(document.querySelector('[data-direction="ingress"]')).not.toBeNull());
    expect(fetch).toHaveBeenCalledWith(ROUTE, expect.anything());
    expect(document.querySelector('[data-chip="in"]')?.hasAttribute('data-isolated')).toBe(true);
    expect(document.querySelector('[data-chip="out"]')?.hasAttribute('data-isolated')).toBe(false);
    expect(document.querySelector('[data-mode="rules"]')).not.toBeNull();
    const lines = document.querySelectorAll('[data-mini-graph] line');
    expect(lines.length).toBe(2);
    for (const line of lines) expect(line.getAttribute('stroke-dasharray')).toBeTruthy();
    expect(document.querySelector('[data-rule="api-from-web/1"]')?.textContent).toContain('3 pods');
    expect(document.querySelector('[data-direction="egress"]')?.textContent).toContain('open');
  });

  it('says a refusal as one', async () => {
    stub(403, { code: 'list_forbidden', message: 'The cluster says you may not list its NetworkPolicies, so which rules apply cannot be shown.' });
    mount();
    expect((await screen.findByRole('alert')).hasAttribute('data-refused')).toBe(true);
  });

  it('says a pod that is gone in one short line, with no code on screen', async () => {
    stub(404, { code: 'no_such_pod', message: 'There is no pod shop/api that you may list: it may have been deleted.' });
    mount();
    const note = await screen.findByText('Pod no longer exists');
    expect(note.hasAttribute('data-gone')).toBe(true);
    expect(note.getAttribute('title')).toContain('may have been deleted');
    expect(document.body.textContent).not.toMatch(/404|no_such_pod|request failed/i);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('keeps a finished pod apart from a gone one, and a failed read apart from both', async () => {
    stub(404, { code: 'pod_finished', message: 'The pod shop/api has finished, so it has no network to show.' });
    mount();
    const note = await screen.findByText('Pod has finished: no network to show');
    expect(note.hasAttribute('data-finished')).toBe(true);
    expect(document.body.textContent).not.toMatch(/no longer exists|404|pod_finished/i);
  });

  it('still draws a failed read as a failure, with its retry', async () => {
    stub(502, { code: 'list_failed', message: 'The cluster did not answer.' });
    mount();
    expect((await screen.findByRole('alert')).textContent).toContain('The request failed (502)');
    expect(screen.queryByText('Pod no longer exists')).toBeNull();
  });

  it('says an isolated direction with no rule as nothing passing', async () => {
    stub(200, answer({ ingress: { isolated: true, policies: [{ namespace: 'shop', name: 'default-deny-ingress' }], rules: [] } }));
    mount();
    await waitFor(() => expect(document.querySelector('[data-direction="ingress"]')?.textContent).toContain('nothing allowed'));
    expect(document.querySelector('[data-mini-graph]')?.textContent).toContain('nothing allowed');
  });
});

describe("the pod's tile and the Pods list's column", () => {
  const signal = new AbortController().signal;

  it('the tile names the two isolations and leads to the pod’s page', async () => {
    stub(200, answer());
    const target = await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'shop', name: 'api' }, signal);
    expect(target?.badges).toEqual(['in isolated', 'out open']);
    expect(target?.to).toBe('/c/kind/x/netinspect/pods/shop/api');
    expect(target?.detail).toBe('2 policies select it');
  });

  it('a pod the route does not know draws no tile', async () => {
    stub(404, { code: 'no_such_pod', message: 'There is no running pod shop/api that you may list.' });
    expect(await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'shop', name: 'api' }, signal)).toBeNull();
  });

  it('a finished pod draws no tile either', async () => {
    stub(404, { code: 'pod_finished', message: 'The pod shop/api has finished.' });
    expect(await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'shop', name: 'api' }, signal)).toBeNull();
  });

  it('the column is one read for the whole list', async () => {
    const fetch = stub(200, { 'shop/api': { text: 'in', hint: 'Isolated for ingress.' } });
    const cells = await isolationCells({ cluster: 'kind', kind: 'pods' }, signal);
    expect(cells['shop/api']?.text).toBe('in');
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith('/x/kind/netinspect/cells', expect.anything());
  });
});
