import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { StrictMode, useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetLiveHandles } from '../live/handles';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import { MAX_PER_CLUSTER } from './tabs';

/**
 * The panel, in a DOM.
 *
 * What only a DOM can show: a hidden pane really is hidden, and a click really
 * closes what it says it closes.
 */

/**
 * A handle on the dock's own API, so a test can open a tab the way a page would
 * rather than by reaching into the reducer.
 *
 * Handed out through an **effect** and not written during render.
 * `react-hooks/globals` and `react-hooks/immutability` both refuse the shorter
 * spellings, and they are right to: anything written during render is a value
 * React does not know changed, and a test seam that lies about when it is
 * assigned is a test that passes for the wrong reason.
 */
const probe: { api: DockApi | null } = { api: null };

function Probe({ take }: { take: (api: DockApi) => void }): ReactNode {
  const api = useDock();
  useEffect(() => {
    take(api);
  }, [api, take]);
  return null;
}

const take = (api: DockApi): void => {
  probe.api = api;
};

function mount(strict = false): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster',
        element: (
          <DockProvider>
            <Probe take={take} />
            <Dock />
          </DockProvider>
        ),
      },
    ],
    { initialEntries: ['/c/prod'] },
  );
  const tree = (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
  render(strict ? <StrictMode>{tree}</StrictMode> : tree);
}

function openLogs(name: string, cluster = 'prod'): boolean {
  let opened = false;
  act(() => {
    opened = probe.api?.openLogs({ cluster, kind: 'pods', namespace: 'default', name }) ?? false;
  });
  return opened;
}

beforeEach(() => {
  // The stream is never reached in these tests - `EventSource` does not exist in
  // jsdom - so the pane's own effect is stubbed out at the source. What is under
  // test here is the panel, not the stream.
  vi.stubGlobal(
    'EventSource',
    class {
      close(): void {}
      onmessage: unknown = null;
      onerror: unknown = null;
    },
  );
  try {
    localStorage.clear();
    sessionStorage.clear();
  } catch {
    // A storage-less environment is one the dock is built to survive.
  }
});

afterEach(() => {
  probe.api = null;
  resetLiveHandles();
  vi.unstubAllGlobals();
});

/** The scope button. Found by what it is rather than as the one closed menu
 *  button, since a log pane's toolbar has a `⋯` that is one too. */
function scopeButton(): HTMLElement {
  return screen.getByRole('button', {
    expanded: false,
    name: (name) => name !== 'More log options',
  });
}

