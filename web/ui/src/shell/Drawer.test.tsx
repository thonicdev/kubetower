import { act, fireEvent, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { Drawer } from './Drawer';
import { chosenDrawerWidth, DRAWER_FLOOR, LIST_MIN } from './drawerWidth';
import { ShellProvider, useShell } from './ShellContext';

/**
 * The handle on the drawer's inner edge: its keyboard,
 * its `aria-value*`, and what it remembers. The geometry - that the drawer is
 * drawn wider and narrower, the list keeps its minimum, the content fits at the
 * narrowest and nothing else scrolls - is a real browser's, in
 * `measure/drawer.spec.ts` 23.
 *
 * jsdom lays nothing out, so the page here is described rather than drawn: a
 * 1440px window whose rail and kinds column leave 1192px to the list and the
 * drawer, a drawer as wide as its flex basis says but never past what leaves
 * the list 305px - which is what the stylesheet does with it - and an object
 * panel whose controls row needs `NEED` on one line.
 */
const WINDOW = 1440;
const ROOM = 1192;
const MAX = ROOM - LIST_MIN;
/** What the panel's controls row measures on one line: over the floor, so the
 *  measurement is what bounds the drawer. `need` is it, per test. */
const NEED = 480;
let need = NEED;

const probe: { shell: ReturnType<typeof useShell> | null } = { shell: null };

function ShellProbe(): ReactNode {
  const api = useShell();
  useEffect(() => {
    probe.shell = api;
  }, [api]);
  return null;
}

function drawnWidth(aside: HTMLElement): number {
  const basis = Number.parseFloat(aside.style.flexBasis);
  return Number.isNaN(basis) ? 560 : Math.min(MAX, Math.max(need - 1, basis));
}

beforeEach(() => {
  vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(WINDOW);
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const aside = document.getElementById('drawer');
    const width =
      this.id === 'drawer'
        ? drawnWidth(this)
        : this.dataset.testid === 'list' && aside !== null
          ? ROOM - drawnWidth(aside)
          : this.hasAttribute('data-object-controls')
            ? need - 1
            : this.tagName === 'HEADER'
              ? 300
              : 0;
    return { width, height: 0, top: 0, left: 0, right: width, bottom: 0, x: 0, y: 0, toJSON: () => ({}) };
  });
});

afterEach(() => {
  need = NEED;
  probe.shell = null;
  resetKeyboardStack();
  vi.restoreAllMocks();
});

function mount(): void {
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster',
        element: (
          <ShellProvider extensions={[]}>
            <ShellProbe />
            <div data-testid="list" />
            <Drawer />
          </ShellProvider>
        ),
      },
    ],
    { initialEntries: ['/c/prod'] },
  );
  render(<RouterProvider router={router} />);
}

/** What `ObjectDetail` draws in the drawer, reduced to the three rows the
 *  narrowest width is measured from. */
function Panel({ name }: { name: string }): ReactNode {
  return (
    <section data-object-chrome="drawer" className="border-l">
      <header>
        <p>Pod</p>
        <h2 className="truncate">{name}</h2>
      </header>
      <div data-object-controls="">
        <button type="button">Logs</button>
      </div>
      <div role="tablist">
        <button type="button">Details</button>
      </div>
    </section>
  );
}

function open(name = 'storefront-web'): void {
  act(() => {
    probe.shell?.openDrawer(<Panel name={name} />);
  });
}

function drawer(): HTMLElement {
  const found = document.getElementById('drawer');
  if (found === null) throw new Error('no drawer');
  return found;
}

function handle(): HTMLElement {
  return screen.getByRole('separator', { name: 'Resize the panel' });
}

function press(key: string, shiftKey = false): void {
  act(() => {
    fireEvent.keyDown(handle(), { key, shiftKey });
  });
}

function stored(): string | null {
  return localStorage.getItem('kubetower.drawer.width');
}

describe('what a screen reader hears', () => {
  it('does not announce the drawer: it is a region the person moves through, not a status line', () => {
    mount();
    open();
    expect(drawer().hidden).toBe(false);
    expect(drawer().hasAttribute('aria-live')).toBe(false);
  });
});

