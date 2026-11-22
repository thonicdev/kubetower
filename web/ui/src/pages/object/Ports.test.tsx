import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Forward } from '../../api';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

afterEach(() => {
  vi.unstubAllGlobals();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};
const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

function object(path: string, data: Record<string, unknown>): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'container', portForward: true } },
    [path]: { body: { state: 'ok', data: { tabs: [], sections: [], ...data } } },
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

/**
 * A forwarder with state, over the path-only stub.
 *
 * `stubFetch` answers by path alone, and `/api/forwards` answers three verbs on
 * one path whose list has to change under the panel when a forward is closed:
 * the test that Stop works is the test that the *list* no longer has it, not
 * that a request was sent somewhere.
 */
function stubForwarder(
  base: StubRoutes,
  live: Forward[],
  onOpen?: Forward,
): { live: Forward[]; calls: string[]; posted: Record<string, unknown>[] } {
  stubFetch(base);
  const stubbed = globalThis.fetch;
  const state = { live: [...live], calls: [] as string[], posted: [] as Record<string, unknown>[] };
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(
      typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url,
    );
    const method = init?.method ?? 'GET';
    state.calls.push(`${method} ${path}`);
    if (path === '/api/forwards' && method === 'GET') {
      return Promise.resolve(
        new Response(JSON.stringify(state.live), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    if (path === '/api/forwards' && method === 'POST' && onOpen !== undefined) {
      state.posted.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
      state.live = [...state.live, onOpen];
      return Promise.resolve(
        new Response(JSON.stringify(onOpen), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    const closing = /^\/api\/forwards\/([^/]+)$/.exec(path);
    if (closing !== null && method === 'DELETE') {
      state.live = state.live.filter((one) => one.id !== closing[1]);
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return stubbed(input, init);
  });
  return state;
}

function control(text: string): HTMLButtonElement {
  const found = [...page().querySelectorAll('button')].find((one) =>
    one.textContent?.includes(text),
  );
  if (found === undefined) throw new Error(`no control saying ${text}`);
  return found;
}

/**
 * Port forwarding from the panel: `openForward`, `listForwards` and
 * `closeForward` each have a caller - a button, the list on mount, a Stop - and
 * a port that cannot be forwarded is drawn as a refusal rather than left out.
 */
describe('the ports panel', () => {
  const POD = '/api/c/prod/r/pods/argo/api-0';
  const pod = {
    kind: 'pods',
    namespace: 'argo',
    name: 'api-0',
    title: 'Pod',
    ports: [
      { name: 'http', port: 8080, protocol: 'TCP', container: 'api', forwardable: true },
      {
        name: 'metrics',
        port: 9125,
        protocol: 'UDP',
        container: 'api',
        reason: {
          code: 'not_supported_here',
          message: 'port-forwarding is TCP, and this port is UDP',
        },
      },
    ],
  };

  it('lists what the object exposes', async () => {
    stubFetch(object(POD, pod));
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('http');
    expect(text).toContain('9125');
  });

  it('offers the control disabled, with why, on a port it cannot forward', async () => {
    stubFetch(object(POD, pod));
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });
    const buttons = [...page().querySelectorAll('button')].filter((one) =>
      one.textContent?.includes('Forward'),
    );
    expect(buttons).toHaveLength(2);
    // The TCP one works and the UDP one is disabled and says so - never absent,
    // because a user looking for the control has to find it.
    expect(buttons[0]?.hasAttribute('disabled')).toBe(false);
    expect(buttons[1]?.hasAttribute('disabled')).toBe(true);
    expect(page().textContent).toContain('port-forwarding is TCP');
  });

  // The forwarder refuses a cluster marked read-only, and
  // the server says so on the port, so the control is drawn disabled carrying
  // the marking's sentence - never a button whose click is then refused.
  it('offers the control disabled, with the marking, on a read-only cluster', async () => {
    const marked =
      'prod is marked read-only in KT_READONLY_CLUSTERS, so port-forwarding to it is refused, as kubectl port-forward is in its terminal';
    stubFetch(
      object(POD, {
        ...pod,
        ports: [
          {
            name: 'http',
            port: 8080,
            protocol: 'TCP',
            container: 'api',
            forwardable: false,
            reason: { code: 'verb_denied', message: marked, retryable: false },
          },
        ],
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });
    const button = control('Forward');
    expect(button.hasAttribute('disabled')).toBe(true);
    expect(button.getAttribute('title')).toBe(marked);
    expect(page().textContent).toContain('marked read-only in KT_READONLY_CLUSTERS');
  });

  // And a click that reaches the server anyway - a drawer opened before a
  // restart that marked the cluster - draws the refusal in place.
  it('draws the read-only refusal in place when a forward is refused', async () => {
    stubFetch({
      ...object(POD, pod),
      '/api/forwards': {
        status: 403,
        contentType: 'application/problem+json',
        body: {
          type: 'about:blank',
          title: 'Refused',
          status: 403,
          detail:
            'prod is marked read-only in KT_READONLY_CLUSTERS, so port-forwarding to it is refused, as kubectl port-forward is in its terminal',
          code: 'read_only',
        },
      },
    });
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });
    fireEvent.click(control('Forward'));

    await waitFor(() => {
      expect(page().querySelector('[role="alert"]')?.textContent).toContain('marked read-only');
    });
  });

  it('sends a pod, and turns the button into the address the server answered', async () => {
    const forwarder = stubForwarder(object(POD, pod), [], {
      id: 'f1',
      cluster: 'prod',
      namespace: 'argo',
      pod: 'api-0',
      remotePort: 8080,
      address: '127.0.0.1:51234',
      scheme: 'http',
    });

    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });
    fireEvent.click(control('Forward'));

    await waitFor(() => {
      expect(page().textContent).toContain('127.0.0.1:51234');
    });
    // A forward is not a tab: the button became the address, with the scheme
    // the server chose.
    const link = [...page().querySelectorAll('a')].find((one) =>
      one.textContent?.includes('51234'),
    );
    expect(link?.getAttribute('href')).toBe('http://127.0.0.1:51234');

    expect(forwarder.calls).toContain('POST /api/forwards');
    expect(forwarder.posted[0]).toMatchObject({
      cluster: 'prod',
      namespace: 'argo',
      pod: 'api-0',
      remotePort: 8080,
      portName: 'http',
    });
    // And no service, because exactly one of the two names the target.
    expect(forwarder.posted[0]).not.toHaveProperty('service');
  });

  it('sends a service where the object is one, and honors the server scheme', async () => {
    const SVC = '/api/c/prod/r/services/argo/api';
    const forwarder = stubForwarder(
      object(SVC, {
        kind: 'services',
        namespace: 'argo',
        name: 'api',
        title: 'Service',
        ports: [
          {
            name: 'https',
            port: 443,
            protocol: 'TCP',
            targetPort: 'http',
            appProtocol: 'https',
            forwardable: true,
          },
        ],
      }),
      [],
      {
        id: 'f2',
        cluster: 'prod',
        namespace: 'argo',
        pod: 'api-7f9c',
        service: 'api',
        remotePort: 443,
        address: '127.0.0.1:51235',
        scheme: 'https',
      },
    );

    mountApp('/c/prod/services/argo/api');
    await waitFor(() => {
      expect(page().textContent).toContain('443');
    });
    fireEvent.click(control('Forward'));

    await waitFor(() => {
      expect(forwarder.posted).toHaveLength(1);
    });
    // A Service forward survives the pod being replaced under it, which is what
    // a rollout does while somebody is watching the tunnel.
    expect(forwarder.posted[0]).toMatchObject({ service: 'api', remotePort: 443, appProtocol: 'https' });
    expect(forwarder.posted[0]).not.toHaveProperty('pod');

    // The scheme is the server's: `http` in front of an HTTPS port produces
    // "The plain HTTP request was sent to an HTTPS port", which reads as a
    // broken tunnel rather than as a wrong link.
    await waitFor(() => {
      const link = [...page().querySelectorAll('a')].find((one) =>
        one.textContent?.includes('51235'),
      );
      expect(link?.getAttribute('href')).toBe('https://127.0.0.1:51235');
    });
  });

  it('draws the server sentence when a forward is refused', async () => {
    stubFetch({
      ...object(POD, pod),
      '/api/forwards': {
        status: 409,
        contentType: 'application/problem+json',
        body: {
          type: 'about:blank',
          title: 'Limit reached',
          status: 409,
          detail: 'this console holds four forwards already; close one first',
          code: 'limit_reached',
        },
      },
    });
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });
    fireEvent.click(control('Forward'));

    await waitFor(() => {
      expect(page().textContent).toContain('holds four forwards already');
    });
  });
});

