import { QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { createQueryClient } from '../../query/client';
import { stubFetch, type StubReply } from '../../testing/harness';
import { Metrics } from './Metrics';

/**
 * A claim's storage chart is there or it is not. Where Prometheus is missing,
 * holds nothing for the claim, or fails, the claim's panel shows nothing at all:
 * no notice, no empty box, no sentence about why.
 */

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const METRICS = '/api/c/prod/metrics';

const used = {
  key: 'volume_used',
  chart: 'volume',
  label: 'Used',
  unit: 'bytes',
  data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [1073741824, 2147483648] },
};
const capacity = {
  key: 'volume_capacity',
  chart: 'volume',
  label: 'Capacity',
  unit: 'bytes',
  data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [10737418240, 10737418240] },
};

function ok(series: unknown[]): StubReply {
  return {
    body: {
      state: 'ok',
      data: {
        series,
        metrics: [{ key: 'volume', label: 'Storage', available: true, selected: true }],
        windows: [{ key: '1h', label: '1h', selected: true }],
      },
    },
  };
}

function mount(): HTMLElement {
  const { container } = render(
    <QueryClientProvider client={createQueryClient()}>
      <KeyboardStack>
        <Metrics cluster="prod" kind="persistentvolumeclaims" namespace="shop" name="data" silent />
      </KeyboardStack>
    </QueryClientProvider>,
  );
  return container;
}

let reads = 0;

/** stubFetch, counting the reads that went out. */
function stub(routes: Record<string, StubReply>): void {
  reads = 0;
  stubFetch(routes);
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (...args: Parameters<typeof fetch>) => {
    reads += 1;
    return stubbed(...args);
  });
}

async function settled(): Promise<void> {
  // The read has gone out and come back.
  await waitFor(() => {
    expect(reads).toBeGreaterThan(0);
  });
  await new Promise((resolve) => setTimeout(resolve, 100));
}

describe('a claim storage chart', () => {
  it('draws used and capacity as one chart', async () => {
    stub({ [METRICS]: ok([used, capacity]) });
    const container = mount();
    await waitFor(() => {
      expect(container.querySelector('svg[role="img"]')).not.toBeNull();
    });
    expect(container.querySelectorAll('polyline')).toHaveLength(2);
    const text = container.textContent ?? '';
    expect(text).toContain('Used');
    expect(text).toContain('Capacity');
    expect(text).toContain('Storage');
  });

  it.each([
    ['an absent answer', { status: 200, body: { state: 'absent', reason: { code: 'not_installed', message: 'no prometheus' } } }],
    ['an unreachable answer', { status: 200, body: { state: 'unreachable', reason: { code: 'cluster_unreachable', message: 'boom' } } }],
    ['a server error', { status: 500, body: 'oops', contentType: 'text/plain' }],
    ['an answer with no series', ok([])],
    ['an answer whose lines hold no sample', ok([{ ...used, data: { ...used.data, values: [null, null] } }])],
  ] as const)('draws nothing for %s', async (_name, reply) => {
    stub({ [METRICS]: reply });
    const container = mount();
    await settled();
    expect(container.innerHTML).toBe('');
  });
});
