// @vitest-environment-options {"url":"http://127.0.0.1:5823/"}
import { waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Forward } from '../../api';
import { mountApp, stubFetch } from '../../testing/harness';

/**
 * The console as the desktop serves it: on `127.0.0.1:5823`, which is the
 * address the launcher opens and `KT_LISTEN`'s default.
 *
 * **A cookie does not isolate by port** (RFC 6265 §8.5). The session cookie set
 * by `127.0.0.1:5823` is sent by the browser to `127.0.0.1` on every port, so a
 * forward link written as `127.0.0.1:<port>` hands the console's session to
 * whatever listens behind the tunnel - a pod's own web server, logging request
 * headers, would hold a cookie that opens every cluster on this machine. The
 * same listener reached as `localhost` is a different cookie host, so the
 * cookie is not sent.
 *
 * Its own file because the address is the jsdom document's, set once per file
 * by the line above; the rest of the panel's tests run on jsdom's default,
 * `localhost`, and pin the other direction.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const POD = '/api/c/prod/r/pods/argo/api-0';

const opened: Forward = {
  id: 'f1',
  cluster: 'prod',
  namespace: 'argo',
  pod: 'api-0',
  remotePort: 8080,
  address: '127.0.0.1:51234',
  scheme: 'http',
};

function stub(): void {
  stubFetch({
    '/api/session': {
      body: {
        identity: 'alice',
        idleExpiresAt: '2999-01-01T00:00:00Z',
        absoluteExpiresAt: '2999-01-01T00:00:00Z',
      },
    },
    '/api/clusters': {
      body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] },
    },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop', portForward: true } },
    '/api/forwards': { body: [opened] },
    [POD]: {
      body: {
        state: 'ok',
        data: {
          tabs: [],
          sections: [],
          kind: 'pods',
          namespace: 'argo',
          name: 'api-0',
          title: 'Pod',
          ports: [{ name: 'http', port: 8080, protocol: 'TCP', container: 'api', forwardable: true }],
        },
      },
    },
  });
}

function forwardLink(): HTMLAnchorElement {
  const page = document.getElementById('page');
  const link = [...(page?.querySelectorAll('a') ?? [])].find((one) =>
    one.textContent?.includes('51234'),
  );
  if (link === undefined) throw new Error('no forward address drawn');
  return link;
}

describe('a forward link, on a console served from 127.0.0.1', () => {
  it('is written under a host the console does not use, so its cookie is not sent', async () => {
    expect(window.location.hostname).toBe('127.0.0.1');
    stub();
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(forwardLink().getAttribute('href')).toBe('http://localhost:51234');
    });
    const href = forwardLink().getAttribute('href') ?? '';
    expect(new URL(href).hostname).not.toBe(window.location.hostname);
    // The port is the tunnel's, untouched: only the spelling of the host moves.
    expect(new URL(href).port).toBe('51234');
    // What the user reads is what the click opens.
    expect(forwardLink().textContent).toBe('http://localhost:51234');
  });
});
