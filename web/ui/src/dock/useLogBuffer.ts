import { useEffect, useState, type Dispatch, type SetStateAction } from 'react';

import { LogBuffer, dockLogBudget } from './logBuffer';

/**
 * A log pane's buffer, and the repaint that draws it.
 *
 * **`useState` and not `useRef`**, for a reason that is not style. The buffer
 * is read during render - it is what is drawn - and `react-hooks/refs` refuses
 * that outright, correctly: a ref read during render is a value React does not
 * know changed. A state initializer runs once, so this is one object for the
 * life of the component, mutated imperatively by the stream and never
 * replaced; `bump` is what tells React a repaint is due. `useRef(new
 * LogBuffer())` would also have constructed and thrown away a buffer on every
 * render.
 *
 * Every pane shares one budget of lines while it is mounted, so the dock's
 * ceiling is not sixteen panes at the per-pane cap each. An
 * effect and not the initializer, because StrictMode builds the state twice
 * and only the one that mounts may hold a share.
 *
 * A share shrinks when another pane joins, and this pane is trimmed then,
 * ended or not: the repaint is how its rows and its "let go" sentence follow.
 */
export function useLogBuffer(): {
  readonly buffer: LogBuffer;
  readonly bump: Dispatch<SetStateAction<number>>;
} {
  const [buffer] = useState(() => new LogBuffer());
  const [, bump] = useState(0);
  useEffect(() => {
    dockLogBudget.join(buffer, () => bump((n) => n + 1));
    return () => dockLogBudget.leave(buffer);
  }, [buffer]);
  return { buffer, bump };
}
