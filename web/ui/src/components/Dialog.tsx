import { useEffect, useId, useRef, type ReactNode, type RefObject } from 'react';

import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { CloseButton } from './CloseButton';

/**
 * A modal dialog, and the three gestures no attribute can express.
 *
 * Without these a dialog still opens, still closes, and still passes every
 * test; it just cannot be used without a mouse and does not give the keyboard
 * back.
 *
 * ## The three, and the fourth that is deliberately absent
 *
 * **Escape**, claimed at a layer from the one keyboard stack. Not a `window`
 * listener: five of those, ordered by capture phase and a `z-index` two scripts
 * read by hand, are what the stack replaced (`keyboard/layers.ts`). A dialog
 * raised from inside another dialog passes `layer` and gets the key first,
 * which is the arrangement that stopped a keystroke dismissing the *covered*
 * dialog and leaving the visible one on screen.
 *
 * **A click on the veil**, and not on the box - *"a modal that closes when you
 * miss a button by four pixels is a modal people stop reading."* The press and
 * the release must both be on the veil: a drag that began inside the box
 * reports the common ancestor as its target, and the veil *is* the ancestor,
 * so without the mousedown guard,
 * selecting text in the dialog and releasing beyond its edge would close it.
 *
 * **Focus in, and back out.** Into the thing that has to be typed if there is
 * one - `data-dialog-focus` names it - otherwise the first field, otherwise the
 * dialog. And back to whatever had the keyboard when it opened, *"so dismissing
 * a confirmation returns the operator to the icon they clicked rather than to
 * the top of the document"*. Only if that element is still in the document: the
 * row it sat in may have been refreshed away underneath. Where it is gone - a
 * row deleted by the dialog's own act, a selection bar that closed with its
 * selection - the keyboard goes to the row now at that place in the same list,
 * else to the first row of the page's list (`returnTarget`), rather than to
 * `<body>`.
 *
 * **No focus trap, and it is a decision rather than an omission:** *a trap that
 * survives a failed request is a browser tab the operator has to close.* Tab
 * can leave. Escape and the veil are both ways out that work whether the request
 * behind the dialog succeeded, failed, or never answered.
 *
 * **One exception, and why it is safe:** the rail's nameplate editor
 * (`shell/ClusterEditor.tsx`) keeps Tab inside, because it
 * sits on a scrim that takes every click and the page behind it cannot be used.
 * It is not the trap this note refuses: Escape (from the keyboard stack) and a
 * click on the scrim always close it, whatever its request did - a failed or a
 * hung save included - and its buttons never disable themselves under the
 * keyboard, so focus cannot fall out to the page while a save is pending.
 *
 * ## What it is not
 *
 * `<dialog showModal>` is not used. It brings a focus trap and the browser's own
 * top-layer Escape handling, which is a sixth undeclared claimant of the key -
 * exactly what the keyboard stack exists to prevent, arriving from the platform
 * instead of from a script.
 */
