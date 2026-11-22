import type { ReactNode } from 'react';

/**
 * The element a click lands on when it is not on the open menu.
 *
 * ## Why an element and not a `document` listener
 *
 * React attaches its own listeners at the root container, so a hand-written
 * `document.addEventListener` runs in a different phase from every React
 * handler and fires for elements React is mid-unmount: a `pointerdown` on
 * `document` closes the menu, then the button's own `click` reopens it, and
 * pressing the button a second time does nothing an operator can see. A scrim
 * in the tree has no second phase to disagree with.
 *
 * ## Why one component
 *
 * Every menu needs the same lines, and a fix made in one copy would land in
 * whichever copy somebody remembered. This is the pointer half of a menu only; Escape stays with each menu's own
 * `useEscape`, because that is a claim on the keyboard stack made by whoever
 * owns the open state, and not a property of the surface a click lands on.
 *
 * ## Out of the tab order and out of the accessibility tree
 *
 * As a plain `<button>` this would be the first tab stop after the trigger - a
 * keyboard operator opening Columns and pressing Tab would focus an invisible
 * element covering the viewport before reaching the first checkbox, and a
 * reader would announce it as "Close the column menu". `tabIndex={-1}` takes it
 * out of the sequence and `aria-hidden` out of the tree; Escape already closes
 * every menu through `KeyLayer.Menu`, so the keyboard loses nothing. It stays a
 * `<button>` because it acts on a click, and an element that acts on a click
 * and says so to nobody is exactly what this is.
 *
 * `z-10` under a panel at `z-20`: the panel it belongs to must sit above it, or
 * the first click on a menu item closes the menu instead.
 *
 * ## A right-click elsewhere closes it too
 *
 * And draws nothing of the browser's. With a list row's
 * context menu open, a right-click on the next row lands here, not on the row:
 * without this the browser would draw its own menu over a console menu that stays
 * open behind it - two menus, neither of them the one asked for. A right-click
 * outside an open menu closes it, which is what a desktop's menus do, and the
 * next right-click is the row's. Every menu on this scrim gets the same.
 */
export function MenuScrim({ onDismiss }: { onDismiss: () => void }): ReactNode {
  return (
    <button
      type="button"
      tabIndex={-1}
      aria-hidden="true"
      onClick={onDismiss}
      onContextMenu={(event) => {
        event.preventDefault();
        onDismiss();
      }}
      className="fixed inset-0 z-10 cursor-default"
    />
  );
}
