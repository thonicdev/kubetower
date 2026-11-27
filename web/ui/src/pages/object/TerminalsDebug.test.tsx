import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { rememberNoShell } from '../../debug/noShellMemory';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetLiveHandles } from '../../live/handles';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

/**
 * Debug on a pod's row (#147): the Shell menu's debug form, the Debug control
 * beside Shell where a container probably has no shell, the dock tab a Start
 * opens, and how the page's `debug` offer gates all of it.
 */

/** Every socket the pane opened, by its address. */
const sockets: string[] = [];

beforeEach(() => {
  sockets.length = 0;
  vi.stubGlobal('matchMedia', () => ({
    matches: false,
    media: '',
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
    onchange: null,
  }));
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    },
  );
  vi.stubGlobal(
    'WebSocket',
    class {
      static readonly OPEN = 1;
      readyState = 0;
      constructor(url: string) {
        sockets.push(url);
      }
      close(): void {}
      send(): void {}
    },
  );
});

afterEach(() => {
  resetKeyboardStack();
  resetLiveHandles();
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

const POD = '/api/c/prod/r/pods/pay/api-0';
const running = { state: 'running', ready: true, restarts: 0 };
const DISTROLESS = { name: 'api', image: 'gcr.io/distroless/static-debian12:nonroot', init: false, status: running };
const NGINX = { name: 'web', image: 'docker.io/library/nginx:1.27.2', init: false, status: running };
const ENVOY = { name: 'envoy', image: 'docker.io/envoyproxy/envoy:v1.31.2', init: false, status: running };

function capabilities(mode: 'desktop' | 'shared' = 'desktop', podExec = true): StubRoutes[string] {
  return {
    body: {
      mode,
      localShell: true,
      podExec,
      nodeShell: false,
      assistant: false,
      portForward: true,
      readOnly: { writesRefused: false, detailWritesRefused: false, measured: true },
    },
  };
}

function podPage(containers: unknown[], extra: Record<string, unknown> = { debug: { permitted: true } }): StubRoutes[string] {
  return {
    body: {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: 'pay',
        name: 'api-0',
        title: 'Pod',
        tabs: [],
        sections: [],
        actions: [],
        containers,
        ...extra,
      },
    },
  };
}

function stub(page: StubRoutes[string], able: StubRoutes[string] = capabilities()): void {
  stubFetch({
    '/api/session': {
      body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
    },
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': able,
    [POD]: page,
  });
}

function terminals(): HTMLElement {
  const element = document.querySelector('[aria-label="Terminals"]');
  if (element === null) throw new Error('no terminal controls');
  return element as HTMLElement;
}

/** The group, once the pod has answered: Shell is no longer waiting on it. */
async function ready(): Promise<void> {
  await waitFor(() => {
    const shell = [...terminals().querySelectorAll('button')].find((one) => one.textContent === 'Shell');
    expect(shell).toBeDefined();
    expect(shell?.title.startsWith('Still reading')).toBe(false);
  });
}

function debugButton(): HTMLButtonElement | null {
  return terminals().querySelector<HTMLButtonElement>('button[data-debug-entry]');
}

function shellMenu(): HTMLButtonElement {
  const found = terminals().querySelector<HTMLButtonElement>('button[aria-label="Shell menu"]');
  if (found === null) throw new Error('no Shell menu control');
  return found;
}

function press(form: HTMLElement, name: string): void {
  const found = [...form.querySelectorAll('button')].find((one) => one.textContent?.trim() === name);
  if (found === undefined) throw new Error(`no ${name} in the form`);
  fireEvent.click(found);
}

function form(): HTMLElement {
  const found = document.querySelector<HTMLElement>('[data-debug-form]');
  if (found === null) throw new Error('no debug form');
  return found;
}

async function debugSocket(): Promise<URL> {
  return waitFor(() => {
    const found = sockets.find((one) => one.includes('/ws/debug'));
    if (found === undefined) throw new Error('no debug socket yet');
    return new URL(found);
  });
}

describe('the Debug control beside Shell', () => {
  it('is drawn where a running container probably has no shell, and not on a pod whose containers have one', async () => {
    stub(podPage([DISTROLESS, ENVOY]));
    const first = mountApp('/c/prod/pods/pay/api-0');
    await ready();
    await waitFor(() => {
      expect(debugButton()).not.toBeNull();
    });
    first.view.unmount();

    stub(podPage([NGINX, ENVOY]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    expect(debugButton()).toBeNull();
  });

  it('is never drawn for an ephemeral container, which is the debug container itself', async () => {
    stub(podPage([NGINX, { ...DISTROLESS, name: 'kt-debug-abcde', ephemeral: true, target: 'web' }]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    expect(debugButton()).toBeNull();
  });

  it('leaves debug containers out of the Shell picker, so a crashing app container still says so', async () => {
    const crashing = { ...NGINX, status: { state: 'waiting', ready: false, restarts: 4, reason: 'CrashLoopBackOff' } };
    const debugContainer = { name: 'kt-debug-abcde', image: 'busybox:1.36', init: false, ephemeral: true, target: 'web', status: running };
    stub(podPage([crashing, ENVOY, debugContainer]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    fireEvent.click(shellMenu());
    const listed = [...document.querySelectorAll('ul[aria-label="Container"] li')].map((one) => one.textContent ?? '');
    expect(listed.some((one) => one.includes('envoy'))).toBe(true);
    expect(listed.some((one) => one.includes('kt-debug-abcde'))).toBe(false);
  });

  it('is drawn once a Shell on that container found no shell', async () => {
    rememberNoShell({ cluster: 'prod', namespace: 'pay', pod: 'api-0', container: 'web', image: NGINX.image });
    stub(podPage([NGINX]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    await waitFor(() => {
      expect(debugButton()).not.toBeNull();
    });
  });

  it('opens the form with the container that has no shell, and Start opens a debug tab on it', async () => {
    stub(podPage([ENVOY, DISTROLESS]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    await waitFor(() => {
      expect(debugButton()).not.toBeNull();
    });
    fireEvent.click(debugButton() as HTMLButtonElement);
    const target = [...form().querySelectorAll('button[aria-pressed="true"]')].map((one) => one.textContent);
    expect(target).toEqual(['busybox', 'api']);
    // Never an ephemeral container, never an init one: the pod's own running ones.
    expect(form().textContent).toContain('envoy');
    expect(form().textContent).toContain('stays in pod');

    press(form(), 'Start');
    const url = await debugSocket();
    expect(url.pathname).toBe('/api/c/prod/ws/debug');
    expect(Object.fromEntries(url.searchParams)).toEqual({
      namespace: 'pay',
      pod: 'api-0',
      image: 'busybox:1.36',
      target: 'api',
    });
    await waitFor(() => {
      expect(document.querySelector('#dock [role="tab"]')?.textContent).toContain('debug · api-0 · busybox');
    });
  });
});

describe('the Shell menu', () => {
  it('opens on the chevron even with one container, and carries the debug form', async () => {
    stub(podPage([NGINX]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    fireEvent.click(shellMenu());
    await waitFor(() => {
      expect(document.querySelector('ul[aria-label="Container"]')?.textContent).toContain('web');
    });
    const section = document.querySelector('[data-debug-in-menu]');
    expect(section?.textContent).toContain('Debug container');
    expect(section?.querySelector('[data-debug-form]')).not.toBeNull();
  });

  it('starts netshoot with no target', async () => {
    stub(podPage([NGINX, ENVOY]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    fireEvent.click(shellMenu());
    await waitFor(() => {
      expect(document.querySelector('[data-debug-form]')).not.toBeNull();
    });
    press(form(), 'netshoot');
    press(form(), 'none');
    press(form(), 'Start');
    const url = await debugSocket();
    expect(url.searchParams.get('image')).toBe('nicolaka/netshoot:v0.13');
    expect(url.searchParams.has('target')).toBe(false);
  });

  it('takes a free reference under Other, and refuses one with a space', async () => {
    stub(podPage([NGINX, ENVOY]));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    fireEvent.click(shellMenu());
    await waitFor(() => {
      expect(document.querySelector('[data-debug-form]')).not.toBeNull();
    });
    press(form(), 'Other');
    const start = [...form().querySelectorAll('button')].find((one) => one.textContent?.trim() === 'Start');
    expect(start?.disabled).toBe(true);
    const input = form().querySelector('input') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'alpine 3.20' } });
    expect(start?.disabled).toBe(true);
    expect(input.getAttribute('aria-invalid')).toBe('true');
    fireEvent.change(input, { target: { value: 'alpine:3.20' } });
    expect(start?.disabled).toBe(false);
    press(form(), 'envoy');
    press(form(), 'Start');
    const url = await debugSocket();
    expect(url.searchParams.get('image')).toBe('alpine:3.20');
    expect(url.searchParams.get('target')).toBe('envoy');
  });
});

describe('the page decides whether Debug is offered', () => {
  it('grays the control and the menu section with the server\'s sentence where it is refused', async () => {
    stub(
      podPage([DISTROLESS], {
        debug: { permitted: false, reason: { code: 'verb_denied', message: 'prod is marked read-only' } },
      }),
    );
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    await waitFor(() => {
      expect(debugButton()).not.toBeNull();
    });
    expect(debugButton()?.disabled).toBe(true);
    expect(debugButton()?.title).toBe('prod is marked read-only');

    fireEvent.click(shellMenu());
    await waitFor(() => {
      expect(document.querySelector('[data-debug-in-menu] fieldset')).not.toBeNull();
    });
    const fieldset = document.querySelector<HTMLFieldSetElement>('[data-debug-in-menu] fieldset');
    expect(fieldset?.disabled).toBe(true);
    expect(fieldset?.title).toBe('prod is marked read-only');
  });

  it('draws nothing of Debug where the page withholds it', async () => {
    stub(podPage([DISTROLESS], {}));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    expect(debugButton()).toBeNull();
    // Nor the Shell menu control: without a debug container its menu would
    // hold nothing Shell's own click does not.
    expect(document.querySelector('[data-shell-menu]')).toBeNull();
    expect(document.querySelector('[data-debug-in-menu]')).toBeNull();
  });

  it('follows the page under the server profile, beside Shell\'s own offer', async () => {
    stub(podPage([DISTROLESS], { shell: { permitted: true }, debug: { permitted: true } }), capabilities('shared'));
    mountApp('/c/prod/pods/pay/api-0');
    await ready();
    await waitFor(() => {
      expect(debugButton()?.disabled).toBe(false);
    });
  });

  it('draws nothing where the build cannot open a shell in a pod', async () => {
    stub(podPage([DISTROLESS]), capabilities('desktop', false));
    mountApp('/c/prod/pods/pay/api-0');
    await waitFor(() => {
      expect(document.querySelector('[data-object-controls]')).not.toBeNull();
    });
    expect(document.querySelector('button[data-debug-entry]')).toBeNull();
    expect(document.querySelector('button[aria-label="Shell menu"]')).toBeNull();
  });
});