/**
 * The way back. Without a Stop, a forward would outlive the drawer and the tab
 * until the process exited - and the cap is per process, so the slot it held
 * would be gone with it.
 */
describe('stopping a forward', () => {
  const POD = '/api/c/prod/r/pods/argo/api-0';
  const pod = {
    kind: 'pods',
    namespace: 'argo',
    name: 'api-0',
    title: 'Pod',
    ports: [{ name: 'http', port: 8080, protocol: 'TCP', container: 'api', forwardable: true }],
  };
  const opened: Forward = {
    id: 'f1',
    cluster: 'prod',
    namespace: 'argo',
    pod: 'api-0',
    remotePort: 8080,
    address: '127.0.0.1:51234',
    scheme: 'http',
  };

  it('closes the forward it opened, and the button is a button again', async () => {
    const forwarder = stubForwarder(object(POD, pod), [], opened);
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().textContent).toContain('8080');
    });

    fireEvent.click(control('Forward'));
    await waitFor(() => {
      expect(page().textContent).toContain('127.0.0.1:51234');
    });
    expect(forwarder.live.map((one) => one.id)).toEqual(['f1']);

    fireEvent.click(control('Stop'));

    // `GET /api/forwards` no longer lists it, and the row went back to offering
    // the control rather than the address.
    await waitFor(() => {
      expect(forwarder.live).toEqual([]);
    });
    expect(forwarder.calls).toContain('DELETE /api/forwards/f1');
    await waitFor(() => {
      expect(page().textContent).not.toContain('127.0.0.1:51234');
      expect(control('Forward').hasAttribute('disabled')).toBe(false);
    });
  });

  it('shows a forward opened elsewhere beside its port, and stops it here', async () => {
    const forwarder = stubForwarder(object(POD, pod), [
      opened,
      // A Service forward that happened to land on this pod. It belongs to the
      // Service's row, not to this one: matching on `pod` alone would draw the
      // wrong address here and offer to stop somebody else's tunnel.
      {
        id: 'f9',
        cluster: 'prod',
        namespace: 'argo',
        pod: 'api-0',
        service: 'api',
        remotePort: 8080,
        address: '127.0.0.1:60000',
        scheme: 'http',
      },
      // Another pod's forward on the same port number, which is not this row's.
      {
        id: 'f2',
        cluster: 'prod',
        namespace: 'argo',
        pod: 'api-1',
        remotePort: 8080,
        address: '127.0.0.1:52000',
        scheme: 'http',
      },
    ]);
    mountApp('/c/prod/pods/argo/api-0');

    // Nothing was clicked: the address is there because the panel asked what
    // this process already holds.
    await waitFor(() => {
      expect(page().textContent).toContain('127.0.0.1:51234');
    });
    expect(page().textContent).not.toContain('60000');
    expect(page().textContent).not.toContain('52000');
    expect(forwarder.calls).toContain('GET /api/forwards');

    fireEvent.click(control('Stop'));
    await waitFor(() => {
      expect(forwarder.live.map((one) => one.id)).toEqual(['f9', 'f2']);
    });
    await waitFor(() => {
      expect(control('Forward').hasAttribute('disabled')).toBe(false);
    });
  });

  it('recognizes a service forward by the service it was opened on', async () => {
    const SVC = '/api/c/prod/r/services/argo/api';
    stubForwarder(
      object(SVC, {
        kind: 'services',
        namespace: 'argo',
        name: 'api',
        title: 'Service',
        ports: [{ name: 'https', port: 443, protocol: 'TCP', appProtocol: 'https', forwardable: true }],
      }),
      [
        {
          id: 'f3',
          cluster: 'prod',
          namespace: 'argo',
          // The replica the tunnel landed on, which is not the row's name and
          // changes under a rollout; `service` is what the row is recognized by.
          pod: 'api-7f9c',
          service: 'api',
          remotePort: 443,
          address: '127.0.0.1:51235',
          scheme: 'https',
        },
      ],
    );
    mountApp('/c/prod/services/argo/api');

    await waitFor(() => {
      expect(page().textContent).toContain('127.0.0.1:51235');
    });
    const link = [...page().querySelectorAll('a')].find((one) =>
      one.textContent?.includes('51235'),
    );
    expect(link?.getAttribute('href')).toBe('https://127.0.0.1:51235');
  });

  it('draws the server sentence when a stop is refused, and keeps the address', async () => {
    stubForwarder(object(POD, pod), [opened]);
    // The stateful layer answers DELETE itself; put the refusal in front of it
    // so the panel hears the server's own sentence.
    const layered = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) =>
      init?.method === 'DELETE'
        ? Promise.resolve(
            new Response(
              JSON.stringify({
                type: 'about:blank',
                title: 'Not Found',
                status: 404,
                detail: 'no port-forward "f1" is open',
                code: 'not_found',
              }),
              { status: 404, headers: { 'Content-Type': 'application/problem+json' } },
            ),
          )
        : layered(input, init),
    );
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().textContent).toContain('127.0.0.1:51234');
    });

    fireEvent.click(control('Stop'));
    await waitFor(() => {
      expect(page().textContent).toContain('no port-forward "f1" is open');
    });
    expect(page().textContent).toContain('127.0.0.1:51234');
  });
});

