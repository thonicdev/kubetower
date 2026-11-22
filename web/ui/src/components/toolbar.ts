/**
 * The one box of every control in a list's toolbar and of a resource panel's
 * header: the search field, Filter, Columns, the `⋯`, and the panel's
 * open and close buttons. A fixed height and one type size, so they measure
 * the same whatever they hold (a word, a glyph, an icon).
 *
 * The border color and the padding are NOT in the base: two utilities of the
 * same kind (`border-line` and `border-accent`, `px-2` and a square's width)
 * are settled by their order in the stylesheet, not in the markup, so each
 * control picks one and the base carries neither.
 */
export const toolbarBase =
  'inline-flex h-8 items-center justify-center gap-1 rounded-md border text-sm leading-none';

/** A control that holds a word or a field: neutral border, side padding. */
export const toolbarControl = `${toolbarBase} border-line px-2`;

/** A control that holds only a glyph or an icon: as wide as it is tall, no
 *  side padding. */
export const toolbarSquare = `${toolbarBase} border-line w-8`;
