import { useId, useState, type ReactNode } from 'react';

import { btnGhost } from './buttons';
import { Icon } from './Icon';

/**
 * A detail that would otherwise be a hover only, within reach of a keyboard
 * and a touch screen: an `info` glyph that shows the text on a press (Enter,
 * Space, a tap) and hides it on a second press, on Escape, or when the focus
 * leaves. The same text stays its hover.
 */
export function DetailTip({ text, label }: { text: string; label: string }): ReactNode {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <span className="relative inline-flex shrink-0" data-detail-tip="">
      <button
        type="button"
        aria-label={label}
        aria-expanded={open}
        aria-controls={id}
        title={text}
        onClick={() => setOpen((was) => !was)}
        onBlur={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && open) {
            event.stopPropagation();
            setOpen(false);
          }
        }}
        className={`${btnGhost} px-0.5`}
      >
        <Icon name="info" />
      </button>
      {open ? (
        <span
          id={id}
          role="tooltip"
          data-detail-tip-text=""
          className="absolute top-full left-0 z-30 mt-1 w-max max-w-64 rounded-md border border-line bg-panel px-2 py-1 text-xs whitespace-pre-line text-fg shadow"
        >
          {text}
        </span>
      ) : null}
    </span>
  );
}