export interface DialogProps {
  /** What it is called. Rendered as the heading and wired to `aria-labelledby`,
   *  because a dialog with no accessible name is announced as "dialog". */
  title: string;
  onClose: () => void;
  children: ReactNode;
  /** Where it sits in the keyboard stack. **Name the layer this thing *is*,
   *  not the one it currently gets away with**: `KeyLayer.Confirm` for a
   *  confirmation, `KeyLayer.Overlay` for something drawn over a dialog - a
   *  full-size chart, a lightbox.
   *
   *  Not "only when raised from inside another dialog": a confirmation not
   *  raised from inside a dialog *yet* is held up on a shared layer only by
   *  the tie-break - "whatever opened last" - and that stops holding on the
   *  refactor that opens it from somewhere else.
   *
   *  Omitting it is the `Dialog` layer and is allowed, for a dialog. The
   *  omission is not silent: `keyboard/dialogLayers.test.ts` lists every call
   *  site that takes the default, so a new one fails until somebody writes down
   *  why it is the right one. */
  layer?: KeyLayer | number;
  /** The label on the close control. */
  closeLabel?: string;
  /** Whether the header draws its close control. Escape and a click on the veil
   *  still close it: a dialog that has its own way back (the apply
   *  confirmation's *Back*) says it once rather than twice. Default true. */
  showClose?: boolean;
  /** Whether the dialog takes Escape. False while something drawn inside it
   *  holds a lower layer that has to be asked first - the enlarged manifest's
   *  find bar (`KeyLayer.Find`) is under `Dialog` in the stack, so the dialog
   *  has to step aside for one Escape. Default true. */
  takesEscape?: boolean;
  /** How much room it takes. `default` is a confirmation's width; `viewport`
   *  is most of the window, for a document rather than a question - the metrics
   *  panel's full-size chart, and the manifest editor, which is 80 to 120
   *  columns of YAML and was unreadable at the drawer's width. The two widths
   *  are here rather than passed as a class so a third size is a decision and
   *  not a string - and `wide` is that decision, taken once: the rail's
   *  Arrange, one step above `default`.
   *
   *  It is a **width**. How tall a viewport dialog is comes from its own
   *  content - `Metrics.tsx` asks for `h-[60vh]`, `ManifestEditor.tsx` for
   *  `h-[75vh]` - because the two have different reasons for their height and
   *  neither reason belongs to the dialog. */
  size?: 'default' | 'wide' | 'viewport' | 'sheet';
  /** `danger` frames the box in red - a tinted border - so an act that removes
   *  something is recognized while the pointer is still moving, before the
   *  button is read. No bar down the left edge, on every danger dialog alike.
   *  */
  tone?: 'default' | 'danger';
}

export function Dialog({
  title,
  onClose,
  children,
  layer = KeyLayer.Dialog,
  closeLabel = 'Close',
  showClose = true,
  takesEscape = true,
  size = 'default',
  tone = 'default',
}: DialogProps): ReactNode {
  const titleId = useId();
  const box = useRef<HTMLDivElement>(null);
  // Whether the press that may become a click started on the veil. Never
  // rendered, so a ref and not state.
  const pressedVeil = useRef(false);

  useEscape(layer, onClose, takesEscape);

  useFocusHandover(box);

  return (
    <div
      // The veil. `z-40` keeps it over the page and under nothing in particular:
      // which dialog owns Escape is the `layer` above, so this number is only
      // about paint.
      className={`fixed inset-0 z-40 flex items-center justify-center bg-bg/80 p-4 ${VEIL[size]}`}
      onMouseDown={(event) => {
        pressedVeil.current = event.target === event.currentTarget;
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        if (!pressedVeil.current) return;
        onClose();
      }}
    >
      <div
        ref={box}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-tone={tone}
        className={`max-h-full w-full overflow-auto rounded-xl border bg-panel p-4 text-sm ${SIZE[size]} ${TONE[tone]}`}
      >
        <div className="flex items-center justify-between gap-2">
          <h2 id={titleId} className="text-lg font-medium">
            {title}
          </h2>
          {showClose && <CloseButton onClick={onClose} label={closeLabel} escape={takesEscape} />}
        </div>

        <div className="mt-3">{children}</div>
      </div>
    </div>
  );
}

/** Full class strings, looked up - never built by interpolation, because
 *  Tailwind finds utilities by scanning source text. */
const SIZE: Readonly<Record<NonNullable<DialogProps['size']>, string>> = {
  default: 'max-w-2xl',
  // Arrange the rail: a row per cluster carrying the tile, two fields, the
  // sixteen swatches and five controls. One step up the scale from default.
  wide: 'max-w-3xl',
  // Roughly four fifths of the window across: enough that a week of samples is
  // readable and that a manifest's longest line fits without wrapping, with a
  // margin left that still reads as a veil to click on.
  // Under 640px it is the whole screen: a window with a margin on a phone is
  // a margin and nothing to read in.
  viewport: 'max-w-[80vw] max-sm:h-full max-sm:max-w-none max-sm:rounded-none max-sm:border-0',
  // A settings form: a box under 640px of window, and the whole screen below it.
  sheet: 'max-w-lg max-sm:h-full max-sm:max-w-none max-sm:rounded-none max-sm:border-0',
};

