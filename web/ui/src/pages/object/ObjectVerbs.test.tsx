import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { stubFetch } from '../../testing/harness';
import { ObjectVerbs } from './ObjectVerbs';

afterEach(() => {
  vi.unstubAllGlobals();
});

const present = (name: string) => ({
  body: { state: 'ok', data: { kind: 'cronjobs', namespace: 'kc', name, title: 'CronJob', tabs: [], sections: [], actions: [] } },
});
const absent = { body: { state: 'absent', reason: { code: 'not_found', message: 'gone' } } };

/**
 * `onGone` is "the object read before is now absent". A route component is
 * reused from one object to the next, so having read A says nothing about B:
 * B read absent first is not B gone.
 */
describe('ObjectVerbs leaves an object only once that object was read', () => {
  it('does not call onGone for an object first read as absent after another one was read', async () => {
    stubFetch({
      '/api/capabilities': { body: { mode: 'desktop' } },
      '/api/c/prod/r/cronjobs/kc/a': present('a'),
      '/api/c/prod/r/cronjobs/kc/b': absent,
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onGone = vi.fn();
    const at = (name: string): ReactNode => (
      <QueryClientProvider client={client}>
        <ObjectVerbs cluster="prod" kind="cronjobs" namespace="kc" name={name} onGone={onGone} />
      </QueryClientProvider>
    );
    const view = render(at('a'));
    await waitFor(() => {
      expect(client.getQueryCache().findAll().some((q) => q.state.status === 'success' && JSON.stringify(q.queryKey).includes('"a"'))).toBe(true);
    });
    view.rerender(at('b'));
    await waitFor(() => {
      expect(client.getQueryCache().findAll().some((q) => q.state.status === 'success' && JSON.stringify(q.queryKey).includes('"b"'))).toBe(true);
    });
    // One more turn for the effect that would have fired.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(onGone).not.toHaveBeenCalled();
  });

  it('calls onGone once the object it read is read as absent', async () => {
    let gone = false;
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const reply =
        path === '/api/capabilities' ? { mode: 'desktop' } : gone ? absent.body : present('a').body;
      return Promise.resolve(new Response(JSON.stringify(reply), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onGone = vi.fn();
    render(
      <QueryClientProvider client={client}>
        <ObjectVerbs cluster="prod" kind="cronjobs" namespace="kc" name="a" onGone={onGone} />
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(client.getQueryCache().findAll().some((q) => q.state.status === 'success' && JSON.stringify(q.queryKey).includes('"a"'))).toBe(true);
    });
    gone = true;
    await client.invalidateQueries();
    await waitFor(() => {
      expect(onGone).toHaveBeenCalledTimes(1);
    });
  });
});
