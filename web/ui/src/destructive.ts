/**
 * **One destructive style for the whole console**, as utility class names.
 *
 * ## Why one
 *
 * The plan dialog's commit control - the one control in the console that must
 * not be pressed by reflex - the drawer's verbs and the selection bar's verbs
 * all mean the same thing, and three treatments for one meaning teaches the
 * hand nothing. This is the one, exported for the same reason `focus.ts` is: an
 * extension compiles against `@kubetower/ui`, and an extra that invents its own
 * red gives the person two.
 *
 * ## What it is, and why each part
 *
 * `border-bad/45 bg-bad/10 text-bad`, hovering to `bg-bad/20`: a red *tint*
 * behind red text, not a filled red button. Every verb a Pod offers - Restart,
 * Evict, Delete - is destructive, so a filled red would draw the drawer's whole
 * verb row as three solid chips, and a warning that covers everything warns
 * about nothing. The tint separates them from Cancel without shouting three
 * times. The opacities are Tailwind's own steps rather than arbitrary values.
 *
 * **The hover takes the frame to the full `--bad`.** On a control that is
 * already red at rest the only honest hover is **intensify**: the frame from
 * 45% to full, the tint from a tenth to a fifth.
 *
 * Deliberately not a stronger fill. `bg-bad` flat on hover would make a control
 * whose rest state is already colored read as *already pressed* rather than as
 * hovered. Nothing in this console fills on hover; what things do here is light
 * or frame.
 *
 * It is on this constant and not at the call sites because `destructiveFor` is
 * the single producer of the flag, and a hover typed beside `destructive` in
 * `Verbs.tsx` and in `SelectionBar.tsx` would be a pair of hand-kept hovers.
 *
 * `disabled:opacity-50` is the gesture not yet satisfied. Half-visible rather
 * than gray, because a destructive control that stops looking destructive while
 * it is disabled is one whose color the eye learns to ignore.
 *
 * ## What it deliberately does not carry
 *
 * **No geometry.** Padding, radius, text size and the focus ring belong to the
 * site: the dialog's commit control is a `py-1` button in a footer, the bar's
 * verbs are `py-0.5` chips, and the drawer's carry an icon. A constant that
 * carried those would soon be four constants.
 */
export const destructive =
  'border-bad/45 bg-bad/10 text-bad hover:border-bad hover:bg-bad/20 disabled:opacity-50';
