import { useEffect, useState, type ReactNode, type RefObject } from 'react';

/**
 * A fade at whichever side of the list's panel has more table beyond it.
 *
 * A list fits its panel (`DataGrid`'s `fitToRoom`), so this is drawn only
 * where a table is still wider than its panel - one whose rows outgrew it
 * between two reads, or held wider on purpose. A scrollbar at the foot of the
 * panel would be below the last row of a long list and nowhere near the cut.
 *
 * Drawn over the panel's edge and not inside it: `#panel` must hold the grid
 * and nothing between (the layout's selector), so the fade is a sibling
 * positioned over it, clear of its own scrollbars - which is why their
 * thickness is read rather than assumed. `pointer-events-none`, so it never
 * takes a click meant for the cell under it, and `aria-hidden`: the table is
 * whole to assistive technology, which reads it without scrolling.
 *
 * **The left fade starts where the held column ends.** A list holds its first
 * column at the left edge (`DataGrid`'s `held`, marked `data-held` on its
 * heading), so what has scrolled off now goes under that column's right edge,
 * not under the panel's. Drawn from the panel's edge, the fade would wash out
 * the held tick and the start of every name.
 *
 * Read again on a scroll of the panel, a resize of it or of its table, a new
 * `answer` and a new `columns`. The table is watched as well because a list
 * fits its columns to the panel a frame after the panel changes width
 * (`DataGrid`'s `fitToRoom`): read on the panel's resize alone, the fade would
 * describe the table before it fitted.
 */
export function SidewaysCue({
  panel,
  answer,
  columns,
}: {
  panel: RefObject<HTMLElement | null>;
  /** The list's answer: new rows can make the table wider or narrower. */
  answer: unknown;
  /** The columns drawn and how, as one string: the Columns menu and the row
   *  height change the table's width with no new answer. */
  columns: string;
}): ReactNode {
  const [edges, setEdges] = useState<Edges>(NONE);

  useEffect(() => {
    const node = panel.current;
    if (node === null) return;
    // Absent in jsdom, where nothing has a width and no cue is drawn anyway.
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(() => {
      read();
    });
    const read = (): void => {
      // The table is looked for on every read: it is drawn after the panel.
      // Observing an element twice is a no-op.
      const table = node.querySelector('table');
      if (table !== null) observer?.observe(table);
      const next = edgesOf(node);
      setEdges((previous) => (sameEdges(previous, next) ? previous : next));
    };
    read();
    node.addEventListener('scroll', read, { passive: true });
    observer?.observe(node);
    return () => {
      node.removeEventListener('scroll', read);
      observer?.disconnect();
    };
  }, [panel, answer, columns]);

  return (
    <>
      {edges.before ? (
        <div
          aria-hidden="true"
          data-sideways="before"
          style={{ left: edges.start, bottom: edges.bar }}
          className="pointer-events-none absolute top-0 z-2 w-8 bg-linear-to-r from-bg to-transparent"
        />
      ) : null}
      {edges.after ? (
        <div
          aria-hidden="true"
          data-sideways="after"
          style={{ right: edges.gutter, bottom: edges.bar }}
          className="pointer-events-none absolute top-0 z-2 w-8 bg-linear-to-l from-bg to-transparent"
        />
      ) : null}
    </>
  );
}

interface Edges {
  /** Table beyond the left edge: the panel is scrolled. */
  before: boolean;
  /** Table beyond the right edge. */
  after: boolean;
  /** The vertical scrollbar's thickness, which the fade keeps clear of. */
  gutter: number;
  /** The horizontal one's. */
  bar: number;
  /** The held column's width, where the left fade starts. */
  start: number;
}

const NONE: Edges = { before: false, after: false, gutter: 0, bar: 0, start: 0 };

/** A pixel of slack either side: a fractional scroll position is not "more". */
function edgesOf(node: HTMLElement): Edges {
  const beyond = node.scrollWidth - node.clientWidth - node.scrollLeft;
  return {
    before: node.scrollLeft > 1,
    after: beyond > 1,
    gutter: Math.max(0, node.offsetWidth - node.clientWidth),
    bar: Math.max(0, node.offsetHeight - node.clientHeight),
    start: node.querySelector<HTMLElement>('thead th[data-held]')?.offsetWidth ?? 0,
  };
}

function sameEdges(one: Edges, other: Edges): boolean {
  return (
    one.before === other.before && one.after === other.after && one.gutter === other.gutter && one.bar === other.bar &&
    one.start === other.start
  );
}
