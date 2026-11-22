import { useState } from 'react';

import { useSettling } from '../components/useDebounced';

/**
 * The find box of a log pane: what is typed, the settled query the buffer is
 * filtered by, the two modes, and the match being stepped to.
 *
 * What the box says, and what the buffer is filtered by, are two values,
 * because the filter is **debounced at 140 ms**: long enough that typing
 * "error" is one pass over the buffer and not five, and short enough that it
 * does not read as a pane that stopped responding. Enter forces it
 * (`settleQuery`). The lines on screen stay as they are until it settles, since
 * filtering is a pass over the buffer in hand and never a request that could
 * leave the pane empty.
 *
 * **No settling marker here**, unlike the list's box. 140 ms over a local
 * buffer is below what reads as waiting, and a live region announcing
 * "filtering" after every keystroke is noise to a screen reader for nothing a
 * sighted reader would have noticed - while the room the marker holds is taken
 * from a toolbar that is already tight in a narrow dock.
 *
 * Changing the text or either mode puts the current match back at the first.
 */
export function useLogFind(): {
  readonly typed: string;
  readonly query: string;
  readonly regex: boolean;
  readonly matchCase: boolean;
  readonly at: number;
  readonly settleQuery: () => void;
  readonly setAt: (at: number) => void;
  readonly onQuery: (text: string) => void;
  readonly onRegex: (on: boolean) => void;
  readonly onMatchCase: (on: boolean) => void;
} {
  const [typed, setTyped] = useState('');
  const [query, settleQuery] = useSettling(typed, 140);
  const [regex, setRegex] = useState(false);
  const [matchCase, setMatchCase] = useState(false);
  const [at, setAt] = useState(0);
  return {
    typed, query, regex, matchCase, at, settleQuery, setAt,
    onQuery: (text) => { setTyped(text); setAt(0); },
    onRegex: (on) => { setRegex(on); setAt(0); },
    onMatchCase: (on) => { setMatchCase(on); setAt(0); },
  };
}
