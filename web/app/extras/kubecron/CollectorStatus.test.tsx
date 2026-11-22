import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import CollectorStatus from './CollectorStatus';
import { kubecron } from './index';

afterEach(() => {
  vi.unstubAllGlobals();
});

function stub(bodies: unknown[]): string[] {
  const asked: string[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
    asked.push(path);
    const body = bodies[Math.min(asked.length - 1, bodies.length - 1)];
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }));
  });
  return asked;
}

function mount(revision = 0) {
  const client = new QueryClient();
  const view = render(
    <QueryClientProvider client={client}>
      <CollectorStatus cluster="prod" revision={revision} />
    </QueryClientProvider>,
  );
  return {
    rerender: (next: number) =>
      view.rerender(
        <QueryClientProvider client={client}>
          <CollectorStatus cluster="prod" revision={next} />
        </QueryClientProvider>,
      ),
  };
}

describe("KubeCron's diagnosis in its Settings tile", () => {
  it('is what the extension hands the tile', () => {
    expect(kubecron.SettingsStatus).toBeDefined();
  });

  it('draws each step with where its value came from, and reads again after a save', async () => {
    const asked = stub([
      {
        collector: {
          state: 'unusable',
          detail: 'the collector at kubecron/kubecron refused the token from Secret kubecron/kubecron-api (401)',
          checks: [
            { step: 'collector', state: 'ok', detail: 'the collector\'s Service kubecron/kubecron, set for this cluster in Settings', source: 'setting' },
            { step: 'secret', state: 'ok', detail: 'the token\'s Secret kubecron/kubecron-api, key api-token, the default', source: 'default' },
            { step: 'reach', state: 'ok', detail: 'the collector at kubecron/kubecron answers' },
            { step: 'token', state: 'refused', detail: 'the collector at kubecron/kubecron refused the token from Secret kubecron/kubecron-api (401)' },
          ],
        },
      },
      { collector: { state: 'ok', where: 'kubecron/kubecron', checks: [] } },
    ]);
    const view = mount(0);
    await waitFor(() => {
      expect(document.querySelector('[data-collector-status="unusable"]')).not.toBeNull();
    });
    expect(asked[0]).toBe('/x/prod/kubecron/status');
    expect(document.querySelector('[data-check="token"]')?.getAttribute('data-check-state')).toBe('refused');
    expect(document.querySelector('[data-check="collector"] [data-check-source]')?.textContent).toContain('setting');
    view.rerender(1);
    expect(await screen.findByText('Reading the collector at kubecron/kubecron.')).toBeTruthy();
    expect(asked).toHaveLength(2);
  });

  it('rechecks on request', async () => {
    const asked = stub([{ collector: { state: 'absent', detail: 'no KubeCron collector runs in this cluster' } }]);
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Recheck' }));
    await waitFor(() => {
      expect(asked).toHaveLength(2);
    });
  });
});
