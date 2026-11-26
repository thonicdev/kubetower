import { act, fireEvent, waitFor } from '@testing-library/react';
import { useEffect, useState, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { DockProvider, useDock, type DockApi } from '../../dock/DockContext';
import { MAX_PER_CLUSTER } from '../../dock/tabs';
import { resetKeyboardStack } from '../../keyboard/stack';
import { Drawer } from '../../shell/Drawer';
import { ShellProvider, useShell } from '../../shell/ShellContext';
import { mountApp, renderWithClient, stubFetch, type StubRoutes } from '../../testing/harness';
import { shellOn, Terminals } from './Terminals';

afterEach(() => {
  probe.dock = null;
  probe.shell = null;
  resetKeyboardStack();
  vi.unstubAllGlobals();
});

beforeEach(() => {
  // What a terminal tab needs from a browser and jsdom does not have. Neither
  // stub makes this file say anything about the terminal - they stop it throwing
  // before it reaches what is being measured, which is whether the control
  // opened a tab at all. `TerminalPane.test.tsx` carries the same two and says
  // the same of them: `matchMedia` is how xterm watches the device pixel ratio,
  // `ResizeObserver` is what the pane watches its host with, and `WebSocket` is
  // what it opens once the tab exists.
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
      close(): void {}
      send(): void {}
      addEventListener(): void {}
      removeEventListener(): void {}
    },
  );
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

/** Everything on, which is the desktop profile. Each test that is about a
 *  capability turns exactly one of them off. */
const able = {
  body: {
    mode: 'desktop',
    localShell: true,
    podExec: true,
    nodeShell: true,
    assistant: true,
    portForward: true,
    readOnly: { writesRefused: false, detailWritesRefused: false, measured: true },
  },
};

const POD = '/api/c/prod/r/pods/argo/api-0';
const NODE = '/api/c/prod/r/nodes/worker-3';
const SECRET = '/api/c/prod/r/secrets/argo/registry-pull';

function podPage(containers: unknown[] = [{ name: 'app' }]): StubRoutes[string] {
  return {
    body: {
      state: 'ok',
      data: {
        kind: 'pods',
        namespace: 'argo',
        name: 'api-0',
        title: 'Pod',
        tabs: [],
        sections: [],
        actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }],
        containers,
      },
    },
  };
}

/** A Secret, which is the one kind the assistant is not offered on. Its `data`
 *  is deliberately absent here: the server does not put a Secret's values in a
 *  detail payload at all, so a stub that carried them would be testing a
 *  response this console never receives. */
function secretPage(): StubRoutes[string] {
  return {
    body: {
      state: 'ok',
      data: {
        kind: 'secrets',
        namespace: 'argo',
        name: 'registry-pull',
        title: 'Secret',
        tabs: [],
        sections: [],
        actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }],
      },
    },
  };
}

function nodePage(): StubRoutes[string] {
  return {
    body: {
      state: 'ok',
      data: {
        kind: 'nodes',
        name: 'worker-3',
        title: 'Node',
        tabs: [],
        sections: [],
        actions: [{ verb: 'cordon', label: 'Cordon', permitted: true }],
      },
    },
  };
}

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': able,
    ...extra,
  };
}

function terminals(): HTMLElement {
  const element = document.querySelector('[aria-label="Terminals"]');
  if (element === null) throw new Error('no terminal controls');
  return element as HTMLElement;
}

/**
 * The Terminals group, once the object it is about has answered.
 *
 * The group is drawn as soon as `capabilities` has answered, and since the
 * pages are chunks of their own the capabilities read can land before the page
 * has even mounted, so the group can be on screen while the object is still
 * being read - and a click on Shell at that moment is a click before the
 * containers are known. Waiting for the group alone is waiting for the wrong
 * read; the Claude control's title says which read is still out.
 */
async function terminalsReady(): Promise<void> {
  await waitFor(() => {
    expect(document.querySelector('[aria-label="Terminals"]')).not.toBeNull();
    expect(terminals().querySelector('button[title^="Still reading"]')).toBeNull();
  });
}

/** A control in the group, by the name a person hears.
 *
 *  **`aria-label` as well as the text**, and the two together are the point.
 *  Every control here is an icon plus its word except Claude, which is the
 *  published mark alone and carries its name on `aria-label` - so a helper that
 *  only read `textContent` would not find it, and one that only read
 *  `aria-label` would stop finding the other
 *  five. Reading both is what lets these tests state the same thing about every
 *  control regardless of which way it is labeled. */
function maybeControl(name: string): HTMLButtonElement | undefined {
  return [...terminals().querySelectorAll('button')].find(
    (one) => one.textContent?.includes(name) === true || one.getAttribute('aria-label') === name,
  );
}

function control(name: string): HTMLButtonElement {
  const found = maybeControl(name);
  if (found === undefined) throw new Error(`no control called ${name}`);
  return found;
}