/**
 * The scheme is the server's guess, and a guess is sometimes wrong: a service
 * on 8443 that guessed `http` is a click that fails. The pair beside the address
 * rewrites the link's scheme client-side; the server still decides the default.
 */
describe('choosing the scheme on a forward address', () => {
  const POD = '/api/c/prod/r/pods/argo/api-0';
  const pod = {
    kind: 'pods',
    namespace: 'argo',
    name: 'api-0',
    title: 'Pod',
    ports: [{ name: 'admin', port: 8443, protocol: 'TCP', container: 'api', forwardable: true }],
  };
  const opened: Forward = {
    id: 'f1',
    cluster: 'prod',
    namespace: 'argo',
    pod: 'api-0',
    remotePort: 8443,
    address: '127.0.0.1:51234',
    scheme: 'http',
  };

  function address(): HTMLAnchorElement {
    const link = [...page().querySelectorAll('a')].find((one) => one.textContent?.includes('51234'));
    if (link === undefined) throw new Error('no address drawn');
    return link;
  }

  function scheme(name: string): HTMLButtonElement {
    const group = page().querySelector('[role="group"][aria-label="Scheme"]');
    if (group === null) throw new Error('no scheme pair');
    const found = [...group.querySelectorAll('button')].find((one) => one.textContent === name);
    if (found === undefined) throw new Error(`no ${name} in the scheme pair`);
    return found;
  }

  it('starts on the scheme the server chose', async () => {
    stubForwarder(object(POD, pod), [{ ...opened, scheme: 'https' }]);
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(address().getAttribute('href')).toBe('https://127.0.0.1:51234');
    });
    expect(scheme('https').getAttribute('aria-pressed')).toBe('true');
    expect(scheme('http').getAttribute('aria-pressed')).toBe('false');
  });

  it('flips the link between http and https, and back', async () => {
    stubForwarder(object(POD, pod), [opened]);
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(address().getAttribute('href')).toBe('http://127.0.0.1:51234');
    });

    fireEvent.click(scheme('https'));
    expect(address().getAttribute('href')).toBe('https://127.0.0.1:51234');
    // The text is the link: what the user reads is what the click opens.
    expect(address().textContent).toBe('https://127.0.0.1:51234');
    expect(scheme('https').getAttribute('aria-pressed')).toBe('true');
    expect(scheme('http').getAttribute('aria-pressed')).toBe('false');

    fireEvent.click(scheme('http'));
    expect(address().getAttribute('href')).toBe('http://127.0.0.1:51234');
  });

  // The other direction of Ports.loopback.test.tsx. jsdom serves this file from
  // `localhost`, as the development loop serves the console (Vite on
  // localhost:5173), and a session cookie set on `localhost` is sent to
  // `localhost` on every port - so here the link keeps the `127.0.0.1` the
  // server answered. Whichever spelling the console is on, the link is on the
  // other one.
  it('never shares a host with the console, so the session cookie stays behind', async () => {
    stubForwarder(object(POD, pod), [opened]);
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(address().getAttribute('href')).toBe('http://127.0.0.1:51234');
    });
    expect(window.location.hostname).toBe('localhost');
    expect(new URL(address().getAttribute('href') ?? '').hostname).not.toBe(
      window.location.hostname,
    );
  });

  it('is not offered before there is an address', async () => {
    stubForwarder(object(POD, pod), []);
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().textContent).toContain('8443');
    });
    expect(page().querySelector('[role="group"][aria-label="Scheme"]')).toBeNull();
  });
});

