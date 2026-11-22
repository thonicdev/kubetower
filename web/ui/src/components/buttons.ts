import { destructive } from '../destructive';
import { focusRingTight } from '../focus';

/**
 * One look per kind of button, as utility strings, with no stylesheet.
 *
 * Every button that holds a word takes `btn` plus one variant, or `btnToggle`
 * for one of a set that stays pressed. A toolbar's
 * controls keep `toolbar.ts`, and a close × is `CloseButton`.
 *
 * - **Height.** 24px at least, the smallest target WCAG 2.5.8 accepts, and
 *   44px under a coarse pointer (a phone or a tablet), where a finger is the
 *   pointer.
 * - **Disabled.** Dimmed with a not-allowed cursor. The site gives the reason,
 *   in a `title` or in text beside it: a control that refuses with no reason
 *   leaves the person guessing.
 * - **Radius.** `rounded-md`. A bare `rounded` draws no corner in this theme.
 */
export const btn =
  'inline-flex min-h-6 items-center justify-center gap-1 rounded-md border px-2 py-0.5 text-xs leading-none ' +
  'pointer-coarse:min-h-11 disabled:cursor-not-allowed disabled:opacity-50 ' +
  focusRingTight;

/** The usual button: Cancel, Close, Refresh, Hide, Copy. */
export const btnSecondary = `${btn} border-line text-muted hover:text-fg`;

/** The one action a dialog or a form exists for: Apply, Save, Sign in.
 *
 *  **Filled**, and that is what tells it from a pressed toggle (`btnToggle`):
 *  an accent border with no fill already means "this is the one you are on"
 *  across the console - the CPU tab, the active container, the current span -
 *  so a primary drawn the same way read as a choice already made rather than
 *  as the act the dialog is for. Placed first in a dialog's foot, before the
 *  way back. */
export const btnPrimary = `${btn} border-accent bg-accent text-fg hover:bg-accent/80`;

/** One button of a set where one, or several, are pressed: a measurement, a
 *  span, a container, a scheme. Pressed is the accent border with no fill, the
 *  console's mark for "the one you are on"; the fill is the primary's. The
 *  caller still says `aria-pressed`. */
export function btnToggle(on: boolean): string {
  return on ? `${btn} border-accent text-fg` : `${btn} border-line text-muted hover:text-fg`;
}

/** An act that cannot be taken back: Delete, End every session. */
export const btnDanger = `${btn} ${destructive}`;

/** A button set in text or in a row, with no box until it is hovered. */
export const btnGhost = `${btn} border-transparent text-muted hover:border-line hover:text-fg`;
