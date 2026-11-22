import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { RefreshApplication } from './RefreshApplication';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const PATH = '/x/kind/argocd/applications/argocd/guestbook/refresh?confirm=confirm';

function stub(reply: () => Response): { url: string; method: string }[] {
  const asked: { url: string; method: string }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      asked.push({ url: typeof input === 'string' ? input : input.toString(), method: init?.method ?? 'GET' });
      return Promise.resolve(reply());
    }),
  );
  return asked;
}

function mount(): void {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RefreshApplication cluster="kind" namespace="argocd" name="guestbook" />
    </QueryClientProvider>,
  );
}

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json; charset=utf-8' } });

describe('Refresh', () => {
  it('asks nothing before the confirmation, and Cancel asks nothing either', () => {
    const asked = stub(() => json(200, { refreshed: true }));
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(asked).toHaveLength(0);
    expect(screen.getByRole('dialog')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(asked).toHaveLength(0);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('sends one POST carrying the confirmation word once confirmed', async () => {
    const asked = stub(() => json(200, { refreshed: true }));
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Refresh' }));
    await waitFor(() => {
      expect(asked).toEqual([{ url: PATH, method: 'POST' }]);
    });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it("says the console's refusal under the button", async () => {
    stub(() =>
      json(403, { code: 'refresh_denied', message: 'The cluster does not allow you to refresh this Application.', detail: 'patch denied' }),
    );
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Refresh' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The cluster does not allow you to refresh this Application.');
  });
});
