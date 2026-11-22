import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { resetLiveHandles } from '../live/handles';
import { Drawer } from '../shell/Drawer';
import { ShellProvider, useShell } from '../shell/ShellContext';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import { LogsButton } from './LogsButton';
import { MAX_PER_CLUSTER } from './tabs';

/**
 * The control, with the drawer it is usually inside and the panel it opens
 * into.
 *
 * Opening the log closes the drawer. What is asserted here is the drawer's own
 * `hidden` attribute, which is what `Drawer.tsx` draws from the shell's state -
 * not a spy on `closeDrawer`, which would pass for a call the drawer never
 * honored.
 *
 * The seam is the same as `Dock.test.tsx`'s: the two contexts are handed out
 * through an effect and never written during render.
 */
const probe: { dock: DockApi | null; shell: ReturnType<typeof useShell> | null } = {
  dock: null,
  shell: null,
};

function DockProbe(): ReactNode {
  const api = useDock();
  useEffect(() => {
    probe.dock = api;
  }, [api]);
  return null;
}

function ShellProbe(): ReactNode {
  const api = useShell();
  useEffect(() => {
    probe.shell = api;
  }, [api]);
  return null;
}

/** The control under a Shell, as it is in the drawer - or without one, as a
 *  page rendered alone has it. */
function mount(withShell = true): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const inside = (
    <DockProvider>
      <DockProbe />
      <LogsButton cluster="prod" kind="pods" namespace="default" name="api-1" />
      <Dock />
      {withShell ? (
        <>
          <ShellProbe />
          <Drawer />
        </>
      ) : null}
    </DockProvider>
  );
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster',
        element: withShell ? <ShellProvider extensions={[]}>{inside}</ShellProvider> : inside,
      },
    ],
    { initialEntries: ['/c/prod'] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

function drawer(): HTMLElement {
  const found = document.getElementById('drawer');
  if (found === null) throw new Error('no drawer');
  return found;
}

function openDrawer(): void {
  act(() => {
    probe.shell?.openDrawer(<p>api-1</p>);
  });
  expect(drawer().hidden).toBe(false);
}

function pressLogs(): void {
  act(() => {
    screen.getByRole('button', { name: 'Logs' }).click();
  });
}

beforeEach(() => {
  // The stream is never reached - `EventSource` does not exist in jsdom - so the
  // pane's own effect is stubbed at the source, as `Dock.test.tsx` does. What
  // is under test is the gesture, not the stream.
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
  } catch {
    // A storage-less environment is one the dock is built to survive.
  }
});

afterEach(() => {
  probe.dock = null;
  probe.shell = null;
  resetLiveHandles();
  resetKeyboardStack();
  vi.unstubAllGlobals();
});

