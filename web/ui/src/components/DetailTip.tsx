import { useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react';

import { focusRingTight } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { Icon } from './Icon';

/** The gap kept between the panel and the window's edges, and its trigger. */
const ROOM = 8;
const GAP = 4;

/** Below the trigger where it fits, above it where it does not, and never past
 *  an edge of the window: a scrolling ancestor would cut a panel placed inside
 *  it, so the panel is `fixed` and placed against the window. */
export function placeTip(
  trigger: { left: number; top: number; bottom: number },
  panel: { width: number; height: number },
  window: { width: number; height: number },
): { left: number; top: number } {
  const left = Math.min(Math.max(ROOM, trigger.left), Math.max(ROOM, window.width - panel.width - ROOM));
  const below = trigger.bottom + GAP;
  const above = trigger.top - GAP - panel.height;
  const top = below + panel.height + ROOM > window.height && above >= ROOM ? above : below;
  return { left, top };
}

/**
 * A detail that would otherwise be a hover only, within reach of a keyboard
 * and a touch screen: an `info` glyph that shows the text on a press (Enter,
 * Space, a tap) and hides it on a second press, on Escape, or when the focus
 * leaves. The text is the panel's alone, never also a native hover, so it is
 * never shown twice.
 *
 * Escape is claimed at `KeyLayer.Menu` while the panel is open, as a menu's
 * is: the window's one listener runs before a React handler, and the drawer
 * the tip sits in would otherwise take the key and close.
 *
 * The trigger is the copy button's size, so a line holding one is as tall as a
 * line without it.
 */
export function DetailTip({ text, label }: { text: string; label: string }): ReactNode {
  const [open, setOpen] = useState(false);
  const [at, setAt] = useState<{ left: number; top: number } | null>(null);
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLSpanElement>(null);

  useEscape(KeyLayer.Menu, () => setOpen(false), open);

  useLayoutEffect(() => {
    if (!open) return undefined;
    const place = (): void => {
      const from = trigger.current?.getBoundingClientRect();
      const box = panel.current?.getBoundingClientRect();
      if (from === undefined || box === undefined) return;
      const next = placeTip(from, box, { width: window.innerWidth, height: window.innerHeight });
      setAt((was) => (was?.left === next.left && was.top === next.top ? was : next));
    };
    place();
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    return () => {
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
    };
  }, [open, text]);

  return (
    <span className="inline-flex shrink-0 align-middle" data-detail-tip="">
      <button
        ref={trigger}
        type="button"
        aria-label={label}
        aria-expanded={open}
        aria-controls={id}
        aria-describedby={open ? id : undefined}
        onClick={() => setOpen((was) => !was)}
        onBlur={() => setOpen(false)}
        className={`relative rounded-sm border border-transparent px-0.5 text-xs leading-none text-muted after:absolute after:-inset-1 hover:text-fg ${focusRingTight}`}
      >
        <Icon name="info" />
      </button>
      {open ? (
        <span
          ref={panel}
          id={id}
          role="tooltip"
          data-detail-tip-text=""
          style={at === null ? { left: 0, top: 0, visibility: 'hidden' } : { left: at.left, top: at.top }}
          className="fixed z-30 w-max max-w-64 rounded-md border border-line bg-panel px-2 py-1 text-xs whitespace-pre-line [overflow-wrap:anywhere] text-fg shadow"
        >
          {text}
        </span>
      ) : null}
    </span>
  );
}
