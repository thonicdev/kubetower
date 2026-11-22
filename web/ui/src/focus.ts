/**
 * The focus ring, as five utility strings.
 *
 * **The width and the color never vary**: `2px solid` accent. The exceptions
 * are site decisions, not rings of their own: a color swatch, where an accent
 * ring on a chip whose whole subject is a color would be read as the color,
 * and the manifest editor, whose frame carries the ring for both of its halves
 * so the textarea does not draw a second one inside it.
 *
 * **The offset is the only thing that varies, and it varies for one reason:
 * whether the control has room outside itself.** `+2` where it does, `-1` or
 * `-2` where it is flush against a boundary and an outside ring would be clipped
 * by the scroll region or paint over the row beside it, `+1` on a field whose
 * own border is its edge, `+3` on a table heading's sort control, which has to
 * clear the cell padding to be seen at all. That is why the five names below are
 * about the control's surroundings and not about a size.
 *
 * **`:focus-visible`, never `:focus`.** The ring is for a keyboard and does not
 * appear on a click - a console whose every button flashes blue under the
 * pointer teaches the person to stop seeing the ring, which is the failure the
 * ring exists to avoid.
 *
 * ## Why a shared constant and not the string typed at every site
 *
 * A `@layer base` rule on `:focus-visible` in `theme.css` would be a component
 * rule in a file that holds tokens only (`eslint.config.js` and
 * `check-styles.mjs` hold the line). Repeating the utilities at each site is
 * sixty chances for the sixty-first control to get two of the three, or
 * `outline-2` with no color, and nothing fails when it does.
 *
 * So **it is a TypeScript constant holding utility class names.** No
 * stylesheet, no `@apply`, nothing `check-styles.mjs` has to be taught about -
 * the classes reach the element exactly as if they had been typed there, and
 * Tailwind's scanner reads them out of this file the same way it reads them out
 * of a `.tsx`.
 *
 * ## Exported from the package on purpose
 *
 * An extension compiles against `@kubetower/ui` and adds pages of its own. An
 * extra that invents its own ring gives the person two rings in one console,
 * and nobody here reviews it - the same argument `index.ts` makes for
 * `StateNotice` and for `DataGrid`.
 */

/** `2px solid var(--color-accent)`, on every one of them. */
const RING = 'focus-visible:outline-2 focus-visible:outline-accent';

/**
 * The default: **the control has room outside itself.** `outline-offset: 2px` -
 * a button on a panel, a link in a sentence, a tile in a dialog.
 */
export const focusRing = `${RING} focus-visible:outline-offset-2`;

/**
 * A field whose own border is its edge. `outline-offset: 1px` - text inputs and
 * textareas, and the retry button on a cluster that did not answer.
 *
 * One pixel and not two because the ring sits immediately outside a border that
 * is already drawn: at `+2` the gap reads as a second, paler border.
 */
export const focusRingTight = `${RING} focus-visible:outline-offset-1`;

/**
 * **Flush against a boundary**, so the ring is drawn inside the control.
 * `outline-offset: -2px` - the table row, the column menu's rows, the dock's
 * tabs and its fold.
 *
 * Not a preference. A row is the full width of a scrolling table, so a ring
 * outside it is clipped on both sides by the scroll region and paints over the
 * row above and below on the other two - which reads as the neighboring rows
 * being selected.
 */
export const focusRingFlush = `${RING} focus-visible:-outline-offset-2`;

/**
 * Drawn just inside a field that fills its container. `outline-offset: -1px` -
 * the filter box, the namespace finder, the manifest textarea.
 *
 * A site that wants the ring to replace its border rather than double it
 * says `focus-visible:border-transparent` beside this - it is a property of the
 * field, not of the ring.
 */
export const focusRingInside = `${RING} focus-visible:-outline-offset-1`;

/**
 * **Clears the cell padding.** `outline-offset: 3px` - the table heading's sort
 * control and the two chart surfaces.
 *
 * A sort control is an inline element inside a padded `<th>`; at `+2` its ring
 * is a rectangle around the word with the padding still outside it, and on a
 * heading beside another heading the two rings sit closer to each other than
 * either does to its own column.
 */
export const focusRingClear = `${RING} focus-visible:outline-offset-3`;

/**
 * **Room for the ring inside a scroll region**, for a region whose controls
 * reach its edge. Not a sixth ring: it goes on the region, never on a control.
 *
 * A box with `overflow` other than `visible` clips at its padding edge, and an
 * outline is drawn outside the control's border - so a field or a button laid
 * flush against a scrolling dialog body loses the part of its ring that falls
 * outside: a typed field loses a strip down its left side, and the buttons
 * under it their left and bottom edges.
 *
 * `p-1` is 4px, which is the widest ring these regions hold: `focusRing`, 2px
 * drawn 2px out. `-m-1` takes the same 4px back on every side, so nothing inside
 * moves and the region's outer edge is where its content's edge was.
 * `scroll-p-1` keeps the same 4px when Tab scrolls a control into view, which
 * otherwise lines its border up with the region's edge and clips the ring again.
 *
 * The alternative the rule above offers - draw the ring inside the control - is
 * the wrong one here: these are fields and buttons on a panel, which have room
 * outside themselves once the region stops cutting it off.
 */
export const focusRoom = '-m-1 p-1 scroll-p-1';
