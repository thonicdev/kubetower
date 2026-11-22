import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { PodView } from './PodView';

afterEach(() => {
  vi.unstubAllGlobals();
});

function mount(status: number, body: unknown): void {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <PodView cluster="kind" namespace="shop" name="api" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("a pod's Pending view when the pod is not there", () => {
  it('says it in one short line, with no code, and the sentence on hover', async () => {
    mount(404, { code: 'no_such_pod', message: 'There is no pod shop/api in this cluster any more.' });
    const note = await screen.findByText('Pod no longer exists');
    expect(note.getAttribute('title')).toContain('any more');
    expect(document.body.textContent).not.toMatch(/404|no_such_pod|request failed/i);
  });

  it('keeps a failed read a failure', async () => {
    mount(502, { code: 'list_failed', message: 'The cluster did not answer.' });
    expect((await screen.findByRole('alert')).textContent).toContain('The request failed (502)');
    expect(screen.queryByText('Pod no longer exists')).toBeNull();
  });
});
