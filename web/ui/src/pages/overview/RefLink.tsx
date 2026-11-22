import type { ReactNode } from 'react';
import { Link } from 'react-router';

import type { Ref } from '../../api';
import { focusRing } from '../../focus';

/**
 * A reference the server composed: a count that leads somewhere, an object that
 * opens.
 *
 * **Both print their text unlinked when there is nowhere honest to go.** The reason:
 * a gray anchor that 404s is worse than a plain word. `href` is
 * optional in the contract precisely so the server can say "no destination"
 * rather than making one up.
 *
 * **Only a path, never a URL.** A `Ref.href` that does not begin with `/` is
 * drawn as text. It closes a `javascript:` href as a rendering vector, and it
 * keeps a stray absolute URL from being handed to the client router, which
 * would navigate to a path that does not exist rather than leaving the console.
 *
 * It is a candidate for `state/` or for a shared components directory.
 */
export function RefLink({
  reference,
  ring = focusRing,
  fill = false,
}: {
  reference: Ref;
  /** One line as wide as the cell gives it, cut with an ellipsis at the end,
   *  and the whole text on hover - for a name that may be longer than its
   *  column, an EKS node's `ip-10-0-142-37.eu-west-3.compute.internal`. The
   *  end is what goes: a node name's distinguishing part is its first DNS
   *  label. The hover is the name because the screen cuts it; the server's
   *  hint ("Open <name>") stays the link's accessible name, so a screen reader
   *  hears what it says everywhere else. */
  fill?: boolean;
  /** Which of `focus.ts`'s rings. `focusRing` wherever the link has room
   *  outside itself; `focusRingInside` inside a `Pill`, whose `truncate` clips
   *  at a padding of one pixel top and bottom and cut the default ring off
   *  there. */
  ring?: string;
}): ReactNode {
  const href = reference.href;
  // `truncate` only cuts a box, so a filling reference is a block.
  const shape = fill ? 'block truncate' : 'truncate';
  const title = fill ? reference.text : reference.hint;

  if (href === undefined || !href.startsWith('/')) {
    return (
      <span title={title} className={shape}>
        {reference.text}
      </span>
    );
  }

  return (
    <Link
      to={href}
      title={title}
      // Only where the hover gave the hint up: elsewhere `title` is the hint,
      // and an `aria-label` equal to it would add nothing.
      aria-label={fill ? reference.hint : undefined}
      className={`${shape} rounded-xs text-accent underline-offset-2 hover:underline ${ring}`}
    >
      {reference.text}
    </Link>
  );
}