describe('the handle is there when the drawer is', () => {
  it('draws no handle while the drawer is closed, and one on its inner edge once open', () => {
    mount();
    expect(screen.queryByRole('separator')).toBeNull();

    open();
    const grip = handle();
    expect(drawer().contains(grip)).toBe(true);
    expect(drawer().firstElementChild).toBe(grip);
    expect(grip.getAttribute('aria-orientation')).toBe('vertical');
    expect(grip.getAttribute('aria-controls')).toBe('drawer');
    expect(grip.tabIndex).toBe(0);
  });

  it('draws the drawer exactly as before while nothing was chosen', () => {
    mount();
    open();
    expect(drawer().className).toContain('w-[var(--drawer-w)]');
    expect(drawer().className).toContain('shrink-0');
    expect(drawer().style.flexBasis).toBe('');
  });

  it('writes what the content needs where the stylesheet reads it, and takes it away on close', () => {
    mount();
    open();
    // The controls row's single-line width, plus nothing for a border jsdom
    // does not compute.
    expect(drawer().style.getPropertyValue('--drawer-need')).toBe(`${String(NEED - 1)}px`);
    act(() => {
      probe.shell?.closeDrawer();
    });
    expect(drawer().style.getPropertyValue('--drawer-need')).toBe('');
  });

  it('never writes a need under the measured floor', () => {
    need = 300;
    mount();
    open();
    expect(drawer().style.getPropertyValue('--drawer-need')).toBe(`${String(DRAWER_FLOOR)}px`);
    act(() => {
      handle().focus();
    });
    expect(handle().getAttribute('aria-valuemin')).toBe(String(DRAWER_FLOOR));
  });

  it('floors a chosen width at what the content needs, and never above the default', () => {
    localStorage.setItem('kubetower.drawer.width', '300');
    mount();
    open();
    expect(drawer().style.flexBasis).toBe('300px');
    expect(drawer().style.minWidth).toBe('min(var(--drawer-w), max(300px, var(--drawer-need, 0px)))');
  });
});

describe('the keyboard', () => {
  it('says what the drawer is drawn at, and how far it may go each way', () => {
    mount();
    open();
    act(() => {
      handle().focus();
    });
    expect(handle().getAttribute('aria-valuenow')).toBe('560');
    expect(handle().getAttribute('aria-valuemin')).toBe(String(NEED - 1));
    expect(handle().getAttribute('aria-valuemax')).toBe(String(MAX));
  });

  it('widens with the left arrow and narrows with the right, 16px a press and 4 with Shift', () => {
    mount();
    open();
    press('ArrowLeft');
    expect(chosenDrawerWidth()).toBe(576);
    expect(handle().getAttribute('aria-valuenow')).toBe('576');
    expect(drawer().style.flexBasis).toBe('576px');

    press('ArrowLeft', true);
    expect(chosenDrawerWidth()).toBe(580);

    press('ArrowRight');
    expect(chosenDrawerWidth()).toBe(564);
  });

  it('narrows below the default, down to what the content needs and no further', () => {
    mount();
    open();
    press('ArrowRight');
    expect(chosenDrawerWidth()).toBe(544);
    expect(stored()).toBe('544');
    for (let at = 0; at < 20; at += 1) press('ArrowRight');
    expect(chosenDrawerWidth()).toBe(NEED - 1);
    expect(handle().getAttribute('aria-valuenow')).toBe(String(NEED - 1));
  });

  it('forgets the width when a step lands exactly on the default', () => {
    mount();
    open();
    press('ArrowRight');
    press('ArrowLeft');
    expect(chosenDrawerWidth()).toBeNull();
    expect(stored()).toBeNull();
  });

  it('goes to the widest with End, and stops there', () => {
    mount();
    open();
    press('End');
    expect(chosenDrawerWidth()).toBe(MAX);
    expect(handle().getAttribute('aria-valuenow')).toBe(String(MAX));
    press('ArrowLeft');
    expect(chosenDrawerWidth()).toBe(MAX);
  });

  it('goes back to the default with Home - not to the narrowest - and forgets the width', () => {
    mount();
    open();
    press('ArrowRight');
    press('ArrowRight');
    press('Home');
    expect(chosenDrawerWidth()).toBeNull();
    expect(stored()).toBeNull();
    expect(handle().getAttribute('aria-valuenow')).toBe('560');
    expect(drawer().className).toContain('w-[var(--drawer-w)]');
  });

  it('writes nothing for a key that moves nothing, so a width chosen on a wider window survives', () => {
    // 900 was chosen on a wider window; this one draws the drawer at 887, its
    // widest. An arrow here must not store 887.
    localStorage.setItem('kubetower.drawer.width', '900');
    mount();
    open();
    press('ArrowLeft');
    press('End');
    expect(stored()).toBe('900');
    expect(chosenDrawerWidth()).toBe(900);
  });

  it('leaves every other key to the page', () => {
    mount();
    open();
    const event = new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true });
    act(() => {
      handle().dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(false);
    expect(chosenDrawerWidth()).toBeNull();
  });
});