describe('the panel', () => {
  it('is in the document before anything is open, and does not come and go', () => {
    // A component that returns null when it is empty is remounted the moment the
    // first tab opens - the same defect as a dock inside the Outlet, wearing an
    // empty dock's clothes.
    mount();
    const before = document.getElementById('dock');
    expect(before).not.toBeNull();

    openLogs('api-1');
    expect(document.getElementById('dock')).toBe(before);
  });

  it('draws a tab per panel and marks the active one', () => {
    mount();
    openLogs('api-1');
    openLogs('api-2');

    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining('api-1'), expect.stringContaining('api-2')]),
    );
    expect(tabs.filter((tab) => tab.getAttribute('aria-selected') === 'true')).toHaveLength(1);
  });

  /**
   * The single promise the partition rests on, asserted in a DOM rather than by
   * grepping for the word `dispose`.
   *
   * `display: none` on a **mounted** subtree. A React tab component that
   * unmounts on hidden kills the socket, which is the one thing the whole
   * partition design forbids - and jsdom can tell the two apart where a text
   * search cannot.
   */
  it('keeps every pane mounted and hides the ones that are not active', () => {
    mount();
    openLogs('api-1');
    openLogs('api-2');

    const panes = screen.getAllByRole('tabpanel', { hidden: true });
    expect(panes).toHaveLength(2);

    const hidden = panes.filter((pane) => pane.className.includes('hidden'));
    expect(hidden, 'exactly one pane is hidden and it is still in the document').toHaveLength(1);
    expect(hidden[0]?.isConnected).toBe(true);
  });

  it('folds without unmounting anything', () => {
    // "If this now needs to end something, it is not a fold." An operator tidying
    // the screen pressed the x and lost every shell on every cluster.
    mount();
    openLogs('api-1');
    const pane = screen.getAllByRole('tabpanel', { hidden: true })[0];

    act(() => {
      screen.getByLabelText(/fold the panel/i).click();
    });

    expect(pane?.isConnected).toBe(true);
    expect(screen.getAllByRole('tab')).toHaveLength(1);
  });

  it('says "Nothing open." rather than collapsing into an invisible region', () => {
    // Nothing open *on this cluster* is not nothing open anywhere, and an
    // `empty:hidden` region renders both the same way - the third rendering of a
    // fourth state.
    mount();
    openLogs('web-1', 'staging');
    act(() => {
      probe.api?.dispatch({ type: 'scope', scope: 'prod' });
    });
    expect(screen.getByText('Nothing open.')).toBeTruthy();
  });

  it('resizes from the keyboard, as the drawer does, and never below its floor', () => {
    // A grip with pointer handlers only cannot be reached without a pointer, so a
    // keyboard user could not size the terminal and the log panel at all.
    mount();
    openLogs('api-1');
    const grip = screen.getByRole('separator', { name: 'Resize the panel' });
    const stored = (): string | null => localStorage.getItem('kubetower.dock.height');

    expect(grip.tabIndex).toBe(0);
    expect(grip.getAttribute('aria-controls')).toBe('dock');
    expect(grip.getAttribute('aria-valuenow')).toBe('300');
    expect(grip.getAttribute('aria-valuemin')).toBe('120');

    fireEvent.keyDown(grip, { key: 'ArrowUp' });
    expect(stored()).toBe('316');
    expect(grip.getAttribute('aria-valuenow')).toBe('316');

    fireEvent.keyDown(grip, { key: 'ArrowUp', shiftKey: true });
    expect(stored()).toBe('320');

    fireEvent.keyDown(grip, { key: 'ArrowDown' });
    expect(stored()).toBe('304');

    fireEvent.keyDown(grip, { key: 'Home' });
    expect(stored()).toBe('300');

    for (let press = 0; press < 30; press += 1) fireEvent.keyDown(grip, { key: 'ArrowDown' });
    expect(stored()).toBe('120');
    expect(grip.getAttribute('aria-valuenow')).toBe('120');

    fireEvent.keyDown(grip, { key: 'End' });
    expect(stored()).toBe(grip.getAttribute('aria-valuemax'));
  });

  it('takes the grip out of the tab order while the panel is folded', () => {
    mount();
    openLogs('api-1');
    act(() => {
      screen.getByLabelText(/fold the panel/i).click();
    });
    const grip = screen.getByRole('separator', { name: 'Resize the panel' });
    expect(grip.hasAttribute('tabindex')).toBe(false);

    fireEvent.keyDown(grip, { key: 'ArrowUp' });
    expect(localStorage.getItem('kubetower.dock.height')).toBeNull();
  });

  it('writes the ceiling refusal into a live region and opens nothing', () => {
    mount();
    for (let i = 0; i < MAX_PER_CLUSTER; i++) expect(openLogs(`api-${i}`)).toBe(true);
    expect(openLogs('one-too-many')).toBe(false);

    const note = dockNote();
    // The refusal begins with the number, and names the cluster - which is the
    // half the server cannot say, because it does not partition by cluster.
    expect(note.textContent).toMatch(new RegExp(`^${MAX_PER_CLUSTER}\\b`));
    expect(note.textContent).toContain('prod');
    expect(screen.getAllByRole('tab')).toHaveLength(MAX_PER_CLUSTER);
  });

  it('asks once before ending everything, and names the fold as the way out', () => {
    mount();
    openLogs('api-1');
    openLogs('api-2');

    act(() => {
      screen.getByLabelText(/close the panel and end every session/i).click();
    });
    // Still there. The first press is the question.
    expect(screen.getAllByRole('tab')).toHaveLength(2);
    const note = screen.getByRole('status');
    expect(note.textContent).toContain('2 sessions');
    expect(note.textContent).toContain('fold');

    act(() => {
      screen.getByLabelText(/press again to end every session/i).click();
    });
    expect(screen.queryAllByRole('tab')).toHaveLength(0);
  });

  it('closes the panel with the close box the drawer and the dialogs have', () => {
    mount();
    openLogs('api-1');
    // The 32px square, 6px corners, of `CloseButton`'s toolbar size - not the
    // 24px inline box a tab's × wears.
    const close = screen.getByLabelText(/close the panel and end every session/i);
    expect(close.className).toContain('w-8');
    expect(close.className).toContain('h-8');
    expect(close.className).toContain('rounded-md');
    expect(close.className).not.toContain('size-6');
    act(() => {
      close.click();
    });
    // Armed, the same box, red.
    const armed = screen.getByLabelText(/press again to end every session/i);
    expect(armed.className).toContain('w-8');
    expect(armed.className).toContain('border-bad');
    expect(armed.className).not.toContain('border-line');
  });

  it('lists where the panels are, and closes a cluster from the list', () => {
    mount();
    openLogs('api-1');
    openLogs('web-1', 'staging');
    act(() => {
      probe.api?.dispatch({ type: 'scope', scope: '*' });
    });

    act(() => {
      scopeButton().click();
    });
    const menu = screen.getByRole('menu');
    expect(within(menu).getByText(/All clusters/)).toBeTruthy();

    act(() => {
      screen.getByLabelText(/close the 1 panel open on staging/i).click();
    });
    expect(screen.getAllByRole('tab')).toHaveLength(1);
  });

  /**
   * In a browser `pointerdown` precedes `click`, so a dismissal listening for
   * `pointerdown` on `document` would run `true → false → true` on the open
   * button, and the toggle would behave as a latch. `fireEvent.click`
   * dispatches no `pointerdown`, so the case above cannot see it - this one
   * dispatches the pair, as a pointer does.
   */
  it('closes the scope menu on its own button, pressed as a pointer presses it', () => {
    mount();
    openLogs('api-1');
    const button = (): HTMLElement => {
      const found = document.querySelector('button[aria-haspopup="menu"]');
      if (found === null) throw new Error('no scope button');
      return found as HTMLElement;
    };
    const press = (): void => {
      act(() => {
        fireEvent.pointerDown(button());
        fireEvent.click(button());
      });
    };

    press();
    expect(screen.getByRole('menu')).toBeTruthy();

    press();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(button().getAttribute('aria-expanded')).toBe('false');
  });

  it('closes the scope menu on a click anywhere else, through the scrim', () => {
    // The click lands on an element in the tree, not on a `document` listener,
    // and that element is out of the tab order: `MenuScrim` says why.
    mount();
    openLogs('api-1');
    act(() => {
      scopeButton().click();
    });
    const scrim = document.querySelector('button[aria-hidden="true"][tabindex="-1"]');
    expect(scrim, 'the scrim is there while the menu is open').not.toBeNull();

    act(() => {
      fireEvent.click(scrim as HTMLElement);
    });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.querySelector('button[aria-hidden="true"][tabindex="-1"]')).toBeNull();
  });

  it('badges what is running on another cluster, counting live panels only', () => {
    mount();
    openLogs('api-1');
    act(() => {
      probe.api?.dispatch({ type: 'followPage', cluster: 'staging' });
    });
    openLogs('web-1', 'staging');

    expect(screen.getByText('+1 elsewhere')).toBeTruthy();

    // An ended panel holds nothing, so the badge must stop counting it - a badge
    // that counts ended sessions sends the operator looking for something to
    // close that is already gone.
    act(() => {
      const id = probe.api?.state.tabs.find((tab) => tab.cluster === 'prod')?.id ?? '';
      probe.api?.dispatch({ type: 'ended', id });
    });
    expect(screen.queryByText(/elsewhere/)).toBeNull();
  });
});

