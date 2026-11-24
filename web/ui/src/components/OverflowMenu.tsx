import {
  createContext,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';

import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { focusRing, focusRingInside } from '../focus';
import { Icon, type IconName } from './Icon';
import { MenuScrim } from './MenuScrim';

/**
 * A `⋯` and what it holds: the controls a toolbar keeps within reach without
 * keeping them on screen.
 *
 * ## Why a component, and why this one
 *
 * Every menu needs the same open-state, scrim and Escape, and a fix made in
 * one copy would land in whichever copy somebody remembered, so the shape is
 * written once here: a trigger, a panel on `MenuScrim`, and Escape claimed at
 * `KeyLayer.Menu` through `useEscape`. `MenuScrim` carries why the click
 * elsewhere is an element and not a `document` listener; nothing about that is
 * repeated here.
 *
 * ## The dot
 *
 * `TableMenu`'s, for the same reason: what is behind a `⋯` is out of sight,
 * and a hidden toggle that is on changes what the reader sees without anything
 * on screen saying why. `flagged` draws the dot, and `flaggedText` is the
 * sentence a screen reader gets in its place, as a description of the trigger
 * and never part of its name: a control whose name changes with its state is
 * one a reader has to learn twice, and the name is what every test finds it by.
 *
 * ## A real menu, so the keyboard is a menu's
 *
 * The items are `menuitem` and `menuitemcheckbox` inside `role="menu"`, which
 * is what lets a toggle say `aria-checked` rather than `aria-pressed`. A role
 * is a promise about the keys, so they are kept: opening puts the focus on the
 * first item, the arrows, Home and End move it, Escape closes and gives the
 * focus back to the `⋯`, and so does choosing an item. Tab leaves the menu and
 * closes it **from the `⋯`**: the focus is put back on the trigger first and
 * the key is not prevented, so the browser's own move starts there and lands
 * on the control after it (or, with Shift, before it). Closing alone unmounted
 * the focused item under the key, and the move would start again from the top
 * of the document - the rail's first link.
 *
 * ## A face other than the `⋯`
 *
 * The header's account menu is this menu under the signed-in name rather than
 * under a `⋯`: `face` is what the trigger draws in the glyph's place, and
 * `triggerClassName` the trigger's box in place of the bordered square, so a
 * trigger that reads as the header's other text controls is still this menu -
 * its keys, its scrim, its Escape and its focus going back - and not a second
 * copy of them. The accessible name is still `label`, so a face that draws a
 * name passes that name as `label` too: what is read and what is heard are one.
 *
 * ## Up or down
 *
 * `placement` is the caller's, because only the caller knows what is below
 * it. A toolbar at the top of the dock has the dock's bottom edge, and so the
 * window's, a pane's height away - at the dock's 120 px minimum, less than four
 * items - so the log pane opens it upward, over the page that is always above
 * the dock.
 */
export interface OverflowMenuProps {
  /** The trigger's accessible name. Stable whatever the state, see above. */
  label: string;
  /** The tooltip on the trigger. */
  title?: string;
  /** Whether something behind the `⋯` is on, which draws the dot. */
  flagged?: boolean;
  /** What the dot says to a screen reader, as a description of the trigger. */
  flaggedText?: string;
  /** Which way the panel opens from the trigger. */
  placement?: 'below' | 'above';
  /** What the trigger draws in place of the `⋯` (see above). */
  face?: ReactNode;
  /** The trigger's box in place of the bordered square; the focus ring is
   *  added either way. */
  triggerClassName?: string;
  /** The `menuitem`s: `OverflowMenuItem` and `OverflowMenuCheck`. */
  children: ReactNode;
}

/** How an item closes the menu it is in, and gives the focus back. */
const CloseMenu = createContext<() => void>(() => undefined);

export function OverflowMenu({
  label,
  title,
  flagged = false,
  flaggedText,
  placement = 'below',
  face,
  triggerClassName = 'rounded-sm border border-line px-1 text-muted hover:text-fg',
  children,
}: OverflowMenuProps): ReactNode {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const flaggedId = useId();
  const trigger = useRef<HTMLButtonElement>(null);

  const close = (): void => {
    setOpen(false);
    trigger.current?.focus();
  };

  return (
    <div className="relative">
      <button
        ref={trigger}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={label}
        aria-describedby={flagged && flaggedText !== undefined ? flaggedId : undefined}
        title={title}
        onClick={() => {
          setOpen((was) => !was);
        }}
        className={`relative ${triggerClassName} ${focusRing}`}
      >
        {/* U+22EF, a midline horizontal ellipsis. */}
        {face ?? <span aria-hidden="true">⋯</span>}
        {flagged ? (
          <>
            <span
              aria-hidden="true"
              data-testid="overflow-flag"
              className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-accent"
            />
            {flaggedText !== undefined && (
              <span id={flaggedId} className="sr-only">
                {flaggedText}
              </span>
            )}
          </>
        ) : null}
      </button>

      {open ? (
        <MenuPanel
          id={panelId}
          label={label}
          close={close}
          onDismiss={() => {
            setOpen(false);
          }}
          className={`absolute right-0 ${placement === 'above' ? 'bottom-full mb-1' : 'top-full mt-1'}`}
        >
          {children}
        </MenuPanel>
      ) : null}
    </div>
  );
}

/**
 * The open half of a menu: the scrim, the `role="menu"` panel, Escape at
 * `KeyLayer.Menu`, the focus on the first item as it appears, and the keys
 * described above. Mounted only while the menu is open.
 *
 * **Separate from the `⋯` so a menu with another way in is still this
 * menu**: a list row's context menu is opened by a
 * right-click, the keyboard's menu key or the row's own `⋯`, and is drawn at the
 * pointer rather than under a trigger, because a list's cells clip what they
 * hold. Its keys, its scrim and its Escape are these ones, not a third copy.
 *
 * `close` is the caller's because only the caller knows where the focus goes
 * back to; `onDismiss` is a click elsewhere, which leaves the focus where the
 * click put it. Placement is the caller's too, as `className` (an `absolute`
 * box under a trigger) or `style` (a `fixed` box at a point).
 */
export function MenuPanel({
  id,
  label,
  close,
  onDismiss,
  className = '',
  style,
  panelRef,
  children,
}: {
  id?: string;
  label: string;
  close: () => void;
  onDismiss: () => void;
  className?: string;
  style?: CSSProperties | undefined;
  /** The panel element, for a caller that places it by its size. */
  panelRef?: RefObject<HTMLDivElement | null>;
  children: ReactNode;
}): ReactNode {
  const own = useRef<HTMLDivElement>(null);
  const panel = panelRef ?? own;
  const focused = useRef(false);

  useEscape(KeyLayer.Menu, close, true);

  // The first item takes the focus as it appears, so the arrows have somewhere
  // to start from - once, and on whichever render first has an item: a row's
  // menu is drawn before the object's verbs have been read.
  useEffect(() => {
    if (focused.current) return;
    const first = items(panel.current)[0];
    if (first === undefined) return;
    first.focus();
    focused.current = true;
  });

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    if (event.key === 'Tab') {
      close();
      return;
    }
    const all = items(panel.current);
    if (all.length === 0) return;
    const at = all.indexOf(document.activeElement as HTMLElement);
    const to =
      event.key === 'ArrowDown' ? (at + 1) % all.length
        : event.key === 'ArrowUp' ? (at - 1 + all.length) % all.length
          : event.key === 'Home' ? 0
            : event.key === 'End' ? all.length - 1
              : null;
    if (to === null) return;
    event.preventDefault();
    all[to]?.focus();
  };

  return (
    <>
      <MenuScrim onDismiss={onDismiss} />
      <div
        ref={panel}
        id={id}
        role="menu"
        aria-label={label}
        onKeyDown={onKeyDown}
        // A right-click on the open menu, or the menu key on one of its items,
        // draws nothing of the browser's over it: the scrim answers a right-click outside the panel, this answers
        // one inside it, and between them an open menu is never covered by a
        // second one.
        onContextMenu={(event) => {
          event.preventDefault();
        }}
        style={style}
        className={`z-20 w-max min-w-40 rounded-lg border border-line bg-panel-2 p-1 shadow-lg ${className}`}
      >
        <CloseMenu.Provider value={close}>{children}</CloseMenu.Provider>
      </div>
    </>
  );
}