/**
 * Every declared port, from every container. The panel must not show fewer
 * ports than the pod declares, so this pins what the server sends and what the
 * panel does with it.
 */
describe('a pod with several containers', () => {
  const POD = '/api/c/prod/r/pods/argo/api-0';
  const pod = {
    kind: 'pods',
    namespace: 'argo',
    name: 'api-0',
    title: 'Pod',
    ports: [
      { name: 'http', port: 8080, protocol: 'TCP', container: 'api', forwardable: true },
      // The mesh sidecar, listening on its own port and on a UDP one.
      { name: 'http-envoy-prom', port: 15090, protocol: 'TCP', container: 'istio-proxy', forwardable: true },
      {
        name: 'statsd',
        port: 9125,
        protocol: 'UDP',
        container: 'istio-proxy',
        reason: { code: 'not_supported_here', message: 'port-forwarding is TCP, and this port is UDP' },
      },
    ],
  };

  it('draws a row per declared port, each saying whose it is', async () => {
    stubForwarder(object(POD, pod), []);
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().textContent).toContain('15090');
    });

    const rows = [...page().querySelectorAll('li')].filter((one) =>
      /8080|15090|9125/.test(one.textContent ?? ''),
    );
    expect(rows).toHaveLength(3);
    expect(rows[0]?.textContent).toContain('api');
    expect(rows[1]?.textContent).toContain('istio-proxy');
    expect(rows[2]?.textContent).toContain('istio-proxy');

    // The UDP port is listed and refused with its reason, never left out.
    const buttons = [...page().querySelectorAll('button')].filter((one) =>
      one.textContent?.includes('Forward'),
    );
    expect(buttons).toHaveLength(3);
    expect(buttons[0]?.hasAttribute('disabled')).toBe(false);
    expect(buttons[1]?.hasAttribute('disabled')).toBe(false);
    expect(buttons[2]?.hasAttribute('disabled')).toBe(true);
    expect(rows[2]?.textContent).toContain('this port is UDP');
  });
});

