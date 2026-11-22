/**
 * The keyboard priority stack: the one place that says who gets a key first.
 *
 * ## The problem
 *
 * Several handlers claim Escape - dialogs, menus, the drawer, a full-size
 * chart. Left to themselves, their order would emerge from accidents: which
 * phase each listener chose, the order components mounted, a `z-index` read by
 * hand. That arrangement can dismiss the *covered* dialog and leave the visible
 * one open, and there is no way out of it from inside a handler:
 * `stopPropagation` does not stop other listeners registered on the same node,
 * so no claimant on `window` could silence another claimant on `window`.
 *
 * ## The answer
 *
 * One listener, on `window`, in the capture phase, installed once for the
 * application (`stack.ts`). Every claimant registers a *layer* from the table
 * below instead of registering a listener of its own. The stack walks its
 * claimants from the highest layer down and stops at the first one that says it
 * handled the key.
 *
 * Three properties follow, and they are the whole point:
 *
 *   - **The order is a number in this file**, not a phase, not a file order,
 *     not a `z-index` read out of the DOM. Changing it is an edit here.
 *   - **`stopPropagation` is not load-bearing.** There is one listener, so
 *     there is nothing on the same node to out-shout. The stack calls it once,
 *     on the way out, to keep the key away from the document - not to arbitrate.
 *   - **Ties break in favor of whatever opened last.** Two dialogs at the same
 *     layer is the covered-dialog case, and "most recently registered wins" is
 *     the only answer that is right without consulting anything visual.
 *
 * ## And what it is not
 *
 * It is not a delegated `document` listener. React attaches its own listeners
 * at the root container, so a hand-written `document.addEventListener` runs in
 * a different phase from every React handler and fires for elements React is
 * in the middle of unmounting. This listener never looks at `event.target` and
 * never matches a selector: it dispatches to components that asked for a key,
 * by layer. It is the one global listener in the application, and it is global
 * because "a key was pressed somewhere else on the page" has no owner element.
 */

/**
 * Higher claims first. The values are spaced so a layer can be inserted between
 * two of them without renumbering, and the names are what code reads - nothing
 * outside this file should write a bare number.
 *
 * | Layer      | Claimant                              | Yielded to             |
 * |------------|---------------------------------------|------------------------|
 * | `Overlay`  | the full-size chart                   | nothing                |
 * | `Confirm`  | a confirmation                        | the overlay            |
 * | `Dialog`   | the manifest dialog                   | the two above          |
 * | `Menu`     | the dock's scope menu, other menus    | anything above         |
 * | `Find`     | a find bar over a view                | the menus and above    |
 * | `Drawer`   | the detail drawer                     | all four above         |
 * | `Page`     | the page's own hotkeys                | everything above       |
 *
 * `Confirm` is above `Dialog`, so a confirmation raised from inside the
 * manifest editor takes Escape before the editor underneath it. The tie-break
 * would settle that today too - the confirmation renders after the editor - but
 * the order is a number here rather than the order two elements happen to be
 * written in. `stack.test.ts` asserts the outcome in **both** registration
 * orders, which is the only form of the test that can tell the two mechanisms
 * apart.
 */
export const KeyLayer = {
  /**
   * The page's own furniture: the kinds menu's jump box, and anything else
   * reachable by a hotkey that belongs to no overlay at all.
   *
   * **Below the drawer, which is the lowest of the overlays**, and that is the
   * whole reason it exists rather than sharing one. A hotkey on the page
   * must never answer a key while something is open over it - `/` with the
   * manifest dialog up is a slash in the editor, not a jump to the menu.
   * Numbered 5 so the spacing above is untouched.
   *
   * A claimant here has one obligation the five above do not: **it looks at
   * where the focus is.** `stack.ts` deliberately never reads `event.target`,
   * and for an overlay that is right - a dialog's Escape is a dialog's Escape
   * wherever the caret sits. A bare printable hotkey is the exception: `/` typed
   * into the namespace box is a slash, and a claimant that took it would have
   * made two text fields in this console unusable to find out.
   */
  Page: 5,
  /** The detail drawer. Deliberately last: it is the least modal thing on the
   *  page and the one an operator is least surprised to keep open. */
  Drawer: 10,
  /**
   * A find bar open over a view: the manifest's and the data block's
   * (`components/useFind.ts`).
   *
   * **Above the drawer, because the bar is inside it.** Escape is what closes
   * both, and one key must take off one thing: the bar the reader opened last,
   * then the drawer it sits in. At `Drawer` the tie-break would settle it the
   * same way - the bar registers after the drawer - but that would hold by
   * accident, so the order is a number here instead.
   *
   * **Below a menu**, because a menu opened while the bar is up is the newer
   * and the smaller thing, and the Escape that closes it must not also clear
   * the search the reader was in the middle of.
   *
   * Its Ctrl+F is not claimed here. Opening the bar is a page hotkey and claims
   * `Page`, below everything, so a dialog or a menu that is open keeps the key
   * away from a view underneath it.
   */
  Find: 15,
  /** Transient popovers: the dock's scope menu, the rail's menus, a column
   *  menu. Anything dismissed by clicking elsewhere. */
  Menu: 20,
  /** A modal dialog: the manifest editor, and anything else that owns the
   *  screen until it is answered. */
  Dialog: 40,
  /** A confirmation. **Above a dialog** - a confirmation raised from inside one
   *  must not close the dialog underneath it, which is the whole reason a
   *  confirmation is a layer of its own rather than a second `Dialog`.
   *
   *  45 and not a renumbering: the gaps in this table exist so a layer can be
   *  inserted between two of them, and this is that affordance being used.
   *  Still below `Overlay`, because a confirmation raised from inside a
   *  full-size chart is not a case this console has - if it ever does, that is
   *  an edit here and a test, not a `z-index`. */
  Confirm: 45,
  /** Something drawn over a dialog: a full-size chart, a lightbox.
   *
   *  The console's one claimant is `pages/object/Metrics.tsx`. */
  Overlay: 50,
} as const;

export type KeyLayer = (typeof KeyLayer)[keyof typeof KeyLayer];
