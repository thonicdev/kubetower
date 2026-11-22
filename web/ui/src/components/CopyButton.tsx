import { useEffect, useRef, useState, type ReactNode, type RefObject } from 'react';

import { focusRing } from '../focus';

/**
 * Put a value on the clipboard, and say which of three things happened.
 *
 * ## Why this is a control at all
 *
 * **Shown is not the same as obtainable.** A twenty-character password
 * displayed perfectly in a message box with no selectable text is not
 * obtainable. An address inside a `whitespace-nowrap` cell in a row that
 * opens a drawer when it is clicked is the same problem: the value is on screen
 * and a drag to select it opens the drawer instead.
 *
 * ## Reporting, not claiming, and the three steps are not decoration
 *
 * Every path ends by telling the operator what happened, and none of them is
 * silence:
 *
 * 1. **The Clipboard API wrote it.** A tick.
 * 2. **It refused**, so a hidden textarea and `document.execCommand('copy')` -
 *    deprecated, still implemented everywhere, and works outside a secure
 *    context. This step exists because **127.0.0.1 is a secure context by the
 *    specification and a LAN address over plain http is not**, which is how this
 *    console is most often reached.
 * 3. **Both refused**, so the shown text is **selected in place** and the button
 *    says so. A selection the operator can press Ctrl+C on is obtainable, where
 *    a button that quietly did nothing is the defect this component exists to
 *    avoid.
 *
 * Step 3 needs something to select, which is `selectRef` - the element holding
 * the text as drawn. Without one the component still reports the refusal and
 * says the value could not be put anywhere, which is worse for the operator and
 * still honest. It selects what is **shown**, which on a capped column may be
 * less than the button would have copied, and it says so rather than pretending
 * otherwise: the whole value is in a prop, and selecting a prop is not a thing a
 * browser can do.
 *
 * ## What it does not use
 *
 * No delegated `document` listener: React re-renders the same element and
 * attaches at the root container, so a button with its own `onClick` is
 * correct here and a `document` listener would fire for buttons React is
 * mid-unmount.
 *
 * `stopPropagation` **is** used, and for a reason rather than out of habit: the
 * row opens the drawer on a click, and the selection made
 * by step 3 must not be cleared by something else acting on the same click.
 */

/** How long the button stays in its answered state: long enough to
 *  read at a glance, short enough that a second copy of the row beside it is not
 *  confusing. */
const SETTLE_MS = 1400;

type Outcome = 'copied' | 'selected' | 'refused';

const SAID: Readonly<Record<Outcome, string>> = {
  copied: 'Copied.',
  selected: 'This browser would not let the page write to the clipboard. The text is selected — press Ctrl+C.',
  refused: 'This browser would not let the page write to the clipboard.',
};

export interface CopyButtonProps {
  /** The whole value, which may be longer than what is drawn. */
  value: string;
  /** What the button is called before it has been pressed. */
  label: string;
  /** The element holding the text as shown, for the last resort. */
  selectRef?: RefObject<HTMLElement | null>;
  /** Which of `focus.ts`'s rings. `focusRing` by default; `focusRingInside`
   *  in a list's cell, which clips what it holds and, at the compact density,
   *  leaves 1.5px above the button where the default ring reaches 4px out. */
  ring?: string;
}