describe('the pointer', () => {
  it('widens by the distance dragged left, and remembers it on release', () => {
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 880, pointerId: 1 });
      fireEvent.pointerMove(handle(), { clientX: 780, pointerId: 1 });
    });
    // Drawn under the drag, not yet remembered.
    expect(drawer().style.flexBasis).toBe('660px');
    expect(chosenDrawerWidth()).toBeNull();

    act(() => {
      fireEvent.pointerUp(handle(), { clientX: 780, pointerId: 1 });
    });
    expect(chosenDrawerWidth()).toBe(660);
    expect(stored()).toBe('660');
  });

  it('narrows by the distance dragged right', () => {
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 880, pointerId: 1 });
      fireEvent.pointerMove(handle(), { clientX: 960, pointerId: 1 });
      fireEvent.pointerUp(handle(), { clientX: 960, pointerId: 1 });
    });
    expect(chosenDrawerWidth()).toBe(480);
  });

  it('keeps what is on screen when the pointer is canceled', () => {
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 880, pointerId: 1 });
      fireEvent.pointerMove(handle(), { clientX: 840, pointerId: 1 });
      fireEvent.pointerCancel(handle(), { pointerId: 1 });
    });
    expect(chosenDrawerWidth()).toBe(600);
  });

  it('stores nothing for a press that moved nothing', () => {
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 880, pointerId: 1 });
      fireEvent.pointerUp(handle(), { clientX: 880, pointerId: 1 });
    });
    expect(chosenDrawerWidth()).toBeNull();
    expect(stored()).toBeNull();
  });

  it('leaves a width chosen on a wider window alone when a press moved nothing', () => {
    // 900 is drawn at
    // 887 here, so a press that stored what it found would store 887.
    localStorage.setItem('kubetower.drawer.width', '900');
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 305, pointerId: 1 });
      fireEvent.pointerUp(handle(), { clientX: 305, pointerId: 1 });
    });
    expect(stored()).toBe('900');
    expect(chosenDrawerWidth()).toBe(900);
  });

  it('is held inside the bounds whichever way it is dragged', () => {
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 880, pointerId: 1 });
      fireEvent.pointerMove(handle(), { clientX: 0, pointerId: 1 });
    });
    expect(drawer().style.flexBasis).toBe(`${String(MAX)}px`);
    act(() => {
      fireEvent.pointerMove(handle(), { clientX: 1400, pointerId: 1 });
      fireEvent.pointerUp(handle(), { clientX: 1400, pointerId: 1 });
    });
    expect(chosenDrawerWidth()).toBe(NEED - 1);
  });

  it('forgets a drag the drawer was closed under, and stores nothing', () => {
    // Escape mid-drag unmounts the handle before any release; the next drawer
    // must not open at the dragged width.
    mount();
    open();
    act(() => {
      fireEvent.pointerDown(handle(), { button: 0, clientX: 880, pointerId: 1 });
      fireEvent.pointerMove(handle(), { clientX: 700, pointerId: 1 });
    });
    expect(drawer().style.flexBasis).toBe('740px');
    act(() => {
      probe.shell?.closeDrawer();
    });
    open('ledger-api');
    expect(drawer().style.flexBasis).toBe('');
    expect(drawer().className).toContain('w-[var(--drawer-w)]');
    expect(stored()).toBeNull();
  });

  it('goes back to the default on a double click', () => {
    mount();
    open();
    press('End');
    act(() => {
      fireEvent.doubleClick(handle());
    });
    expect(chosenDrawerWidth()).toBeNull();
    // React leaves an empty `style` behind; what matters is that no basis does.
    expect(drawer().style.flexBasis).toBe('');
    expect(drawer().className).toContain('w-[var(--drawer-w)]');
  });
});

describe('the width is remembered for every kind, and storage may refuse it', () => {
  it('opens the next drawer at the width the last one was left at', () => {
    localStorage.setItem('kubetower.drawer.width', '700');
    mount();
    open();
    expect(drawer().style.flexBasis).toBe('700px');
    open('storefront-svc');
    expect(drawer().style.flexBasis).toBe('700px');
  });

  it('still resizes, for this window, when storage refuses the write', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError');
    });
    mount();
    open();
    press('ArrowLeft');
    expect(drawer().style.flexBasis).toBe('576px');
  });
});
