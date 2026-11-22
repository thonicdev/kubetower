import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from 'react';
import { useLocation } from 'react-router';

import { focusRingFlush } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import {
  chooseDrawerWidth,
  chosenDrawerWidth,
  clampDrawerWidth,
  defaultDrawerWidth,
  DRAWER_FLOOR,
  drawerBounds,
  FINE,
  isDefaultWidth,
  STEP,
  subscribeToDrawerWidth,
  type DrawerBounds,
} from './drawerWidth';
import { useShell } from './ShellContext';

/**
 * The detail drawer.
 *
 * Two behaviors, each guarding against a real defect:
 *
 * **It empties on every navigation.** Otherwise: open a pod's drawer, click
 * another cluster in the rail, and the pod is still there - an object from a
 * cluster you are no longer looking at, with its verbs still on it. Those
 * buttons carry the cluster the drawer was opened from, so a Delete pressed on
 * what looks like staging goes to production. Here it is one effect on the
 * location: the rule expressed once instead of per link.
 *
 * **And it is keyed on the path, not on the whole location.** The object panel
 * writes the chosen tab into the query (`?tab=metrics`, so a reload and a
 * pasted link land on what was being read), so watching `search` too would
 * **close the drawer on any tab click inside it**. Two rules
 * that are each right - *a navigation empties the drawer* and *the tab lives in
 * the address* - would collide on the one word "navigation".
 *
 * A query change on the same path is not a navigation in the sense this rule
 * means. The rule is about arriving somewhere else: another cluster, another
 * kind, another object. `pathname` is exactly that, and it still fires for every
 * case above.
 *
 * **Escape closes it, last.** It claims `KeyLayer.Drawer`, the bottom of the
 * stack, so a dialog, a confirmation or a menu over it all get the key first.
 *
 * **Two ways to an empty drawer, and they are not the same call**.
 * The navigation *empties* it - `emptyDrawer`, and what was open is remembered
 * per cluster so that the rail tile can bring it back through `?open=`. Escape
 * *closes* it - `closeDrawer`, the person's own gesture, and the memory is
 * forgotten with it. Nothing is carried across a navigation either way: the
 * memory is an address the destination page reads, never a drawer that stays.
 *
 * **Its inner edge is a handle that resizes it**. The
 * bounds and the memory are `drawerWidth.ts`; the clamp is the stylesheet's.
 * With nothing chosen the drawer is `--drawer-w` and `shrink-0`, exactly as it
 * always was. With a width chosen it is a flex item based at that width and
 * the first to give way: its shrink factor is `GIVES_WAY` against the list
 * column's 1, and the list column is based at `LIST_MIN` (`Shell.tsx`). Its
 * floor is `min(--drawer-w, max(chosen, --drawer-need))`:
 *
 * - **widened** (chosen over the default), the floor is the default, so a
 *   window made narrower takes the pixels off the drawer until it is back at
 *   the default, and only then off the list;
 * - **narrowed**, the floor is the chosen width, or what the content measured
 *   (`--drawer-need`, written by the handle) where that is wider - so a width
 *   chosen on a secret, or stored before a pod's verbs arrived, is drawn as
 *   wide as the pod needs and no narrower;
 * - and never above the default, so a narrow window draws what it always drew.
 *
 * That is the clamp on a resize and on a reload, with no listener to keep in
 * step.
 */
