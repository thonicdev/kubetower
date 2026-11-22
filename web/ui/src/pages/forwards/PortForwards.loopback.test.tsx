// @vitest-environment-options {"url":"http://127.0.0.1:5823/"}
import { cleanup, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Forward } from '../../api';
import { mountApp, stubFetch } from '../../testing/harness';

/**
 * The list of open forwards, on the console as the desktop serves it:
 * `127.0.0.1:5823`, the launcher's address.
 *
 * The Ports panel's `Ports.loopback.test.tsx` has the argument: a cookie does not isolate by port, so a link to a
 * forward on the console's own host hands the session to whatever listens
 * behind the tunnel. This page draws the same addresses and must spell them
 * the same way. Its own file because the address is the jsdom document's, set
 * once per file by the line above; `PortForwards.test.tsx` runs on jsdom's
 * default, `localhost`, and pins the other direction.
 */

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const linked: Forward = {
  id: 'f-http',
  cluster: 'prod',
  namespace: 'argo',
  pod: 'api-0',
  remotePort: 8080,
  address: '127.0.0.1:51234',
  scheme: 'http',
};
const plain: Forward = {
  id: 'f-raw',
  cluster: 'prod',
  namespace: 'argo',
  pod: 'db-0',
  remotePort: 5432,
  address: '127.0.0.1:51235',
};

function cell(id: string): Element | null {
  const row = document.querySelector(`#page [data-forward="${id}"]`)?.closest('tr');
  return row?.querySelector('td:nth-child(4)') ?? null;
}

describe('the list of open port-forwards, on a console served from 127.0.0.1', () => {
  it('links and draws each address under localhost, never the cookie host', async () => {
    stubFetch({
      '/api/session': {
        body: {
          identity: 'alice',
          idleExpiresAt: '2999-01-01T00:00:00Z',
          absoluteExpiresAt: '2999-01-01T00:00:00Z',
        },
      },
      '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/capabilities': { body: { mode: 'desktop', portForward: true } },
      '/api/forwards': { body: [linked, plain] },
    });
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(cell('f-http')).not.toBeNull();
    });
    expect(window.location.hostname).toBe('127.0.0.1');
    expect(cell('f-http')?.querySelector('a')?.getAttribute('href')).toBe('http://localhost:51234');
    expect(cell('f-raw')?.textContent).toBe('localhost:51235');
    expect(document.getElementById('page')?.innerHTML).not.toContain('127.0.0.1:5123');
  });
});
