/**
 * The console's one look for a table: what every `<table>`, the extras' pages
 * included, draws its body, its headings and its rows with.
 *
 * A resource list, the Helm list, the port-forwards page, a release's facts and
 * revisions, the overview's panels and an extra's runs or namespaces are all
 * the same object to the person reading them - rows of one subject, read down
 * a column - so they are drawn alike: one body size, one heading, one row height. `DataGrid` takes its classes
 * from here, and so does every hand-written table; `tableLook.test.ts` reads the
 * source and fails on a `<table>` that does not, with the few that are read as
 * code (a diff, a key's value) named there.
 *
 * Full class strings, never assembled by interpolation from fragments: Tailwind
 * finds a utility by scanning the source text.
 */

/**
 * The `<table>`: the layout hook `theme.css` matches (`.kt-grid`, the one-line
 * rule and the list's fixed layout) and **the body size**, 13px, for every
 * table alike. A table drawn at 12px beside a list at 13px reads as a second,
 * smaller kind of table.
 */
export const TABLE = 'kt-grid text-sm';

/** The horizontal padding of every cell, heading and body. */
export const PAD = 'px-2';

/**
 * The heading row: the small upper-case label in the muted tone, with the
 * heading rhythm (`tracking-heading`).
 */
export const HEAD_ROW = 'text-left text-2xs tracking-heading text-muted uppercase';

/**
 * One heading cell, with its rule under it. The rule is on the cell and not on
 * the row: a sticky heading carries its own cells, and a border left on the
 * `<tr>` would scroll away from what it underlines.
 *
 * `pad` is the cell's horizontal padding and it is the body cells' own: a
 * heading padded differently from its column draws off it.
 */
export function headCell(pad: string = PAD): string {
  return `border-b border-line ${pad} py-1.5 font-medium`;
}

/**
 * A body row: half-strength rule, so the row is the unit and the line is only
 * where one ends.
 */
export const ROW = 'border-b border-line/50';

/**
 * One body cell, at either density.
 *
 * ## The height is the row's, and it is stated
 *
 * **A row's height is a fixed `h-9`** (`h-7` compact) and not whatever its
 * tallest content happens to be. Left to its content, a list whose rows carry a
 * pill (Pods: Status) drew 36px rows and a list of plain text (ConfigMaps,
 * Secrets, Ingresses) 28px ones - the same 13px text, and the second list read
 * as a smaller one. On a table cell `height` is a floor, so a wrapping prose
 * cell still grows its row.
 *
 * ## The pill is centered on the line, not stood on it
 *
 * A pill is an `inline-block` that truncates, and an inline-block whose
 * `overflow` is not `visible` takes its **bottom edge** as its baseline. On the
 * baseline of 13px text it then stands a whole pill above the line and pushes
 * the line box 8px taller than the text - the row grew for a chip, not for its
 * words. `align-middle` on every pill a cell holds puts it on the line's middle,
 * inside the row's height.
 *
 * `leading-tight` with the compact padding, because half the height of a row is
 * the line box. `pad` is the heading's own, as for `headCell`.
 */
export function bodyCell(compact = false, pad: string = PAD): string {
  return compact
    ? `${pad} h-7 py-0.5 leading-tight **:data-pill:align-middle`
    : `${pad} h-9 py-1 **:data-pill:align-middle`;
}