/**
 * The container menu, the console's own popover.
 *
 * Read by its `aria-label` rather than by its tag: what these tests are about
 * is that the user is *asked*, and the element the question arrives on is the
 * part that may change.
 */
function maybeContainerMenu(): HTMLElement | null {
  return document.querySelector('ul[aria-label="Container"]');
}

async function containerMenu(): Promise<HTMLElement> {
  return await waitFor(() => {
    const found = maybeContainerMenu();
    if (found === null) throw new Error('no container menu');
    return found;
  });
}

/** One row of that menu, by the container's name. */
function containerRow(name: string): HTMLButtonElement {
  const found = [...(maybeContainerMenu()?.querySelectorAll('button') ?? [])].find((one) =>
    one.textContent?.includes(name),
  );
  if (found === undefined) throw new Error(`no menu row for ${name}`);
  return found;
}

function tabs(): string[] {
  return [...document.querySelectorAll('#dock [role="tab"], #dock button')].map(
    (one) => one.textContent ?? '',
  );
}

/**
 * The controls inside the drawer, with the dock's API and the shell's in hand.
 *
 * Handed out through an effect and never written during render, which is
 * `Dock.test.tsx`'s seam and its reason. The panel itself is not mounted: what
 * is asserted is the drawer's `hidden` attribute and the dock's tab list, and
 * the panel would only add a terminal to stub.
 */
const probe: { dock: DockApi | null; shell: ReturnType<typeof useShell> | null } = {
  dock: null,
  shell: null,
};

function Probe(): ReactNode {
  const dock = useDock();
  const shell = useShell();
  useEffect(() => {
    probe.dock = dock;
    probe.shell = shell;
  }, [dock, shell]);
  return null;
}

/** A pod's controls as the drawer holds them, on a page addressed by a cluster
 *  so the dock follows it. */
function mountInDrawer(): void {
  stubFetch(shell());
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster',
        element: (
          <ShellProvider extensions={[]}>
            <DockProvider>
              <Probe />
              <Drawer />
              <Terminals
                identity={{ cluster: 'prod', kind: 'pods', namespace: 'argo', name: 'api-0' }}
                containers={[{ name: 'app', image: 'app:1', init: false }]}
                kindLabel="Pod"
              />
            </DockProvider>
          </ShellProvider>
        ),
      },
    ],
    { initialEntries: ['/c/prod'] },
  );
  renderWithClient(<RouterProvider router={router} />);
}

function drawer(): HTMLElement {
  const found = document.getElementById('drawer');
  if (found === null) throw new Error('no drawer');
  return found;
}

function openDrawer(): void {
  act(() => {
    probe.shell?.openDrawer(<p>api-0</p>);
  });
  expect(drawer().hidden).toBe(false);
}

/**
 * The three ways into a terminal - Shell, Node shell and Claude - each opens a
 * tab in the dock.
 */