export function Drawer(): ReactNode {
  const { drawer, closeDrawer, emptyDrawer } = useShell();
  const { pathname } = useLocation();

  useEffect(() => {
    emptyDrawer();
    // Deliberately keyed on the path and not on `emptyDrawer`: the point is "the
    // person arrived somewhere else", and the cluster change is only the
    // loudest case. **Not `search`** - see the header: the object panel puts its
    // chosen tab in the query, and watching it here closed the drawer on every
    // tab click.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pathname]);

  useEscape(KeyLayer.Drawer, closeDrawer, drawer !== null);

  const chosen = useSyncExternalStore(subscribeToDrawerWidth, chosenDrawerWidth);
  // The width under a drag, drawn but not yet remembered.
  const [dragged, setDragged] = useState<number | null>(null);
  const width = dragged ?? chosen;

  return (
    <aside
      id="drawer"
      hidden={drawer === null}
      className={`relative overflow-auto border-l border-line bg-panel ${SHEET} ${
        width === null ? 'w-[var(--drawer-w)] shrink-0' : ''
      }`}
      style={
        width === null
          ? undefined
          : {
              flexGrow: 0,
              flexShrink: GIVES_WAY,
              flexBasis: `${String(width)}px`,
              minWidth: `min(var(--drawer-w), max(${String(width)}px, var(--drawer-need, 0px)))`,
            }
      }
    >
      {drawer === null ? null : <DrawerGrip chosen={chosen} onDrag={setDragged} />}
      {drawer}
    </aside>
  );
}

/**
 * Below `sm` the drawer is a sheet: it covers the whole box it shares with the
 * list (`Shell.tsx`) instead of taking a share of it. At 390px a drawer at
 * `--drawer-w` was 179px beside a 130px list, and neither could be read - its
 * cards printed one character a line and its tabs were cut. As a sheet it has
 * the content area's whole width, its header's close X takes the list back,
 * and nothing about the list underneath changes.
 *
 * `!` on the width and the floor, because a width the person chose elsewhere
 * is an inline style (below) and a phone has no room to honor it. The handle
 * is not drawn: there is nothing beside the sheet to give or take a width from.
 * The breakpoint is the one the top bar already folds at.
 */
const SHEET = 'max-sm:absolute max-sm:inset-0 max-sm:z-20 max-sm:w-auto! max-sm:min-w-0! max-sm:border-l-0';

/**
 * The drawer's shrink factor against the list column's 1. Flex shrinks in
 * proportion to factor times basis, so the list's share of an overflow is
 * 305 against a million times the drawer's basis: a thousandth of a pixel. At
 * 1000 it was a tenth, measured at 1280 and 1024 - a list drawn 304.9px wide
 * where the default drawer leaves it 304.97.
 */
const GIVES_WAY = 1_000_000;

/** What the handle reads from the page: the width drawn, and its bounds. */
interface Reading {
  readonly now: number;
  readonly bounds: DrawerBounds;
}

/**
 * The narrowest width the drawer's content fits in, measured from the drawer on
 * screen: its header, its controls row and its tab strip, each laid out **on
 * one line at its natural width** for the length of one synchronous read and
 * put back before anything is drawn. `null` where there is nothing to measure -
 * not an object's panel, or a page with no layout (jsdom) - which keeps the
 * default as the floor.
 *
 * - every row is `width: max-content`, and every `flex-wrap` inside it
 *   `nowrap`: the controls row wraps by design, and the question is the
 *   width at which it does not have to;
 * - what truncates by design - the object's name, its namespace - counts
 *   nothing: a name is one unbreakable token that no width is promised to hold,
 *   and it says so on hover;
 * - the empty spacer before the verbs counts its margins and nothing else, as it
 *   does at the width where the row just fits.
 *
 * Plus the drawer's own border, because the rows are inside it.
 */
function contentNeed(aside: HTMLElement): number | null {
  const panel = aside.querySelector<HTMLElement>('[data-object-chrome="drawer"]');
  if (panel === null) return null;
  const rows = [...panel.children].filter(
    (row): row is HTMLElement =>
      row instanceof HTMLElement &&
      (row.tagName === 'HEADER' || row.hasAttribute('data-object-controls') || row.getAttribute('role') === 'tablist'),
  );
  const undo: (() => void)[] = [];
  const set = (element: HTMLElement, property: string, value: string): void => {
    const was = element.style.getPropertyValue(property);
    element.style.setProperty(property, value);
    undo.push(() => {
      element.style.setProperty(property, was);
    });
  };
  let need = 0;
  try {
    for (const row of rows) {
      set(row, 'width', 'max-content');
      for (const inner of [row, ...row.querySelectorAll<HTMLElement>('*')]) {
        const style = getComputedStyle(inner);
        if (style.flexWrap === 'wrap') set(inner, 'flex-wrap', 'nowrap');
        if (style.textOverflow === 'ellipsis') set(inner, 'display', 'none');
      }
    }
    for (const row of rows) need = Math.max(need, row.getBoundingClientRect().width);
  } finally {
    for (const put of undo.reverse()) put();
  }
  if (need <= 0) return null;
  return need + (Number.parseFloat(getComputedStyle(aside).borderLeftWidth) || 0);
}

/**
 * The drawn width, the room it shares with the list beside it - the drawer's
 * previous sibling in `Shell.tsx` - and what its content needs. Read at the
 * moment it is needed, because the room changes with the window and the kinds
 * column, and the need with the object.
 */
function measure(aside: HTMLElement): Reading {
  const now = aside.getBoundingClientRect().width;
  const list = aside.previousElementSibling;
  const room = now + (list === null ? 0 : list.getBoundingClientRect().width);
  return { now, bounds: drawerBounds(window.innerWidth, room, contentNeed(aside)) };
}

/** Writes what the content needs - never under the measured floor - where the
 *  stylesheet reads it. */
function publishNeed(aside: HTMLElement): void {
  const need = contentNeed(aside);
  if (need === null) aside.style.removeProperty('--drawer-need');
  else aside.style.setProperty('--drawer-need', `${String(Math.max(DRAWER_FLOOR, Math.ceil(need)))}px`);
}

/**
 * The handle on the drawer's inner edge.
 *
 * **The dock's grip and the column grip, not a third mechanism.** Pointer
 * events with `setPointerCapture`, as `Dock.tsx`'s `Grip`, so a trackpad and a
 * touchscreen work and the drag survives leaving a six-pixel strip; the column
 * grip's keyboard (`DataGrid.tsx`): the arrows step 16px, Shift 4px, both ways
 * - plus what a splitter has and a column does not, Home for the default and
 * End for the widest. And the column grip's two traps:
 *
 * - **a press that moved nothing stores nothing** - a click on the edge is an
 *   ordinary click to a person, and must not freeze today's width into a
 *   choice, nor overwrite a width chosen on a wider window with the one this
 *   window could draw;
 * - **`pointercancel` keeps what is on screen**, as `pointerup` does.
 *
 * Two of its own:
 *
 * - **a key that moves nothing writes nothing** - on a window where the drawer
 *   is already as wide as it may go, storing the clamped width would erase the
 *   one chosen on a wider window;
 * - **a drawer closed mid-drag forgets the drag** - Escape unmounts this handle
 *   before any `pointerup`, and the next drawer opened at a width nobody chose.
 *
 * Exactly the default is the default: a width dragged or stepped onto it is
 * forgotten rather than stored, so the drawer follows `--drawer-w` again, and
 * Home or a double click does the same from anywhere.
 *
 * Drawn on hover, on focus and under a drag only - the drawer's border is the
 * edge the rest of the time. It sits inside the drawer, which scrolls nothing
 * but its tab panel (layout test 21), so it is the drawer's full height.
 */
function DrawerGrip({
  chosen,
  onDrag,
}: {
  chosen: number | null;
  onDrag: (width: number | null) => void;
}): ReactNode {
  const grip = useRef<HTMLDivElement>(null);
  const drag = useRef<{ x: number; start: number; reached: number; bounds: DrawerBounds } | null>(null);
  const [reading, setReading] = useState<Reading | null>(null);
  const [dragging, setDragging] = useState(false);

  // What the content needs, kept where the stylesheet reads it: measured before
  // the first paint, and again whenever the drawer's content changes - another
  // object, or the payload's verbs arriving. Only written to the DOM, never to
  // state, so it costs no render; the floor it feeds is the stylesheet's.
  useLayoutEffect(() => {
    const aside = grip.current?.parentElement;
    if (aside === null || aside === undefined) return;
    publishNeed(aside);
    if (typeof MutationObserver === 'undefined') return;
    let frame = 0;
    const observer = new MutationObserver(() => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        publishNeed(aside);
      });
    });
    observer.observe(aside, { childList: true, subtree: true, characterData: true });
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      aside.style.removeProperty('--drawer-need');
    };
  }, []);

  // A drag the drawer was closed under is let go of, not kept. Nothing is
  // stored: nobody let go of the handle.
  useEffect(
    () => () => {
      if (drag.current !== null) {
        drag.current = null;
        onDrag(null);
      }
    },
    [onDrag],
  );

  // Kept current as the window, the kinds column or the drawer itself changes,
  // for the `aria-value*` below. Set from the observer's callback, never from
  // the effect's body. jsdom has no observer, and there the reading is taken on
  // focus and on each gesture instead.
  useEffect(() => {
    const aside = grip.current?.parentElement;
    if (aside === null || aside === undefined || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => {
      setReading(measure(aside));
    });
    observer.observe(aside);
    if (aside.previousElementSibling !== null) observer.observe(aside.previousElementSibling);
    return () => {
      observer.disconnect();
    };
  }, []);

  const aside = (): HTMLElement | null => grip.current?.parentElement ?? null;

  /** Remember a width - or forget it, exactly at the default - and say so. */
  const keep = (width: number, bounds: DrawerBounds): void => {
    const next = isDefaultWidth(width, window.innerWidth) ? null : Math.round(width);
    setReading({ now: next ?? defaultDrawerWidth(window.innerWidth), bounds });
    if (next !== chosen) chooseDrawerWidth(next);
  };

  const down = (event: PointerEvent<HTMLDivElement>): void => {
    const box = aside();
    if (event.button !== 0 || box === null) return;
    // Keeps the press from selecting the text the pointer travels over.
    event.preventDefault();
    const { now, bounds } = measure(box);
    drag.current = { x: event.clientX, start: now, reached: now, bounds };
    setDragging(true);
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
    } catch {
      // Not every browser, and the drag still works without it.
    }
  };

  const move = (event: PointerEvent<HTMLDivElement>): void => {
    const held = drag.current;
    if (held === null) return;
    // The handle is on the left edge: moving left widens.
    held.reached = clampDrawerWidth(held.start - (event.clientX - held.x), held.bounds);
    onDrag(held.reached);
    setReading({ now: held.reached, bounds: held.bounds });
  };

  const up = (event: PointerEvent<HTMLDivElement>): void => {
    const held = drag.current;
    if (held === null) return;
    drag.current = null;
    setDragging(false);
    try {
      event.currentTarget.releasePointerCapture(event.pointerId);
    } catch {
      // Already released, or never captured.
    }
    // Stored before the drag width is let go, so no frame draws the old width.
    if (Math.abs(held.reached - held.start) >= 1) keep(held.reached, held.bounds);
    onDrag(null);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    const box = aside();
    if (box === null) return;
    const { now, bounds } = measure(box);
    const step = event.shiftKey ? FINE : STEP;
    if (event.key === 'Home') {
      event.preventDefault();
      reset();
      return;
    }
    let next: number;
    if (event.key === 'ArrowLeft') next = clampDrawerWidth(now + step, bounds);
    else if (event.key === 'ArrowRight') next = clampDrawerWidth(now - step, bounds);
    else if (event.key === 'End') next = bounds.max;
    else return;
    event.preventDefault();
    // Nothing moved: nothing is written, so a width chosen on a wider window
    // survives a key pressed on this one.
    if (Math.abs(next - now) < 0.5) {
      setReading({ now, bounds });
      return;
    }
    keep(next, bounds);
  };

  const reset = (): void => {
    if (chosen !== null) chooseDrawerWidth(null);
    setReading(null);
  };

  // Before anything was measured: the width asked for, and the default as both
  // bounds - the floor nothing measured keeps.
  const fallback = defaultDrawerWidth(window.innerWidth);
  const now = Math.round(reading?.now ?? chosen ?? fallback);
  const min = Math.round(reading?.bounds.min ?? Math.min(fallback, now));
  const max = Math.round(reading?.bounds.max ?? Math.max(now, fallback));

  return (
    <div
      ref={grip}
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize the panel"
      aria-controls="drawer"
      aria-valuenow={now}
      aria-valuemin={min}
      aria-valuemax={max}
      tabIndex={0}
      title="Drag to resize the panel. Arrow keys resize; Shift for a finer step; End for the widest; Home or a double click goes back to the default width."
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={up}
      onKeyDown={onKeyDown}
      onDoubleClick={reset}
      onFocus={() => {
        const box = aside();
        if (box !== null) setReading(measure(box));
      }}
      className={`absolute inset-y-0 left-0 z-10 w-1.5 cursor-col-resize max-sm:hidden touch-none before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:content-[''] hover:before:bg-accent focus-visible:before:bg-accent ${
        dragging ? 'before:bg-accent' : ''
      } ${focusRingFlush}`}
    />
  );
}
