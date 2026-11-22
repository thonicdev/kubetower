/**
 * **The hover says which control this is**, as utility class names.
 *
 * One neutral treatment applied to everything (`text-muted hover:text-fg`)
 * says nothing about what a control does. Three arms say it: blue for what
 * watches, Claude's color for Claude, red for what destroys
 * (`destructive.ts`). This constant is the first: a frame in the accent color
 * and the text brought up to `fg`. The frame and the color are one hover in
 * two parts, and neither half is worth having without the other.
 *
 * ## Why a constant and not the pair typed at each site
 *
 * `focus.ts`'s argument, for a smaller number: Logs (`dock/LogsButton.tsx`),
 * and the pod and node shells (`pages/object/Terminals.tsx`). A rule that has
 * to be re-typed to reach a fourth control is a rule with a fourth control
 * missing from it.
 *
 * Exported from the package for the reason `destructive.ts` is: an extension
 * compiles against `@kubetower/ui` and adds pages. An extra that gives its own
 * log viewer a different hover gives the person two vocabularies for the same
 * act, and nobody here reviews it.
 *
 * ## What it deliberately does not carry
 *
 * **No geometry and no rest state.** The border itself (`border border-line`),
 * the padding, the radius and the ring belong to the site - Logs is a `py-0.5`
 * chip in the drawer's row, and an extra's might not be. What is here is the
 * hover and the disabled negation of the hover, which are one property.
 *
 * ## The collision with the focus ring, which is accepted rather than unnoticed
 *
 * `focus.ts`'s ring is `outline-2 outline-accent` and this frame is
 * `border-accent` - **the same token, so the same blue**, down to the value.
 * The two are told apart by outline versus border: **one pixel apart and both
 * visible at once**, the ring a 2px outline at `outline-offset: 1px`, outside;
 * this the border, inside - two concentric frames with a 1px gap. Neither may
 * be quietly dropped in favor of the other: the ring is the keyboard's only
 * signal and the border is the pointer's, and a reader who finds the pair
 * redundant is looking at two different inputs.
 */
export const watching =
  'hover:border-accent hover:text-fg disabled:hover:border-line disabled:hover:text-muted';
