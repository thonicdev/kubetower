import type { ReactNode } from 'react';

import { btnSecondary } from '../components/buttons';
import type { Backwards } from './useLogPaging';

/** The sentence below the buffer. `retrying` marks a reconnect notice and
 *  `waiting` a follow waiting for its container to start: the two kinds a
 *  later marker may replace, and the second the one a line takes down. */
export interface Marker {
  readonly text: string;
  readonly bad: boolean;
  readonly retrying?: boolean;
  readonly waiting?: boolean;
}

/**
 * The five outcomes, rendered from one place so that they cannot drift apart.
 *
 * Precedence is fixed and is the order below. Cheap on purpose - text and one
 * button, no geometry - because it is re-rendered on the arrival path.
 *
 * The button exists **as well as** the scroll gesture, and that is not a
 * belt-and-braces: a pane showing fewer lines than fit it has no scrollbar, so
 * there is no "scroll to the top" to perform and a gesture-only trigger would
 * simply not exist on a quiet pod - which is most pods, most of the time.
 */
export function Backward({
  state,
  onMore,
  empty,
  dropped,
  cap,
}: {
  state: Backwards;
  onMore: () => void;
  empty: boolean;
  dropped: number;
  cap: number;
}): ReactNode {
  // Nothing has arrived yet, so there is nothing to be earlier *than*. Asking
  // the server how many lines precede zero is a question with no answer.
  if (empty) return null;

  // The panel let go of its oldest lines to stay within its cap. Said, because
  // a pane whose top silently moves reads as a container that never printed
  // them, and because Download writes only what is held.
  //
  // The live region is the sentence alone, which appears once; the count beside
  // it changes with every trim and would be re-announced each time.
  if (dropped > 0) {
    return (
      <p className="py-1 text-warn">
        —{' '}
        <span role="status">
          This panel keeps only its newest {cap.toLocaleString('en-US')} lines, so earlier
          lines were let go, and Download does not include them.
        </span>{' '}
        ({dropped.toLocaleString('en-US')} so far) —
      </p>
    );
  }

  if (state.busy) {
    return (
      <p className="py-1 text-muted" role="status">
        — reading earlier lines… —
      </p>
    );
  }
  if (state.fail !== undefined) {
    return (
      <p className="py-1 text-warn" role="status">
        — {state.fail}{' '}
        <button type="button" className={btnSecondary} onClick={onMore}>
          Try again
        </button>{' '}
        —
      </p>
    );
  }
  if (state.stop !== undefined) {
    // `beginning` is an ending and reads as one; the other two are this console
    // declining, and are drawn as a warning because there *is* more log.
    const ending = state.stop === 'beginning';
    return (
      <p className={`py-1 ${ending ? 'text-muted' : 'text-warn'}`} role="status">
        — {state.note === '' || state.note === undefined ? 'No earlier lines.' : state.note} —
      </p>
    );
  }
  return (
    <p className="py-1 text-muted">
      <button type="button" className={btnSecondary} onClick={onMore}>
        Read earlier lines
      </button>
    </p>
  );
}

/** How many newer lines a held selection keeps back. Nothing while none are. */
export function HeldBack({ waiting }: { waiting: number }): ReactNode {
  if (waiting <= 0) return null;
  return (
    // `select-none`, so a drag that runs past the last row does not take
    // this sentence with it into the clipboard. The live region is the
    // sentence alone, which appears once; the count beside it changes on
    // every repaint and would be re-announced each time.
    <p className="select-none py-1 text-muted">
      —{' '}
      <span role="status">Following is paused while text in this panel is selected.</span>{' '}
      {waiting.toLocaleString('en-US')} newer {waiting === 1 ? 'line is' : 'lines are'} held back
      until the selection is let go. —
    </p>
  );
}

/** The sentence below the buffer, if there is one. */
export function MarkerLine({ marker }: { marker: Marker | null }): ReactNode {
  if (marker === null) return null;
  return (
    <p className={`py-1 ${marker.bad ? 'text-warn' : 'text-muted'}`} role="status">
      — {marker.text} —
    </p>
  );
}