describe('Logs, pressed from the drawer', () => {
  it('opens the log and closes the drawer', () => {
    mount();
    openDrawer();

    pressLogs();

    expect(screen.getAllByRole('tab')).toHaveLength(1);
    expect(drawer().hidden, 'the log has the width once it is asked for').toBe(true);
  });

  it('leaves the drawer where it was when the ceiling refuses', () => {
    mount();
    // **Eight *other* pods, and the naming is the point.** `api-1` is the pod
    // the button under test asks for; filling the ceiling with `api-0`..`api-7`
    // would press Logs on a panel already in the strip, and the assertions
    // below would record the refusal of it as the correct outcome.
    //
    // The ceiling is still full. Nothing in it is the pod being asked for.
    for (let i = 0; i < MAX_PER_CLUSTER; i++) {
      act(() => {
        probe.dock?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name: `other-${i}` });
      });
    }
    openDrawer();

    pressLogs();

    // Nothing opened, the operator is still on the object, and the panel's own
    // status line says why - the one sentence with the number in it.
    expect(screen.getAllByRole('tab')).toHaveLength(MAX_PER_CLUSTER);
    expect(drawer().hidden).toBe(false);
    expect(dockNote().textContent).toMatch(new RegExp(`^${MAX_PER_CLUSTER}\\b`));
  });

  it('closes the drawer at the ceiling when the panel it asks for is already open', () => {
    /*
      Eight log panels open on one cluster, and Logs pressed in the drawer on a
      pod already in the tab strip: the drawer must close, or the dock is left
      with a terminal in it wrapping mid-word.

      `room` counts and never looks at what is being asked for, so alone it
      would refuse a panel that costs nothing because it already exists.

      The ceiling is genuinely full here - seven others plus `api-1` itself - so
      this asserts the reuse and not a raised limit. The tab count must not move:
      focusing is not opening.
    */
    mount();
    for (let i = 0; i < MAX_PER_CLUSTER - 1; i++) {
      act(() => {
        probe.dock?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name: `other-${i}` });
      });
    }
    // The one the button asks for, opened from somewhere else first - a list
    // row, an earlier visit - and then left behind another tab.
    act(() => {
      probe.dock?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1' });
    });
    act(() => {
      probe.dock?.dispatch({ type: 'select', id: 'logs:1' });
    });
    expect(screen.getAllByRole('tab')).toHaveLength(MAX_PER_CLUSTER);
    openDrawer();

    pressLogs();

    // Nothing new was built, nothing was refused, the panel is in front, and the
    // drawer has given the width back.
    expect(screen.getAllByRole('tab'), 'focusing is not opening').toHaveLength(MAX_PER_CLUSTER);
    expect(probe.dock?.state.active).toBe('logs:8');
    expect(dockNote().textContent, 'no refusal, so no note').toBe('');
    expect(drawer().hidden, 'the log has the width once it is asked for').toBe(true);
  });

  it('reuses the open panel rather than building a second one, ceiling or not', () => {
    // Below the ceiling the old behavior was not a refusal but a duplicate: the
    // reducer's `open` is `<kind>:<seq>` and never a content hash, so a second
    // press built `logs:2` on the same pod - the same lines twice, costing a tab
    // and one of the server's sixteen follows. Measured before the fix: two
    // presses gave `logs:1,logs:2`.
    mount();

    pressLogs();
    pressLogs();

    expect(screen.getAllByRole('tab')).toHaveLength(1);
  });

  it('unfolds the panel when it sends the operator to a tab already in it', () => {
    // A `select` alone would answer the gesture by moving something nobody can
    // see. The operator pressed Logs on a folded dock: the panel has to come
    // back for the answer to mean anything, which is why `focus` exists beside
    // `select` rather than reusing it.
    mount();
    pressLogs();
    act(() => {
      probe.dock?.dispatch({ type: 'fold', folded: true });
    });
    expect(probe.dock?.state.folded).toBe(true);
    openDrawer();

    pressLogs();

    expect(probe.dock?.state.folded).toBe(false);
    expect(drawer().hidden).toBe(true);
  });

  it('carries its glyph, like every other control in the row it sits in', () => {
    // A control is findable by its name whether or not it has a mark beside
    // it, so nothing else would notice the glyph missing.
    //
    // So this asserts the `<use href>` and not merely that an `<svg>` is
    // present. The sprite resolves by id, and a wrong id draws an empty box in
    // silence - the failure `IconName` makes impossible in TypeScript and that
    // nothing checks between the union and the sheet.
    mount();
    const logs = screen.getByRole('button', { name: 'Logs' });
    expect(logs.querySelector('use')?.getAttribute('href')).toBe('#i-logs');
  });

  it('wears the watching arm: an accent frame, and the word still lifting with it', () => {
    /*
      The hover is `border-color: var(--accent)` **and** `color: var(--fg)`.
      With `border border-line` that never changes and `hover:text-fg` alone, a
      control the operator hovers to open a stream would look exactly like the
      one that copies a value.

      **The pair is the assertion and neither half stands alone.** Asserting the
      frame alone would pass on a repair that landed the border and dropped the
      color, and asserting `hover:text-fg` alone would pass on the word alone.
      The Claude control's test argues the shape; this is it on the other arm.

      `hover:bg-panel-2` is refused by name because it is a different arm's: the
      surface lights for a control that is a glyph with no word (`Menu.tsx`'s
      fold, `Rail.tsx`'s Arrange, the Claude mark). Logs has a word, and the
      word is what moves.

      The ring is asserted beside the hover because the two deliberately share
      `--color-accent`:
      the ring is a 2px outline one pixel **outside** the border and the hover
      is the border itself, so both are visible at once. A later reader tidying
      one of them away is the failure this line exists against.

      jsdom applies no stylesheet, so this is a class-string test and weak on
      purpose - the computed values are the browser's to show.
    */
    mount();
    const classes = screen.getByRole('button', { name: 'Logs' }).className.split(/\s+/);

    expect(classes).toContain('hover:border-accent');
    expect(classes).toContain('hover:text-fg');
    // At rest the frame is the row's own line: the blue is the hover, not the
    // control.
    expect(classes).toContain('border-line');
    // Not the glyph-only arm.
    expect(classes).not.toContain('hover:bg-panel-2');
    // And the ring, outside the border the hover paints.
    expect(classes).toContain('focus-visible:outline-2');
    expect(classes).toContain('focus-visible:outline-accent');
    expect(classes).toContain('focus-visible:outline-offset-1');
  });

  it('still opens the log where there is no Shell above it', () => {
    // A page rendered alone has a dock and no drawer. There the control opens
    // the log and has nothing to close, and that is not an error.
    mount(false);
    expect(document.getElementById('drawer')).toBeNull();

    pressLogs();

    expect(screen.getAllByRole('tab')).toHaveLength(1);
  });
});

/** The dock's own live region. Not `getByRole('status')`: past four, a log
 *  pane waiting for a connection draws its notice as a status too. */
function dockNote(): HTMLElement {
  const note = screen.getAllByRole('status').find((each) => each.getAttribute('aria-live') === 'polite');
  if (note === undefined) throw new Error("the dock's live region is missing");
  return note;
}