/**
 * The StrictMode trap, exercised rather than reasoned about.
 *
 * React 19's StrictMode double-invokes effects in development, so a stream
 * opened in a `useEffect` opens **two** - and `internal/logs` caps followed
 * streams at sixteen, which presents as a limit reached at half the streams, in
 * development only.
 */
describe('StrictMode', () => {
  it('opens one stream per panel, not two', () => {
    const opened: string[] = [];
    vi.stubGlobal(
      'EventSource',
      class {
        constructor(url: string) {
          opened.push(url);
        }
        close(): void {}
        onmessage: unknown = null;
        onerror: unknown = null;
      },
    );

    mount(true);
    openLogs('api-1');
    expect(opened).toHaveLength(1);
  });

  it('keeps two panels on one pod apart', () => {
    // The handle key contains the tab's sequence number. Without it the second
    // panel finds the first's handle, and the counter that exists to keep two
    // shells on one pod apart is defeated by the deduplication that exists to
    // keep StrictMode from opening two.
    //
    // **Built by dispatching `open` twice rather than by calling `openLogs`**:
    // `openLogs` finds a panel already on the object and focuses it, so it
    // cannot make two panels on one pod - which is the whole point of it.
    //
    // The property here is the **handle key**, which belongs to the tab: two
    // tabs on one pod must not share a stream, however they came to exist. The
    // reducer still builds them - its `open` is a counter and never a content
    // hash - and `openShell` still reaches the state for real, because two
    // shells on one pod are two working sessions. So the rule is live and this
    // asserts it at the level it actually holds.
    const opened: string[] = [];
    vi.stubGlobal(
      'EventSource',
      class {
        constructor(url: string) {
          opened.push(url);
        }
        close(): void {}
        onmessage: unknown = null;
        onerror: unknown = null;
      },
    );

    mount(true);
    const twice = (): void => {
      act(() => {
        probe.api?.dispatch({
          type: 'open',
          tab: { kind: 'logs', cluster: 'prod', label: 'api-1', namespace: 'default', name: 'api-1' },
        });
      });
    };
    twice();
    twice();
    expect(opened).toHaveLength(2);
  });
});

