import { useLayoutEffect, useRef, useState, type RefObject } from 'react';

/**
 * Lists longer than this are drawn through the window below; a shorter one is
 * drawn whole, exactly as it always was.
 */
export const WINDOW_AFTER = 60;

/** Rows drawn above and below what is visible, so a fast scroll finds rows. */
const OVERSCAN = 12;

/** The viewport height assumed where none can be measured. */
const FALLBACK_VIEWPORT = 800;

/** A half-open range of row indexes, `[from, to)`. */
export interface Span {
  from: number;
  to: number;
}

function scrollParent(from: Element | null): HTMLElement | null {
  for (let at = from?.parentElement ?? null; at !== null; at = at.parentElement) {
    const overflow = getComputedStyle(at).overflowY;
    if (overflow === 'auto' || overflow === 'scroll' || overflow === 'overlay') return at;
  }
  return null;
}

/**
 * Which rows of a long table to draw: the ones in view plus an overscan, and
 * any row in `pinned` (the one holding the focus, the one whose menu is open),
 * so that scrolling never unmounts an element the person is working with.
 *
 * The table keeps being a native `<table>`: the rows not drawn are stood in for
 * by spacer rows of the same total height, so the scrollbar, the sticky head
 * and the column layout behave as with every row present. The position is read
 * from where the body sits against the nearest scrolling ancestor, so it does
 * not matter which element scrolls.
 *
 * Returns the spans to draw, in order; the gaps between them (and before and
 * after) are the spacers' heights.
 */
export function useRowWindow(
  body: RefObject<HTMLTableSectionElement | null>,
  count: number,
  rowHeight: number,
  pinned: readonly number[],
): Span[] {
  const on = count > WINDOW_AFTER;
  const [seen, setSeen] = useState<Span>({ from: 0, to: Math.ceil(FALLBACK_VIEWPORT / rowHeight) });
  const read = useRef<() => void>(() => undefined);

  const measure = (): void => {
    const tbody = body.current;
    if (tbody === null) return;
    const scroller = scrollParent(tbody);
    const top = scroller === null ? 0 : scroller.getBoundingClientRect().top;
    const height = (scroller === null ? window.innerHeight : scroller.clientHeight) || FALLBACK_VIEWPORT;
    const above = top - tbody.getBoundingClientRect().top;
    const from = Math.max(0, Math.floor(above / rowHeight));
    const to = Math.max(from, Math.ceil((above + height) / rowHeight));
    setSeen((held) => (held.from === from && held.to === to ? held : { from, to }));
  };

  // After every render: a new answer or a filter moves the body under the
  // viewport without a scroll event. The scroll listener below reads through
  // the ref, so it always calls the measure of the latest render.
  useLayoutEffect(() => {
    read.current = measure;
    // A microtask, so the state is set from a callback and not in the effect's
    // body; it still runs before the browser paints.
    if (on) queueMicrotask(measure);
  });

  useLayoutEffect(() => {
    if (!on) return;
    const tbody = body.current;
    if (tbody === null) return;
    const scroller = scrollParent(tbody);
    const target: HTMLElement | Window = scroller ?? window;
    let frame = 0;
    const later = (): void => {
      if (frame !== 0) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        read.current();
      });
    };
    target.addEventListener('scroll', later, { passive: true });
    window.addEventListener('resize', later);
    const watch = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(later);
    if (scroller !== null) watch?.observe(scroller);
    return () => {
      target.removeEventListener('scroll', later);
      window.removeEventListener('resize', later);
      watch?.disconnect();
      if (frame !== 0) cancelAnimationFrame(frame);
    };
  }, [on, body]);

  if (!on) return [{ from: 0, to: count }];

  const from = Math.max(0, Math.min(seen.from, count) - OVERSCAN);
  const to = Math.min(count, Math.max(seen.to, from) + OVERSCAN);
  const spans: Span[] = [{ from, to }];
  for (const at of pinned) {
    if (at < 0 || at >= count) continue;
    spans.push({ from: at, to: at + 1 });
  }
  spans.sort((a, b) => a.from - b.from);
  const merged: Span[] = [];
  for (const span of spans) {
    const last = merged[merged.length - 1];
    if (last !== undefined && span.from <= last.to) last.to = Math.max(last.to, span.to);
    else merged.push({ ...span });
  }
  return merged;
}