/** The veil's padding, taken away where the box is the whole screen. */
const VEIL: Readonly<Record<NonNullable<DialogProps['size']>, string>> = {
  default: '',
  wide: '',
  viewport: 'max-sm:p-0',
  sheet: 'max-sm:p-0',
};

const TONE: Readonly<Record<NonNullable<DialogProps['tone']>, string>> = {
  default: 'border-line',
  danger: 'border-bad/45',
};

/**
 * Take the keyboard on open and give it back on close.
 *
 * The opener is read in the effect rather than during render: a render may be
 * discarded, and `document.activeElement` read from a discarded one is a
 * question asked at a moment that never happened.
 *
 * The focus order is: the typed confirmation, then a number field, then the
 * commit button, then the way out - "the thing the operator came here to do,
 * and the way out if there is nothing to do". `data-dialog-focus` is the first
 * of those made explicit rather than inferred from a name the server happened
 * to choose.
 */
function useFocusHandover(box: RefObject<HTMLDivElement | null>): void {
  const openerRef = useRef<Element | null>(null);

  useEffect(() => {
    openerRef.current = document.activeElement;

    const root = box.current;
    if (root !== null) {
      const first =
        root.querySelector<HTMLElement>('[data-dialog-focus]') ??
        root.querySelector<HTMLElement>(
          'input:not([type="hidden"]):not([disabled]), textarea:not([disabled]), select:not([disabled])',
        ) ??
        root.querySelector<HTMLElement>('button[type="submit"]:not([disabled])') ??
        root.querySelector<HTMLElement>('button:not([disabled])');
      // `preventScroll`, because focusing the first field of a tall dialog
      // otherwise scrolls its own heading off the top of itself.
      first?.focus({ preventScroll: true });
    }

    const opener = openerRef.current;
    const place = rowPlaceOf(opener);
    return () => {
      // Still in the document: the control that opened this may have been
      // refreshed away while the dialog was up, and focusing a detached node
      // silently moves the keyboard to <body>.
      giveBack(opener, place);
    };
  }, [box]);
}

/** The list row an opener sat in: its body and its index there. */
interface RowPlace {
  body: Element;
  index: number;
}

function rowPlaceOf(opener: Element | null): RowPlace | undefined {
  const row = opener?.closest('tbody > tr');
  const body = row?.parentElement;
  if (row === null || row === undefined || body === null || body === undefined) return undefined;
  return { body, index: Array.prototype.indexOf.call(body.children, row) };
}

/**
 * Where the keyboard goes when a dialog closes, the first of these that takes
 * it: the control that opened it, while it is still in the document; the row
 * now at the opener's place in the same list, or the list's last row where the
 * list got shorter; the first row of the list on the page. "Takes it" is read
 * back from `document.activeElement`: a control still in the document can sit
 * in a hidden panel, where focusing it silently leaves the keyboard on
 * `<body>`.
 */
function giveBack(opener: Element | null, place: RowPlace | undefined): void {
  const candidates: (() => HTMLElement | null)[] = [
    () => (opener instanceof HTMLElement && document.contains(opener) ? opener : null),
    () => {
      if (place === undefined || !document.contains(place.body)) return null;
      const rows = place.body.children;
      const row = rows[Math.min(place.index, rows.length - 1)];
      return row === undefined ? null : focusableIn(row);
    },
    () => {
      const first = document.querySelector('#page tbody > tr');
      return first === null ? null : focusableIn(first);
    },
  ];
  for (const candidate of candidates) {
    const target = candidate();
    if (target === null) continue;
    target.focus({ preventScroll: true });
    if (document.activeElement === target) return;
  }
}

/** A row itself where it is a tabstop, else the first control inside it. */
function focusableIn(row: Element): HTMLElement | null {
  if (row instanceof HTMLElement && row.tabIndex >= 0) return row;
  return row.querySelector<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), [tabindex="0"]');
}