/**
 * Where the console cannot forward for you - the server profile, in a pod - the
 * row draws the `kubectl port-forward` command instead.
 *
 * A forward binds a port inside the console's process and the link is
 * `127.0.0.1:<port>`; from a browser talking to a pod that is the viewer's own
 * machine, where nothing listens. `capabilities.portForward: false` is what
 * says so, and the three forward operations are not registered there, so a
 * panel that asked for the list would be sending a 404 from every drawer.
 */
describe('the ports panel, where the console cannot forward', () => {
  const POD = '/api/c/prod/r/pods/argo/api-0';
  const SVC = '/api/c/prod/r/services/argo/api';

  function commandRoutes(path: string, data: Record<string, unknown>): StubRoutes {
    return {
      ...object(path, data),
      '/api/capabilities': { body: { mode: 'shared', portForward: false } },
    };
  }

  it('draws the command for a pod, and never asks for the forwards', async () => {
    const forwarder = stubForwarder(
      commandRoutes(POD, {
        kind: 'pods',
        namespace: 'argo',
        name: 'api-0',
        title: 'Pod',
        ports: [
          { name: 'http', port: 8080, protocol: 'TCP', container: 'api', forwardable: true },
          {
            name: 'metrics',
            port: 9125,
            protocol: 'UDP',
            container: 'api',
            reason: { code: 'not_supported_here', message: 'port-forwarding is TCP, and this port is UDP' },
          },
        ],
      }),
      [],
    );
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().textContent).toContain('kubectl port-forward -n argo pod/api-0 8080:8080');
    });
    // No control that would bind a port in the pod.
    const forwardButtons = [...page().querySelectorAll('button')].filter((one) =>
      one.textContent?.includes('Forward'),
    );
    expect(forwardButtons).toHaveLength(0);
    // Something to copy it with, named for what it copies.
    expect(page().querySelector('[role="group"][aria-label^="kubectl port-forward"]')).not.toBeNull();
    // The UDP port has no command - kubectl cannot forward it either - and
    // keeps its reason.
    expect(page().textContent).not.toContain('9125:9125');
    expect(page().textContent).toContain('this port is UDP');
    expect(forwarder.calls.filter((call) => call.includes('/api/forwards'))).toEqual([]);
  });

  it('names a service as svc/, and moves a privileged port up by 8000', async () => {
    stubForwarder(
      commandRoutes(SVC, {
        kind: 'services',
        namespace: 'argo',
        name: 'api',
        title: 'Service',
        ports: [{ name: 'https', port: 443, protocol: 'TCP', targetPort: '8443', forwardable: true }],
      }),
      [],
    );
    mountApp('/c/prod/services/argo/api');

    await waitFor(() => {
      expect(page().textContent).toContain('kubectl port-forward -n argo svc/api 8443:443');
    });
  });
});
