import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { focusRing, focusRingTight } from '../focus';
import { Icon } from './Icon';
import { toolbarBase } from './toolbar';

export interface CloseButtonProps {
  /** What closing does. Optional only where `to` is given instead. */
  onClick?: () => void;
  /** Where closing leads, for a × that leaves a page for another: drawn as a
   *  link to it, so it opens in a new tab like any other link, with the
   *  same box, glyph and name. */
  to?: string;
  /** The accessible name and the tooltip. "Close" when the context names what
   *  closes; otherwise say what, "Close the search". */
  label?: string;
  /** `toolbar`: the 32px square of a panel's or a dialog's header, top right.
   *  `inline`: 24px (44px under a coarse pointer), for a tab, a chip or a
   *  banner. */
  size?: 'toolbar' | 'inline';
  /** `bad` when closing ends something that cannot be resumed (a session). */
  tone?: 'neutral' | 'bad';
  /** Whether Escape closes too; it is named in the tooltip. The key itself is
   *  the site's (`useEscape`). */
  escape?: boolean;
  /** A close that asks twice, after the first press: the red box says the next
   *  one acts. The dock's × is the one. */
  armed?: boolean;
  className?: string;
}

/**
 * The close × of the whole console: one glyph (the `close` icon), one size per
 * place, the name on the control and the glyph hidden from screen readers.
 */
export function CloseButton({
  onClick,
  to,
  label = 'Close',
  size = 'toolbar',
  tone = 'neutral',
  escape = false,
  armed = false,
  className = '',
}: CloseButtonProps): ReactNode {
  const colour = tone === 'bad' ? 'text-muted hover:text-bad' : 'text-muted hover:text-fg';
  // `toolbarSquare`'s box, with the border's color chosen here: two border
  // colors on one element would be settled by stylesheet order, not by intent.
  const edge = armed ? 'border-bad bg-bad/15' : 'border-line';
  const box =
    size === 'toolbar'
      ? `${toolbarBase} w-8 ${edge} pointer-coarse:size-11 ${focusRing}`
      : `inline-flex size-6 shrink-0 items-center justify-center rounded-sm pointer-coarse:size-11 ${focusRingTight}${
          armed ? ` border ${edge}` : ''
        }`;
  const title = escape ? `${label} (Escape)` : label;
  if (to !== undefined) {
    return (
      <Link to={to} aria-label={label} title={title} onClick={onClick} className={`${box} ${colour} ${className}`}>
        <Icon name="close" className="pointer-events-none" />
      </Link>
    );
  }
  return (
    <button type="button" aria-label={label} title={title} onClick={onClick} className={`${box} ${colour} ${className}`}>
      <Icon name="close" className="pointer-events-none" />
    </button>
  );
}