/** The panel's items, in order, for the arrow keys. */
function items(panel: HTMLElement | null): HTMLElement[] {
  if (panel === null) return [];
  return [...panel.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemcheckbox"]')];
}

// No color here: an item is `text-fg`, or `text-bad` where the server flagged
// its verb destructive, and two color utilities on one element are settled by
// the stylesheet's order rather than by which was meant.
const ITEM = `flex w-full items-center gap-2 rounded-sm px-1.5 py-1 text-left text-xs hover:bg-panel ${focusRingInside}`;

/** A toggle behind the `⋯`. The tick is the state drawn; `aria-checked` is the
 *  state said. Its box keeps its width when empty, so the labels stay in line. */
export function OverflowMenuCheck({
  checked,
  label,
  title,
  onChange,
}: {
  checked: boolean;
  label: string;
  title?: string;
  onChange: (checked: boolean) => void;
}): ReactNode {
  const close = useContext(CloseMenu);
  return (
    <button
      type="button"
      role="menuitemcheckbox"
      aria-checked={checked}
      tabIndex={-1}
      title={title}
      onClick={() => {
        onChange(!checked);
        close();
      }}
      className={`${ITEM} text-fg`}
    >
      <span aria-hidden="true" className="inline-block w-3 shrink-0">
        {checked ? '✓' : ''}
      </span>
      <span>{label}</span>
    </button>
  );
}

/**
 * An act behind the `⋯`.
 *
 * `destructive` and `disabled` are the server's answers wherever the act is a
 * verb (a list row's menu): red from the flag and never
 * from the word, as `destructive.ts` says, and a verb the server refused drawn
 * and refused rather than hidden, its reason as the title - `Verbs.tsx`'s rule.
 * A refused item keeps its place in the arrows' walk, `aria-disabled` and not
 * `disabled`, so a reader moving through the menu meets it and hears why it is
 * there.
 *
 * ## A glyph before the label
 *
 * `icon` draws one of the sheet's glyphs in the leading column every item keeps
 * - the column a toggle's tick is drawn in - at the console's one icon size,
 * `aria-hidden` like every glyph here: the label stays the whole of the item's
 * name. Optional: the log pane's and a list row's items pass none; the account
 * menu's Settings and Log out carry one. In a menu where every item has one the labels line up on the
 * glyphs; a menu mixing the two would not, which is why none does.
 */
export function OverflowMenuItem({
  label,
  title,
  icon,
  destructive = false,
  disabled = false,
  badge,
  onSelect,
}: {
  label: string;
  /** A count drawn after the label: Settings' "1" when an update is ready to install. */
  badge?: string | undefined;
  title?: string | undefined;
  /** A glyph drawn before the label, in the column the tick takes. */
  icon?: IconName | undefined;
  destructive?: boolean;
  disabled?: boolean;
  onSelect: () => void;
}): ReactNode {
  const close = useContext(CloseMenu);
  return (
    <button
      type="button"
      role="menuitem"
      tabIndex={-1}
      title={title}
      aria-disabled={disabled ? true : undefined}
      data-destructive={destructive ? '' : undefined}
      onClick={() => {
        if (disabled) return;
        onSelect();
        close();
      }}
      className={`${ITEM} ${destructive ? 'text-bad' : 'text-fg'}${disabled ? ' cursor-not-allowed opacity-50' : ''}`}
    >
      {icon === undefined ? <span aria-hidden="true" className="inline-block w-3 shrink-0" /> : <Icon name={icon} />}
      <span>{label}</span>
      {badge === undefined ? null : <CountBadge title={title}>{badge}</CountBadge>}
    </button>
  );
}

/**
 * A line between two sets of items. A list row's menu draws one between its
 * safe entries and its verbs, so the hand reaching for Open
 * does not land on a verb. `role="separator"`, which the arrows step over: they
 * walk the `menuitem`s alone.
 */
export function OverflowMenuSeparator(): ReactNode {
  return <div role="separator" className="my-1 border-t border-line" />;
}

/**
 * A small accent count, after a label: the Settings entry's "1". Without a
 * `label` it is hidden from the accessibility tree, so the link or item it sits
 * in keeps its own name ("Settings", not "Settings1") and that control's
 * tooltip says why. With one, it is announced by that label - where nothing
 * else on the control says it, such as the account menu's trigger.
 */
export function CountBadge({
  children,
  title,
  label,
}: {
  children: ReactNode;
  title?: string | undefined;
  label?: string | undefined;
}): ReactNode {
  return (
    <span
      data-count-badge=""
      {...(label === undefined ? { 'aria-hidden': true } : { role: 'img', 'aria-label': label })}
      title={title}
      className="ml-1 inline-flex min-w-4 items-center justify-center rounded-full bg-accent px-1 text-2xs leading-4 font-semibold text-fg"
    >
      {children}
    </span>
  );
}
