import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react';

import { compileFilter } from '../dock/logBuffer';
import { KeyLayer } from '../keyboard/layers';
import { useEscape, useKeyLayer } from '../keyboard/useKeyLayer';
import { useSettling } from './useDebounced';

/**
 * A search inside one view the console draws itself: an object's manifest, and
 * the keys of a ConfigMap or a Secret.
 *
 * ## Why the browser's own find is not enough
 *
 * It finds text in the document, and one thing on these views is not text in
 * the document: a Secret's value, which is not in the page at all until
 * somebody presses *Show*. The browser's find misses it, and must; a search
 * that is ours has to miss it just as surely - see `Data.tsx`, which decides
 * what is searchable, and never this hook. A ConfigMap's value is drawn whole,
 * with no fold to search past.
 *
 * ## What this holds, and what it leaves to the view
 *
 * The bar's state: open or not, what was typed, what that settled into, the
 * case toggle, and which match is current. **Not the matches.** What a view's
 * text is - the manifest's tokens, a ConfigMap's keys and inline values, a
 * Secret's keys and the values somebody revealed - is the view's to say, and so
 * is how many matches that makes; every method that moves between them is
 * handed the count rather than remembering one that a revealed or hidden value
 * has since changed.
 *
 * ## The settling is the log pane's
 *
 * `useSettling` at 140 ms, the figure and the hook `LogPane.tsx` filters
 * with: one pass over what is in hand per pause in the typing, and Enter
 * settles at once. Here Enter does a second thing once the text has settled,
 * which is step to the next match; Shift+Enter steps back. A cleared box clears
 * at once rather than 140 ms later, because the highlight it would leave
 * behind belongs to nothing on screen.
 *
 * ## Ctrl+F, and only while the view holds the keyboard
 *
 * ⌘F on an Apple keyboard, which carries `metaKey`: the same either-modifier
 * test `shell/Menu.tsx` makes for Ctrl+K and Ctrl+B. Ctrl+R, and every other
 * chord, is left to the browser - only `f` is looked at.
 *
 * Claimed at `KeyLayer.Page` and only while the view is mounted and has
 * something to search, so the key is the browser's again the moment the view is
 * not on screen: another tab of the object, another page, an object still
 * loading. And even then it is taken **only when the keyboard is with the
 * view** - `holdsTheKeyboard` below: the focus is inside the view's region (the
 * object's panel, for both views), or nowhere in particular, which is where a
 * click on the view's text leaves it. A terminal in the dock, the list's filter
 * box or a dialog over the page keeps Ctrl+F for itself or for the browser.
 *
 * ## Escape takes the bar off first
 *
 * At `KeyLayer.Find`, above the drawer the bar is drawn in, so one Escape closes
 * the search and clears its highlight, and the next closes the drawer. The
 * focus goes back to wherever it was when the bar was opened, as a dialog gives
 * it back to its opener: an Escape that dropped the keyboard on `<body>` would
 * leave the reader's next keystrokes going nowhere they chose.
 */
export interface Find {
  /** Whether the bar is drawn. */
  readonly open: boolean;
  /** What the box holds, as typed. */
  readonly typed: string;
  readonly matchCase: boolean;
  /** What to match, settled; `null` while closed or empty. Global, so whoever
   *  runs it starts from `lastIndex` 0 - `findRanges` does. */
  readonly pattern: RegExp | null;
  /** The box, for the bar to attach and for the hotkey to focus. */
  readonly input: RefObject<HTMLInputElement | null>;
  /** Open the bar, or bring the keyboard back to it if it is open. */
  show: () => void;
  /** Close the bar and clear what it found. */
  close: () => void;
  type: (text: string) => void;
  setMatchCase: (on: boolean) => void;
  /** Which match is current, from 0, for a view that has `count` of them. */
  current: (count: number) => number;
  /** Move by `by` matches, wrapping at either end. */
  step: (by: number, count: number) => void;
  /** Enter in the box: settle if the typing has not, else step. */
  enter: (backwards: boolean, count: number) => void;
}

export interface FindOptions {
  /** The view the bar searches. Its current match is found in it by
   *  `data-find-current` and scrolled into sight. */
  view: RefObject<HTMLElement | null>;
  /** Whether there is anything to search. False takes the hotkey back. */
  enabled: boolean;
  /** The region that counts as the view for the hotkey - a selector matched
   *  from the view outwards, or the view itself when absent or not found. */
  region?: string | undefined;
}