export function CopyButton({ value, label, selectRef, ring = focusRing }: CopyButtonProps): ReactNode {
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // The timer outlives nothing: a row refreshed away mid-settle would otherwise
  // leave a callback holding a component React has unmounted; the state is
  // ours and has to be cleaned up.
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );

  function settle(next: Outcome): void {
    setOutcome(next);
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      setOutcome(null);
    }, SETTLE_MS);
  }

  function copy(): void {
    copyText(value, (written) => {
      if (written) {
        settle('copied');
        return;
      }
      settle(selectInPlace(selectRef?.current ?? null) ? 'selected' : 'refused');
    });
  }

  const said = outcome === null ? label : SAID[outcome];

  return (
    <button
      type="button"
      title={said}
      aria-label={said}
      data-copy-state={outcome ?? 'idle'}
      // The same rule as `stopPropagation` below, said the way the row reads it:
      // `DataGrid` declines to open a row when the click began inside a
      // `data-no-open` control. Both, because they fail differently - the
      // attribute survives a caller that renders this inside something which
      // re-dispatches, and the stopPropagation survives a row that checks
      // nothing.
      data-no-open=""
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        copy();
      }}
      // Drawn at full contrast with a border, so an opacity-on-focus treatment
      // would be invisible; the ordinary ring carries that information.
      // The `after:` box widens what the pointer hits to 24px and more without
      // making a table row taller.
      className={`relative rounded-sm border border-line px-1 text-xs after:absolute after:-inset-1 ${ring} ${
        outcome === 'copied' ? 'text-ok' : outcome === null ? 'text-muted' : 'text-warn'
      }`}
    >
      {/* The mark is a state of one control rather than an icon of its own, so
          it is drawn here and not added to anything shared. */}
      <span aria-hidden="true">{outcome === 'copied' ? '✓' : '⧉'}</span>
      {/* What happened, for a screen reader, without waiting for the button to
          be focused again. `aria-label` is only read when the control is next
          visited; the live region says it now. */}
      <span role="status" aria-live="polite" className="sr-only">
        {outcome === null ? '' : SAID[outcome]}
      </span>
    </button>
  );
}

/**
 * The fallback write: a textarea off screen, selected, copied.
 *
 * Off screen and **not** `hidden`. A hidden element has no selection, so the
 * copy silently copies nothing - which is the exact failure mode this whole
 * component is written against. `readOnly` stops a mobile keyboard appearing and
 * the fixed position stops the page scrolling to it.
 */
function execCopy(value: string): boolean {
  const area = document.createElement('textarea');
  area.value = value;
  area.readOnly = true;
  area.style.position = 'fixed';
  area.style.top = '-1000px';
  area.style.opacity = '0';
  document.body.appendChild(area);
  try {
    area.select();
    area.setSelectionRange(0, value.length);
    // Deprecated, still implemented, and the only thing that works outside a
    // secure context. Absent in jsdom, which is why the call is guarded rather
    // than only wrapped.
    return typeof document.execCommand === 'function' && document.execCommand('copy');
  } catch {
    return false;
  } finally {
    // `finally`, so the textarea leaves the document on the throwing path too.
    // One left behind is a focusable, selectable element a keyboard can reach.
    document.body.removeChild(area);
  }
}

/**
 * Steps 1 and 2 of the header: the Clipboard API, then `execCommand`. `done`
 * hears whether either wrote the value - synchronously where the Clipboard API
 * is not there to wait for, so a caller reports at once; step 3, selecting it
 * in place, needs the caller's element and is the caller's.
 *
 * Exported for the one other place that copies without a button of its own, a
 * list row's context menu, so the two cannot come to
 * disagree about which browsers can copy.
 */
export function copyText(value: string, done: (written: boolean) => void): void {
  // The absence is checked rather than assumed: `navigator.clipboard` is
  // undefined outside a secure context, so reading `.writeText` off it would
  // throw before anything could be reported.
  if (window.isSecureContext && navigator.clipboard?.writeText !== undefined) {
    navigator.clipboard.writeText(value).then(
      () => {
        done(true);
      },
      // A rejected promise is a refusal by the browser or by a permissions
      // policy, not a bug here. Fall through rather than reporting a failure
      // the operator can do nothing about.
      () => {
        done(execCopy(value));
      },
    );
    return;
  }
  done(execCopy(value));
}

/** Put the shown text under the operator's cursor, for the case where nothing
 *  may write to the clipboard on their behalf. */
function selectInPlace(element: HTMLElement | null): boolean {
  if (element === null) return false;
  try {
    const range = document.createRange();
    range.selectNodeContents(element);
    const selection = window.getSelection();
    if (selection === null) return false;
    selection.removeAllRanges();
    selection.addRange(range);
    return true;
  } catch {
    return false;
  }
}

/** Exported so a test asserts on the table rather than on a rendering of it, and
 *  so no two outcomes can quietly come to say the same thing. */
export { SAID as copyOutcomes };
