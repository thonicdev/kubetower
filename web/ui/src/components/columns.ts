/**
 * The column arrangement: what is shown, and in what order.
 *
 * Pure functions over whatever the server said the columns are. They are
 * separate from the menu that edits them and from the grid that draws them,
 * because the one property worth asserting about an arrangement is easiest to
 * state about a function: **a stored layout that no longer matches the table
 * draws neither a ghost nor a gap.**
 *
 * That is not hypothetical. A column preference is stored per kind and outlives
 * the console that wrote it, so the two ordinary states of a stored layout are
 * "names a column this release removed" and "does not name a column this release
 * added". The server resolves the first — the payload lists the columns that
 * exist and nothing else, so a removed column cannot come back as a ghost — and
 * the arrangement below resolves the second: a column with no stored order sorts
 * by where the server put it and is drawn, rather than falling off the end of a
 * list of remembered keys.
 *
 * **Ties are broken by the server's own order, and that is the whole of the
 * merge.** `Array.prototype.sort` has been required to be stable since ES2019,
 * so two columns sharing an `order` stay in payload order rather than swapping
 * on a re-render — which would be a column that moves when nothing moved it.
 */

export interface Arrangeable {
  key: string;
  visible: boolean;
  order: number;
  /**
   * Off by default, rather than hidden by somebody.
   *
   * Optional here and required on `Column`, so this module stays usable for any
   * arrangeable list while the resource grid gets the real fact. It is the
   * difference between *the operator hid this* and *it is off by default*, and
   * `api/openapi.yaml` says in as many words that the difference "decides what
   * resetting means".
   */
  secondary?: boolean;
  /**
   * Why this column cannot be hidden, where it cannot.
   *
   * Drawn as a fact and never as a control that refuses: *"a toggle that does
   * nothing is worse than an explained absence."* Distinct from `isLastVisible`
   * below, which is this client's own rule about the last column standing; this
   * is the server's, about one particular column.
   */
  lockedNote?: string;
}

/** Every column, in the order they are drawn — hidden ones included. What the
 *  menu lists, so that showing one again is possible. */
export function arrangeAll<C extends Arrangeable>(columns: readonly C[]): readonly C[] {
  return [...columns].sort((a, b) => a.order - b.order);
}

/** The columns the grid draws. */
export function arrange<C extends Arrangeable>(columns: readonly C[]): readonly C[] {
  return arrangeAll(columns).filter((column) => column.visible);
}

/**
 * Whether hiding this column would leave the grid with none.
 *
 * A table with no columns is not a narrower table, it is a page that has lost
 * its contents to a checkbox — and the way back is another checkbox in a menu
 * that is now attached to nothing. The last visible column cannot be hidden.
 */
export function isLastVisible<C extends Arrangeable>(
  columns: readonly C[],
  key: string,
): boolean {
  const visible = columns.filter((column) => column.visible);
  return visible.length === 1 && visible[0]?.key === key;
}