describe('the ways into a terminal on one object', () => {
  it('offers a shell inside a pod, and opens it on the click', async () => {
    stubFetch(shell({ [POD]: podPage() }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    fireEvent.click(control('Shell'));

    // The tab is the proof: `openShell` is what the dock exposes and a tab
    // carrying the pod's name is what it produces.
    await waitFor(() => {
      expect(tabs().join(' ')).toContain('api-0');
    });
  });

  it('asks which container when the pod has several, rather than picking one', async () => {
    stubFetch(
      shell({
        [POD]: podPage([
          { name: 'app', image: 'ghcr.io/shop/api:4.2' },
          { name: 'sidecar', image: 'envoyproxy/envoy:v1.29' },
        ]),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    fireEvent.click(control('Shell'));

    // Not a tab yet. The server would otherwise take the first container, which
    // on a pod with a sidecar is rarely the one a user meant.
    const picker = await containerMenu();
    expect(picker.textContent).toContain('sidecar');
    // The image beneath each name, which on a pod whose containers are called
    // `app` and `sidecar` is the only thing that says which is which.
    expect(picker.textContent).toContain('envoyproxy/envoy:v1.29');
    // Drawn by ImageRef without its copy button: each row is a button, and a
    // button inside a button is invalid and steals the row's click (#110).
    const sidecar = picker.querySelector('[data-image="envoyproxy/envoy:v1.29"]');
    expect(sidecar?.querySelector('[data-image-version]')?.textContent).toBe('v1.29');
    expect(picker.querySelectorAll('button button')).toHaveLength(0);
    expect(picker.querySelector('[aria-label="Copy image"]')).toBeNull();

    fireEvent.click(containerRow('sidecar'));
    await waitFor(() => {
      expect(tabs().join(' ')).toContain('sidecar');
    });
  });

  it('draws the question as a menu under the button, and not as a control beside it', async () => {
    stubFetch(shell({ [POD]: podPage([{ name: 'app' }, { name: 'sidecar' }]) }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    const button = control('Shell');
    // The group's controls before the question is asked. A native `<select>`
    // would be drawn *after* the button, inside the group, so the row would
    // reflow and everything beside it move.
    const before = [...terminals().children].length;

    fireEvent.click(button);
    const menu = await containerMenu();

    // Nothing joined the row: the panel is `absolute` inside the button's own
    // `relative` wrapper, so it hangs under the button and the row is the same
    // width it was. The wrapper holds Shell and its menu control in a row of
    // their own, so the panel's static position is under the pair.
    expect([...terminals().children].length).toBe(before);
    expect(button.closest('div')?.parentElement?.className).toContain('relative');
    expect(menu.closest('div[id]')?.className).toContain('absolute');
    // The console's own menu and not a fifth private popover: the same scrim
    // four other menus share, which is `aria-hidden` and out of the tab order.
    expect(document.querySelector('button[aria-hidden="true"][tabindex="-1"]')).not.toBeNull();
    expect(button.getAttribute('aria-expanded')).toBe('true');
  });

  it('closes the container menu on Escape, through the one keyboard stack', async () => {
    stubFetch(shell({ [POD]: podPage([{ name: 'app' }, { name: 'sidecar' }]) }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    fireEvent.click(control('Shell'));
    await containerMenu();

    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => {
      expect(maybeContainerMenu()).toBeNull();
    });
    // And no tab was opened on the way out: dismissing the question is not an
    // answer to it.
    expect(tabs().join(' ')).not.toContain('api-0');
  });

  it('leaves an init container out of the choice', async () => {
    stubFetch(
      shell({ [POD]: podPage([{ name: 'app' }, { name: 'wait-for-db', init: true }]) }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    // One choosable container, so no picker at all - an init container has
    // finished by the time anybody opens a shell, and offering it is offering a
    // session that cannot start.
    fireEvent.click(control('Shell'));
    await waitFor(() => {
      expect(tabs().join(' ')).toContain('api-0');
    });
    expect(maybeContainerMenu()).toBeNull();
    // And the control says nothing about a menu it does not have: a reader told
    // `aria-expanded="false"` is told to look for something that never opens.
    expect(control('Shell').hasAttribute('aria-expanded')).toBe(false);
  });

  it('confirms before opening a shell on a node, and carries the plan token', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(
      shell({
        [NODE]: nodePage(),
        '/api/c/prod/nodeshell/preview': {
          body: {
            token: 'plan-node-4',
            expiresAt: '2999-01-01T00:00:00Z',
            channel: '/api/c/prod/ws/nodeshell',
            sentence: 'This opens a root shell on worker-3 with the host filesystem mounted.',
            facts: { verb: 'Node shell', count: 1, destructive: true, targets: [] },
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });

    mountApp('/c/prod/nodes/worker-3');
    await terminalsReady();
    fireEvent.click(control('Node shell'));

    // A confirmation and not a button, because the pod is created inside the
    // socket and closing the socket is what deletes it.
    await waitFor(() => {
      expect(document.body.textContent).toContain('root shell on worker-3');
    });
    expect(posted).toEqual([
      { path: '/api/c/prod/nodeshell/preview', body: { node: 'worker-3' } },
    ]);
    // And nothing is open yet.
    expect(tabs().join(' ')).not.toContain('worker-3');
  });

  it('offers a Claude session on an object that is not a Secret', async () => {
    stubFetch(shell({ [POD]: podPage() }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    fireEvent.click(control('Claude'));
    // The label says which resource the session was opened on: `Claude · pod`.
    // Without the prefix, a Claude tab and a shell tab on the
    // same pod are the same word.
    await waitFor(() => {
      expect(tabs().join(' ')).toContain('Claude');
    });
    expect(tabs().join(' ')).toContain('api-0');
  });

  /**
   * The mark, in its own color, and no word beside it.
   *
   * The published mark is drawn alone. Recoloring it to the row's muted gray -
   * `fill="currentColor"` on the sprite path - with *Claude* written next to it
   * would remove the mark's one identifying property and add a word to replace
   * it.
   *
   * Three things, because each of them is separately easy to undo:
   * the button carries no text; the name a screen reader reads is on
   * `aria-label`, since the sprite is `aria-hidden` and `title` here is the
   * sentence that changes with `unread`; and the sprite's own fill is the
   * published color rather than `currentColor`. The last is asserted on
   * `Icon.tsx`'s sheet rather than on the button, which is where it lives.
   */
  it('draws the Claude control as the mark alone, in its own color', async () => {
    stubFetch(shell({ [POD]: podPage() }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    const claude = control('Claude');

    expect(claude.textContent?.trim()).toBe('');
    expect(claude.getAttribute('aria-label')).toBe('Claude');
    expect(claude.getAttribute('title')).toBe('Open a Claude session about api-0');

    const path = document.querySelector('#i-claude path');
    // Named rather than imported, for the reason Icon.test.tsx gives.
    expect(path?.getAttribute('fill')).toBe('#D97757'); // style-allow: the published mark
  });

  /**
   * The other side of the test above.
   *
   * The mark's own color is what stops `hover:text-fg` reaching it: the rule
   * fires - computed `color` changes on hover - and nothing on screen changes,
   * the button's pixels at rest and hovered being identical. A control with a
   * hover rule and no hover is worse than having neither: it reads as done.
   *
   * **What this asserts is a pair, and the pair is the point.** An affordance
   * that does not depend on `currentColor`, *and* the mark still at its
   * published fill - because the cheap repair is to make the mark inherit
   * again, which fixes the hover by recoloring the trademark, and the assertion above
   * would still pass if this one only checked that some hover class exists.
   *
   * `hover:text-` is refused by name rather than by absence of a tint: it is
   * the class that looks like an affordance here and is not one, and the next
   * reader tidying this row back to its neighbors' treatment is exactly how
   * the defect would return.
   *
   * **jsdom cannot see a hover**, so this is a class-string test and it is weak
   * on purpose. The browser is what shows the hover, and this only holds the
   * decision in place.
   */
  it('gives the mark a hover that does not depend on the color it refuses to take', async () => {
    stubFetch(shell({ [POD]: podPage() }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    const claude = control('Claude');

    // The surface lights, which is what `Menu.tsx` and `Rail.tsx` already do
    // for a control folded down to a glyph with no word.
    expect(claude.className).toContain('hover:bg-panel-2');
    // **And the frame is Claude's own color**: the surface fill plus
    // `border-color` in Claude's color. The border is the only channel left -
    // a text color paints nothing here (see above) and recoloring the mark
    // would undo the published color.
    expect(claude.className).toContain('hover:border-claude');
    // Not the watching arm's blue. Logs, Shell and the node shell take
    // `hover:border-accent`; this control sits in the same row and the cheap
    // sweep is to give all four the same frame, which would stop the hover
    // saying *which* control this is.
    expect(claude.className).not.toContain('hover:border-accent');
    // And nothing here tries to recolor the content, which cannot work and
    // would be undone into recoloring the mark.
    expect(claude.className).not.toContain('hover:text-');
    // The mark is untouched: it keeps its published fill.
    expect(document.querySelector('#i-claude path')?.getAttribute('fill')).toBe('#D97757'); // style-allow: the published mark
    // A fill inside the border cannot stand in for the ring outside it, and the
    // ring is the keyboard's only signal. Both, or neither is worth having.
    expect(claude.className).toContain('focus-visible:outline-2');
    expect(claude.className).toContain('focus-visible:outline-accent');
    // A disabled control that lights under the pointer promises a click it
    // refuses - the same guard the row's other controls carry on their own
    // hover.
    expect(claude.className).toContain('disabled:hover:bg-transparent');
    // Both channels, because the button gained a second one: a disabled control
    // that draws Claude's frame under the pointer promises a click it refuses,
    // and the guard that covered the fill alone would not have said so.
    expect(claude.className).toContain('disabled:hover:border-line');
  });

  /**
   * A session with no subject is not a session about this object.
   *
   * `claude.go`'s `hasObject` is `Kind != "" && Name != ""`, and the Kind a
   * session is framed with is `DetailPage.title` - "Pod", not the route's
   * "pods", which would pass the server's validation and put the wrong word in
   * the session's own CLAUDE.md. Until the payload arrives there is no such
   * word, so the control waits rather than opening a pane that claims on its tab
   * to be about a pod and is about the cluster.
   */
  it('waits for the object before offering a session about it', async () => {
    stubFetch(
      shell({
        [POD]: {
          body: {
            state: 'unreachable',
            reason: { code: 'cluster_unreachable', message: 'the cluster did not answer' },
          },
        },
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    const claude = control('Claude');
    expect(claude.hasAttribute('disabled')).toBe(true);
    // Disabled and carrying which of the two it is: still reading, or could not
    // be read. A user hovering a grayed control is owed the right sentence.
    expect(claude.getAttribute('title')).toContain('could not be read');
  });
});

/**
 * The drawer closes once a pane is open, as it does from Logs, because the
 * reason is the same: a terminal at the bottom read beside a drawer on the
 * right is a terminal in what is left. The drawer's own `hidden` attribute is
 * what is asserted - the thing `Drawer.tsx` draws from the shell's state, not a
 * spy on the call.
 */
/**
 * A Pending pod, or one whose only container is in
 * ImagePullBackOff, offered Shell as though there were a process to attach to.
 */
describe('Shell, before any container has run', () => {
  const waiting = (reason: string) => ({
    name: 'app',
    image: 'shop/api:1.4.2',
    init: false,
    status: { state: 'waiting', ready: false, reason },
  });

  it("is disabled with the kubelet's own word while no container is running", async () => {
    stubFetch(shell({ [POD]: podPage([waiting('ImagePullBackOff')]) }));
    mountApp('/c/prod/pods/argo/api-0');
    await terminalsReady();

    await waitFor(() => {
      expect(control('Shell').disabled).toBe(true);
    });
    // And the one gesture that re-reads it: the drawer does not refresh itself.
    expect(control('Shell').title).toBe(
      'No container in this pod is running (ImagePullBackOff), so there is nothing to open a shell in. ' +
        'Close this object and open it again to check whether one has started.',
    );
    fireEvent.click(control('Shell'));
    expect(tabs().join(' ')).not.toContain('api-0');
  });

  it.each([
    ['every container is unreported', [{ name: 'app', image: 'shop/api:1.4.2', init: false, status: { state: 'unreported', ready: false } }]],
    [
      'one container is unreported beside a waiting one',
      [
        { ...waiting('ContainerCreating'), name: 'sidecar' },
        { name: 'app', image: 'shop/api:1.4.2', init: false, status: { state: 'unreported', ready: false } },
      ],
    ],
  ])('keeps Shell where a status could not be read: %s', async (_what, containers) => {
    // The server always sends a status, and
    // `unreported` is what it sends when the status block could not be read.
    // That is the console not knowing, and disabling on it is the
    // empty-versus-failed collapse.
    stubFetch(shell({ [POD]: podPage(containers) }));
    mountApp('/c/prod/pods/argo/api-0');
    await terminalsReady();
    expect(control('Shell').disabled).toBe(false);
    expect(control('Shell').title).toBe('Open a shell inside api-0');
  });

  it('is enabled as soon as one container is running', async () => {
    stubFetch(
      shell({
        [POD]: podPage([
          { ...waiting('CrashLoopBackOff'), name: 'sidecar' },
          { name: 'app', image: 'shop/api:1.4.2', init: false, status: { state: 'running', ready: true } },
        ]),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');
    await terminalsReady();
    expect(control('Shell').disabled).toBe(false);
    expect(control('Shell').title).toBe('Open a shell inside api-0');

    // The stopped sidecar is not offered, so the one
    // running container is not a guess and the shell opens on it, unasked.
    fireEvent.click(control('Shell'));
    expect(maybeContainerMenu()).toBeNull();
    await waitFor(() => {
      expect(tabs().join(' ')).toContain('api-0/app');
    });
  });
});

describe('Shell, pressed from the drawer', () => {
  it('opens the shell and closes the drawer', async () => {
    mountInDrawer();
    await terminalsReady();
    openDrawer();

    act(() => {
      fireEvent.click(control('Shell'));
    });

    expect(probe.dock?.state.tabs.map((tab) => tab.kind)).toEqual(['exec']);
    expect(drawer().hidden, 'the shell has the width once it is asked for').toBe(true);
  });

  it('leaves the drawer where it was when the ceiling refuses', async () => {
    mountInDrawer();
    await terminalsReady();
    for (let i = 0; i < MAX_PER_CLUSTER; i++) {
      act(() => {
        probe.dock?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'argo', name: `api-${i}` });
      });
    }
    openDrawer();

    act(() => {
      fireEvent.click(control('Shell'));
    });

    // Nothing opened and the user is still on the object; the dock's note
    // carries the refusal, and it is the only place the number is written.
    expect(probe.dock?.state.tabs).toHaveLength(MAX_PER_CLUSTER);
    expect(probe.dock?.state.note).toMatch(new RegExp(`^${MAX_PER_CLUSTER}\\b`));
    expect(drawer().hidden).toBe(false);
  });
});

/**
 * The same gesture on the third control of the row: a Claude tab must not open
 * under a drawer still holding the object. Two cases, the same two as the
 * Shell's, because the rule is one rule: the drawer goes when the pane has
 * opened, and stays when the dock refused.
 */
describe('Claude, pressed from the drawer', () => {
  it('opens the session and closes the drawer, as Logs and Shell do', async () => {
    mountInDrawer();
    await terminalsReady();
    openDrawer();

    act(() => {
      fireEvent.click(control('Claude'));
    });

    expect(probe.dock?.state.tabs.map((tab) => tab.kind)).toEqual(['claude']);
    expect(drawer().hidden, 'the session has the width once it is asked for').toBe(true);
  });

  it('leaves the drawer where it was when the ceiling refuses', async () => {
    mountInDrawer();
    await terminalsReady();
    for (let i = 0; i < MAX_PER_CLUSTER; i++) {
      act(() => {
        probe.dock?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'argo', name: `api-${i}` });
      });
    }
    openDrawer();

    act(() => {
      fireEvent.click(control('Claude'));
    });

    expect(probe.dock?.state.tabs).toHaveLength(MAX_PER_CLUSTER);
    expect(probe.dock?.state.note).toMatch(new RegExp(`^${MAX_PER_CLUSTER}\\b`));
    expect(drawer().hidden).toBe(false);
  });
});

/**
 * Under `shared` Shell is the pod's own answer (`DetailPage.shell`): asked for
 * the person and the console's own ServiceAccount on that pod. One review of
 * every namespace would answer no to a person granted exec in some, and draw
 * Shell nowhere while the socket would let them in.
 */
describe('Shell under the shared profile', () => {
  const shared = {
    body: { ...able.body, mode: 'shared', localShell: false, nodeShell: false, assistant: false, portForward: false },
  };
  function sharedPod(offer: unknown): StubRoutes[string] {
    const page = (podPage().body as { data: Record<string, unknown> }).data;
    return { body: { state: 'ok', data: offer === undefined ? page : { ...page, shell: offer } } };
  }
  /** The page answered: its Delete verb is drawn. An absent Shell is then an
   *  absence in a page that loaded. */
  async function pageAnswered(): Promise<void> {
    await waitFor(() => {
      expect([...document.querySelectorAll('button')].some((one) => one.textContent?.includes('Delete'))).toBe(true);
    });
  }

  it('is offered on a pod the person and the console may exec in', async () => {
    stubFetch(shell({ '/api/capabilities': shared, [POD]: sharedPod({ permitted: true }) }));
    mountApp('/c/prod/pods/argo/api-0');
    await pageAnswered();
    await waitFor(() => {
      expect(control('Shell').disabled).toBe(false);
    });
  });

  it('is withheld on a pod the person may not exec in', async () => {
    stubFetch(shell({ '/api/capabilities': shared, [POD]: sharedPod(undefined) }));
    mountApp('/c/prod/pods/argo/api-0');
    await pageAnswered();
    expect(document.querySelector('[aria-label="Terminals"]')).toBeNull();
  });

  it("is disabled, saying so, where only the console's ServiceAccount may not", async () => {
    const sentence =
      'you may, but this console may not: its ServiceAccount is not granted create on pods/exec in argo';
    stubFetch(
      shell({
        '/api/capabilities': shared,
        [POD]: sharedPod({ permitted: false, reason: { code: 'verb_denied', message: sentence } }),
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');
    await pageAnswered();
    await waitFor(() => {
      expect(control('Shell').disabled).toBe(true);
    });
    expect(control('Shell').title).toBe(sentence);
  });

  it('is not drawn before the pod has answered, and is the desktop’s podExec alone elsewhere', () => {
    const sharedAble = shared.body as Parameters<typeof shellOn>[0];
    expect(shellOn(sharedAble, { permitted: true }, false)).toEqual({ drawn: false, refused: undefined });
    expect(shellOn(sharedAble, undefined, true)).toEqual({ drawn: false, refused: undefined });
    const desktop = able.body as Parameters<typeof shellOn>[0];
    expect(shellOn(desktop, undefined, false)).toEqual({ drawn: true, refused: undefined });
    expect(shellOn({ ...desktop, podExec: false } as Parameters<typeof shellOn>[0], { permitted: true }, true)).toEqual({
      drawn: false,
      refused: undefined,
    });
  });
});

describe('a capability the profile does not offer draws no control', () => {
  it('withholds the pod shell where podExec is false', async () => {
    stubFetch(
      shell({
        [POD]: podPage(),
        '/api/capabilities': { body: { ...able.body, podExec: false } },
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    // Withheld rather than disabled: the profile does not have this, and the
    // refusal that matters is at the upgrade either way. The capability is a
    // hint; `api/websocket.md` says the contract is the control.
    expect(terminals().textContent).not.toContain('Shell');
    // `maybeControl` and not `textContent`: the Claude control draws the mark
    // and no word, so the group's text never says "Claude" even when the
    // control is right there.
    expect(maybeControl('Claude')).not.toBeUndefined();
  });

  /**
   * The assistant must not be offered on a Secret, since it has no right to
   * read them.
   *
   * The control is withheld rather than disabled, which is the same shape as the
   * capability arm above and for the same reason: a disabled control invites the
   * question of what would enable it, and nothing would.
   *
   * **The absence is asserted beside a presence**, because an empty answer and a
   * failed answer render identically here: a page that never loaded also
   * has no Claude control, and would pass a test that only looked for its
   * absence. The Delete verb is the proof that this object's payload arrived and
   * its control row was drawn.
   *
   * **And this is the cosmetic half.** `internal/terminal/claude.go` refuses an
   * assistant session addressed to a Secret at the upgrade, which is the half
   * that holds when the socket is addressed by something other than this button.
   */
  it('withholds the Claude session on a Secret, which it must not be invited to read', async () => {
    stubFetch(shell({ [SECRET]: secretPage() }));
    mountApp('/c/prod/secrets/argo/registry-pull');

    // The object answered and its control row was drawn: this is an absence in a
    // page that loaded, not the absence of a page.
    await waitFor(() => {
      expect(document.querySelector('[data-object-controls]')?.textContent).toContain('Delete');
    });

    // Read off the document rather than through `maybeControl`, which asks the
    // Terminals group for its buttons: a Secret offers no shell and no node
    // shell either, so with the assistant withheld the group is not drawn at all
    // and there is nothing to ask.
    expect(document.querySelector('[aria-label="Terminals"] [aria-label="Claude"]')).toBeNull();
  });

  it('withholds the Claude session where the assistant is off', async () => {
    stubFetch(
      shell({
        [POD]: podPage(),
        '/api/capabilities': { body: { ...able.body, assistant: false } },
      }),
    );
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    expect(maybeControl('Claude')).toBeUndefined();
    expect(terminals().textContent).toContain('Shell');
  });
});

/**
 * The Shell control must not be enabled before the object has answered.
 *
 * The group is drawn on `capabilities`, and that read can land first: for as
 * long as the object's own read is out, the drawer says *Still reading* and a
 * clickable Shell control beside it would, with no containers in hand, see a
 * list of none, skip the question this component exists to ask, and open the
 * exec route with no container at all - which the API server answers with
 * `spec.containers[0]`. On a pod with a sidecar that is a shell in whichever
 * container happens to be first, chosen by nobody.
 *
 * So the pod's read is held open here rather than stubbed as an answer, and
 * released with the payload each case is about. Holding the *body* and building
 * a `Response` per call, because a `Response` is consumed once and a second
 * fetch of the same route would otherwise throw where the assertion should be.
 */
describe('Shell, before the object has answered', () => {
  function holdThePodOpen(): (containers: unknown[]) => Promise<void> {
    stubFetch(shell());
    const stubbed = globalThis.fetch;
    let release: (body: unknown) => void = () => {};
    const held = new Promise<unknown>((resolve) => {
      release = resolve;
    });
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(
        typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url,
      );
      if ((path.split('?')[0] ?? path) === POD) {
        return held.then(
          (body) =>
            new Response(JSON.stringify(body), {
              headers: { 'Content-Type': 'application/json' },
            }),
        );
      }
      return stubbed(input, init);
    });
    return async (containers: unknown[]) => {
      await act(async () => {
        release(podPage(containers).body);
      });
    };
  }

  function shellControl(): HTMLButtonElement {
    return control('Shell');
  }

  it('is disabled while the object is still being read, and opens nothing', async () => {
    holdThePodOpen();
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(document.querySelector('[aria-label="Terminals"]')).not.toBeNull();
    });

    const shellButton = shellControl();
    expect(shellButton.hasAttribute('disabled')).toBe(true);
    // The same sentence the assistant control carries for the same state: one
    // read is out, and both controls say which one rather than each inventing a
    // way of saying *not yet*.
    expect(shellButton.getAttribute('title')).toContain('Still reading');
    expect(shellButton.getAttribute('title')).toBe(control('Claude').getAttribute('title'));

    fireEvent.click(shellButton);

    // Neither route out of the control: no question asked from an empty list,
    // and no exec tab opened on a container nobody chose.
    expect(maybeContainerMenu()).toBeNull();
    expect(tabs().join(' ')).not.toContain('api-0');
  });

  it('asks which container once a two-container pod has answered', async () => {
    const release = holdThePodOpen();
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(shellControl().hasAttribute('disabled')).toBe(true);
    });
    await release([{ name: 'app' }, { name: 'sidecar' }]);

    await waitFor(() => {
      expect(shellControl().hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(shellControl());

    const picker = await containerMenu();
    expect(picker.textContent).toContain('sidecar');
    // Still nothing open: the question is the answer to the click.
    expect(tabs().join(' ')).not.toContain('api-0');
  });

  it('opens straight away once a one-container pod has answered', async () => {
    const release = holdThePodOpen();
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(shellControl().hasAttribute('disabled')).toBe(true);
    });
    await release([{ name: 'app' }]);

    await waitFor(() => {
      expect(shellControl().hasAttribute('disabled')).toBe(false);
    });
    fireEvent.click(shellControl());

    // One container is not a question, and the wait costs the user the moment the read takes and nothing else.
    await waitFor(() => {
      expect(tabs().join(' ')).toContain('api-0');
    });
    expect(maybeContainerMenu()).toBeNull();
  });
});

/*
  **The hover names what the control is**, and this row is where all three
  arms are visible at once: Logs and Shell frame in blue on hover, Claude in
  Claude's color, and the destructive buttons in red.

  Three arms, and the row draws every one of them: Shell and the node shell
  watch, the mark is Claude's, and `Verbs` hangs its destructive verbs off the
  same group. What is pinned per arm is a **pair** - the treatment present and
  the one it replaces absent - because "some hover class exists" passes on the
  repair that recolored the Claude mark, and the reason the Claude test is
  shaped this way.

  jsdom applies no stylesheet: a `hover:` class is a string in `className` here
  and nothing more.
*/
describe("the terminal row's hovers name what each control is", () => {
  it('gives the pod shell the accent frame, and keeps the word lifting with it', async () => {
    stubFetch(shell({ [POD]: podPage() }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    const classes = control('Shell').className.split(/\s+/);

    // The whole hover: the frame **and** the color, not the color alone.
    expect(classes).toContain('hover:border-accent');
    expect(classes).toContain('hover:text-fg');
    expect(classes).toContain('border-line');
    // Not the glyph-only arm, and not Claude's.
    expect(classes).not.toContain('hover:bg-panel-2');
    expect(classes).not.toContain('hover:border-claude');
    // The ring stays where it is, on the accent, *outside* the border the hover
    // paints. The two sharing `--color-accent` is acceptable, so the two channels are meant to be legible together and neither may be
    // quietly dropped in favor of the other.
    expect(classes).toContain('focus-visible:outline-2');
    expect(classes).toContain('focus-visible:outline-accent');
    expect(classes).toContain('focus-visible:outline-offset-1');
    // And a disabled Shell - the pod has not answered yet - draws neither.
    expect(classes).toContain('disabled:hover:border-line');
    expect(classes).toContain('disabled:hover:text-muted');
  });

  it('gives the node shell the same arm, because it watches the same way', async () => {
    stubFetch(shell({ [NODE]: nodePage() }));
    mountApp('/c/prod/nodes/worker-3');

    await terminalsReady();
    const classes = control('Node shell').className.split(/\s+/);

    // The third watching control: it is on a different page from Logs, so a
    // rule applied by hand would reach two of the three.
    expect(classes).toContain('hover:border-accent');
    expect(classes).toContain('hover:text-fg');
    expect(classes).not.toContain('hover:bg-panel-2');
  });

  it('leaves the destructive verb on the same object red rather than blue', async () => {
    // The row's other arm, asserted from here as well as from `Verbs.test.tsx`,
    // because this is the surface where the three sit beside each other and a
    // sweep that reddened everything - or blued everything - would be read as
    // correct in either file alone.
    stubFetch(shell({ [POD]: podPage() }));
    mountApp('/c/prod/pods/argo/api-0');

    await terminalsReady();
    const remove = [...document.querySelectorAll('button')].find(
      (one) => one.textContent === 'Delete',
    );
    const classes = (remove?.className ?? '').split(/\s+/);

    expect(classes).toContain('hover:border-bad');
    expect(classes).not.toContain('hover:border-accent');
    expect(classes).not.toContain('hover:text-fg');
  });
});

/**
 * A node shell's confirmation names the node its token opens, whatever the
 * panel shows by the time the user answers.
 *
 * The drawer is keyed on the object, so a re-targeted drawer is a new panel -
 * `ObjectDrawer.test.tsx` holds that. This holds the other half, for any surface
 * that hands `Terminals` a new identity without remounting it: the dialog and
 * the dock tab must not read `identity`, or they would name worker-4 while
 * `plan.token` opened worker-3.
 */
describe('a node shell confirmation, re-targeted', () => {
  const aim: { to: ((name: string) => void) | null } = { to: null };

  function Aimed(): ReactNode {
    const [name, setName] = useState('worker-3');
    useEffect(() => {
      aim.to = setName;
    }, []);
    return <Terminals identity={{ cluster: 'prod', kind: 'nodes', name }} kindLabel="Node" />;
  }

  it('names the node the plan was made for, in the dialog and in the tab', async () => {
    stubFetch(
      shell({
        '/api/c/prod/nodeshell/preview': {
          body: {
            token: 'plan-for-worker-3',
            expiresAt: '2999-01-01T00:00:00Z',
            channel: '/api/c/prod/ws/nodeshell',
            sentence: 'This opens a root shell on worker-3 with the host filesystem mounted.',
            facts: { verb: 'Node shell', count: 1, destructive: true, targets: [] },
          },
        },
      }),
    );
    const router = createMemoryRouter(
      [
        {
          path: '/c/:cluster',
          element: (
            <ShellProvider extensions={[]}>
              <DockProvider>
                <Probe />
                <Aimed />
              </DockProvider>
            </ShellProvider>
          ),
        },
      ],
      { initialEntries: ['/c/prod'] },
    );
    renderWithClient(<RouterProvider router={router} />);

    await terminalsReady();
    fireEvent.click(control('Node shell'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Open a shell on worker-3?');
    });

    // The panel moves to another node while the question is open.
    act(() => {
      aim.to?.('worker-4');
    });
    expect(document.body.textContent).toContain('Open a shell on worker-3?');
    expect(document.body.textContent).not.toContain('Open a shell on worker-4?');

    const apply = [...document.querySelectorAll('button')].find((one) => one.textContent?.trim() === 'Apply');
    if (apply === undefined) throw new Error('no Apply in the dialog');
    act(() => {
      fireEvent.click(apply);
    });

    await waitFor(() => {
      expect(probe.dock?.state.tabs.length).toBe(1);
    });
    const tab = probe.dock?.state.tabs[0];
    expect(tab?.token).toBe('plan-for-worker-3');
    expect(tab?.label).toBe('worker-3');
  });
});