/**
 * A reload gets its tabs back. jsdom cannot reload a page, but the dock
 * does not know the difference between a reload and this: the tree unmounted,
 * storage kept, the tree mounted again from nothing. What is asserted is the
 * DOM the operator gets - the tabs, in order, the same one selected - and the
 * stream count, because a reopened log panel that opened its stream twice is
 * the StrictMode defect wearing a restore's clothes.
 */
describe('a reload', () => {
  function reopen(strict = false): void {
    cleanup();
    resetLiveHandles();
    probe.api = null;
    mount(strict);
  }

  it('gets the same tabs back, in order, with the same one in front', () => {
    mount();
    openLogs('api-1');
    openLogs('api-2');
    openLogs('web-0', 'staging');
    act(() => {
      probe.api?.dispatch({ type: 'select', id: 'logs:1' });
    });
    act(() => {
      probe.api?.dispatch({ type: 'scope', scope: '*' });
    });

    reopen();

    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      expect.stringContaining('api-1'),
      expect.stringContaining('api-2'),
      expect.stringContaining('web-0'),
    ]);
    expect(tabs[0]?.getAttribute('aria-selected')).toBe('true');
    expect(probe.api?.state.tabs.every((tab) => tab.live)).toBe(true);
  });

  it('opens one stream per reopened panel under StrictMode, and restores once', () => {
    const opened: string[] = [];
    vi.stubGlobal(
      'EventSource',
      class {
        constructor(url: string) {
          opened.push(url);
        }
        close(): void {}
        onmessage: unknown = null;
        onerror: unknown = null;
      },
    );
    mount(true);
    openLogs('api-1');
    openLogs('api-2');
    opened.length = 0;

    reopen(true);

    expect(screen.getAllByRole('tab')).toHaveLength(2);
    expect(opened).toHaveLength(2);
  });

  it('starts empty when the last tab was closed before the reload', () => {
    mount();
    openLogs('api-1');
    act(() => {
      probe.api?.dispatch({ type: 'closeAll' });
    });
    expect(sessionStorage.getItem('kubetower.dock.tabs')).toBeNull();

    reopen();
    expect(screen.queryAllByRole('tab')).toHaveLength(0);
  });

  it('gives a Claude tab a conversation at the door, and hands the same one back after a reload', () => {
    // The id is the browser's to draw and the server's to interpret; the
    // dock's whole part is that the tab it reopens carries the id it had.
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'claude', cluster: 'prod', label: 'Claude · api-1', name: 'api-1' });
      probe.api?.openShell({ kind: 'exec', cluster: 'prod', label: 'api-1', name: 'api-1' });
    });
    const before = probe.api?.state.tabs.find((tab) => tab.kind === 'claude')?.session;
    expect(before).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(probe.api?.state.tabs.find((tab) => tab.kind === 'exec')?.session).toBeUndefined();

    reopen();

    expect(probe.api?.state.tabs.find((tab) => tab.kind === 'claude')?.session).toBe(before);
  });

  it('says a lone node shell was not reopened, on a panel that can be seen', () => {
    // A node shell is never reopened, so a dock whose only stored tab was one
    // restores to no tabs at all - and a `hidden` section with no tabs would
    // put the note saying why into a `display: none` subtree that nobody can
    // see or hear before it clears itself.
    sessionStorage.setItem(
      'kubetower.dock.tabs',
      JSON.stringify({ tabs: [{ kind: 'nodeshell', cluster: 'prod', label: 'node-1' }], active: 0 }),
    );
    mount();
    const dock = document.getElementById('dock');
    expect(probe.api?.state.tabs).toHaveLength(0);
    expect(dock?.className.split(/\s+/)).not.toContain('hidden');
    expect(within(dock as HTMLElement).getByRole('status').textContent).toMatch(
      /node shell on node-1 .*not reopened/,
    );
    // Header only: there is no pane to give a height to.
    expect(dock?.getAttribute('style')).toBeNull();

    // And once the sentence has been read, an empty dock is not drawn.
    act(() => {
      probe.api?.dispatch({ type: 'note', note: '' });
    });
    expect(dock?.className.split(/\s+/)).toContain('hidden');
  });

  it('draws the dock from nothing when storage holds something that is not a dock', () => {
    sessionStorage.setItem('kubetower.dock.tabs', '{"tabs":[{"kind":"exec"}]}');
    mount();
    expect(screen.queryAllByRole('tab')).toHaveLength(0);
    expect(document.getElementById('dock')).not.toBeNull();
  });
});

/** The dock's own live region. Not `getByRole('status')`: past four, a log
 *  pane waiting for a connection draws its notice as a status too. */
function dockNote(): HTMLElement {
  const note = screen.getAllByRole('status').find((each) => each.getAttribute('aria-live') === 'polite');
  if (note === undefined) throw new Error("the dock's live region is missing");
  return note;
}