export function useFind({ view, enabled, region }: FindOptions): Find {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState('');
  const [settled, settleNow] = useSettling(typed, 140);
  const [matchCase, setMatchCase] = useState(false);
  const [at, setAt] = useState(0);
  // How many times the keyboard has been asked into the box. A counter and not
  // a flag, so that asking a second time - Ctrl+F with the bar already open -
  // is a change the effect below sees.
  const [asked, setAsked] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  // Where the keyboard was when the bar was asked for, to give it back to.
  // Never rendered, so a ref.
  const opener = useRef<HTMLElement | null>(null);

  const query = typed === '' ? '' : settled;
  const pattern = useMemo(
    // `compileFilter` with the regular expression off cannot fail: it escapes
    // the text, so its error arm is never taken here and the bar draws none.
    () => (open ? compileFilter(query, false, matchCase).pattern : null),
    [open, query, matchCase],
  );

  useEffect(() => {
    if (asked === 0) return;
    const box = input.current;
    if (box === null) return;
    box.focus({ preventScroll: true });
    box.select();
  }, [asked]);

  // The current match into sight, after the paint that marked it. Its own
  // effect rather than part of `step`, because a match becomes current in three
  // ways - typing, stepping and the case toggle - and the mark it moves to
  // carries `data-find-current` only once that render is committed.
  useEffect(() => {
    if (pattern === null) return;
    const current = view.current?.querySelector('[data-find-current]');
    // jsdom has no layout and no `scrollIntoView`; a browser always has one.
    if (current instanceof HTMLElement && typeof current.scrollIntoView === 'function') {
      current.scrollIntoView({ block: 'center', inline: 'nearest' });
    }
  }, [pattern, at, view]);

  const show = useCallback(() => {
    if (!open) {
      const active = document.activeElement;
      opener.current = active instanceof HTMLElement && active !== document.body ? active : null;
    }
    setOpen(true);
    setAsked((n) => n + 1);
  }, [open]);

  const close = useCallback(() => {
    // Only when the keyboard is in the bar: an Escape pressed with the focus on
    // the document has nothing to give back, and pulling the focus to the
    // opener from there would move it for no reason the reader can see.
    const bar = input.current?.closest('[data-find-bar]');
    const back = opener.current;
    if (bar?.contains(document.activeElement) === true && back !== null && document.contains(back)) {
      back.focus({ preventScroll: true });
    }
    opener.current = null;
    setOpen(false);
    setTyped('');
    setAt(0);
  }, []);

  const type = useCallback((text: string) => {
    setTyped(text);
    setAt(0);
  }, []);

  const toggleCase = useCallback((on: boolean) => {
    setMatchCase(on);
    setAt(0);
  }, []);

  const current = useCallback(
    (count: number) => (count === 0 ? 0 : Math.min(at, count - 1)),
    [at],
  );

  const step = useCallback(
    (by: number, count: number) => {
      if (count === 0) return;
      const from = Math.min(at, count - 1);
      setAt((((from + by) % count) + count) % count);
    },
    [at],
  );

  const enter = useCallback(
    (backwards: boolean, count: number) => {
      // Typed faster than it settled: Enter is "search now", as in the log
      // pane, and the first match becomes current. Stepping past it as well
      // would land on the second match of a search nobody has seen yet.
      if (typed !== settled) {
        settleNow();
        return;
      }
      step(backwards ? -1 : 1, count);
    },
    [typed, settled, settleNow, step],
  );

  useKeyLayer(
    KeyLayer.Page,
    (event) => {
      if (event.key.toLowerCase() !== 'f') return false;
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey) return false;
      if (!holdsTheKeyboard(view.current, input.current, region, event.target)) return false;
      show();
      return true;
    },
    enabled,
  );

  useEscape(KeyLayer.Find, close, open);

  return {
    open,
    typed,
    matchCase,
    pattern,
    input,
    show,
    close,
    type,
    setMatchCase: toggleCase,
    current,
    step,
    enter,
  };
}

/**
 * Whether a Ctrl+F belongs to this view rather than to whatever else has the
 * keyboard.
 *
 * - **Not under a modal** that is not the view's own: a dialog's keys are the
 *   dialog's, and a find bar opening behind one would take the focus out of it.
 *   The dialog does not claim Ctrl+F, so the stack would hand it down here.
 * - **The focus nowhere in particular** - `<body>` - is the view's: a click on
 *   the manifest's text or a value's leaves the focus there, and that click is
 *   the reader saying where they are reading.
 * - **The bar's own box** is, so Ctrl+F in it selects what was typed, as a
 *   browser's find bar does.
 * - **The text the view searches** is, even where it takes typing: the
 *   manifest editor's document carries `data-find-text`.
 * - **Any other text field** is not: the dock's terminal is a text area, and
 *   Ctrl+F there is a shell's forward-a-character. `shell/Menu.tsx` makes the
 *   same check for its own hotkeys.
 * - Otherwise, the focus has to be inside the view's region.
 */
function holdsTheKeyboard(
  view: HTMLElement | null,
  box: HTMLInputElement | null,
  region: string | undefined,
  target: EventTarget | null,
): boolean {
  if (view === null || !view.isConnected) return false;
  const modal = document.querySelector('[aria-modal="true"]');
  if (modal !== null && !modal.contains(view)) return false;
  if (!(target instanceof HTMLElement) || target === document.body || target === document.documentElement) {
    return true;
  }
  if (target === box) return true;
  // The text the view searches, even where it is a field: the manifest
  // editor's document is typed into, and Ctrl+F there is a search of it.
  if (view.contains(target) && target.closest('[data-find-text]') !== null) return true;
  if (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)) return false;
  const scope = (region === undefined ? null : view.closest(region)) ?? view;
  return scope.contains(target);
}

/** One match in a text: where it starts and where it ends, as string offsets. */
export interface Range {
  readonly from: number;
  readonly to: number;
}

/**
 * Every match of `pattern` in `text`, in order - or the first `limit` of them.
 *
 * A zero-length match ends the walk, as it does in `logBuffer.ts`'s `pieces`:
 * it leaves `lastIndex` where it was, and the loop would never end. The escaped
 * text this bar compiles cannot match nothing, but the guard costs a comparison
 * and the failure costs the tab.
 *
 * `limit` stops the walk rather than trimming after it: a caller that marks no
 * more than a cap of hits across a data block (`Data.tsx`) asks each
 * value for one more than what is left of its cap, to learn that there were
 * more, and the walk over a million one-character matches is never made.
 */
export function findRanges(text: string, pattern: RegExp | null, limit = Infinity): Range[] {
  if (pattern === null || text === '' || limit <= 0) return [];
  const out: Range[] = [];
  pattern.lastIndex = 0;
  let found = pattern.exec(text);
  while (found !== null) {
    if (found[0].length === 0) break;
    out.push({ from: found.index, to: found.index + found[0].length });
    if (out.length >= limit) break;
    found = pattern.exec(text);
  }
  pattern.lastIndex = 0;
  return out;
}
