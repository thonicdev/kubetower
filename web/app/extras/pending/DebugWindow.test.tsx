import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import DebugWindow from './DebugWindow';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Pod Debugging's window", () => {
  it("draws the pod's view and leads to every pending pod", async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(new Response(JSON.stringify({ pending: false, phase: 'Running' }), { status: 200, headers: { 'Content-Type': 'application/json' } }))),
    );
    const router = createMemoryRouter([{ path: '/', element: <DebugWindow cluster="prod" kind="pods" namespace="shop" name="api-1" /> }]);
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    expect((await screen.findByText('All pending pods →')).getAttribute('href')).toBe('/c/prod/x/pending');
    expect(document.querySelector('[data-pending-window]')).not.toBeNull();
  });
});
