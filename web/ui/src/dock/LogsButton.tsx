import type { ReactNode } from 'react';

import { btn } from '../components/buttons';
import { Icon } from '../components/Icon';
import { watching } from '../hover';
import { useShellIfPresent } from '../shell/ShellContext';
import { useDockIfPresent } from './DockContext';

/**
 * Open a log panel on this object.
 *
 * ## Where it is
 *
 * In `ObjectDetail`'s control row, at **the near end, with Shell and Claude.**
 * The row is split by what its controls do rather than by what they are - the
 * verbs change the object and these watch it - and a log is the plainest
 * watching there is. Nothing here depends on the position.
 *
 * ## The glyph
 *
 * Every control in that row is an `<Icon/>` plus its word (`Terminals.tsx`
 * states the convention and the verbs follow it through `GLYPH`), so this one
 * is `i-logs` plus its word rather than a bare label. The dock's `MARK` table
 * is a different register - typographic marks for tab titles - and is
 * deliberately left alone.
 *
 * The button's own padding and focus ring match the row: `py-0.5` and a ring,
 * so a keyboard operator sees this control as they see the others.
 *
 * ## It offers nothing where there is nothing to offer
 *
 * Only pods have a log to read, and only a dock can hold the panel. Both are
 * checked here rather than in the caller, so the caller is one line and cannot
 * get either wrong - and `useDockIfPresent` answers `null` rather than throwing
 * where the Shell is not above it, so a page rendered in isolation is simply
 * missing the control rather than failing to draw.
 *
 * ## The refusal is not composed here
 *
 * At the ceiling `openLogs` answers `false` and the panel's own status line
 * already carries the sentence with the number in it. A second sentence here
 * would be a second place the limit is written, and the two would disagree the
 * first time one changed.
 *
 * ## Opening the log closes the drawer
 *
 * A pane opened at the bottom while the drawer stays open on the right is read
 * through what is left between the rail, the drawer and the panel's own
 * height. Once the operator has
 * asked for the log they want the width for it, so the drawer is emptied
 * **after** `openLogs` has answered - and only on `true`. A refused open leaves
 * the drawer where it was: the operator is still on the object, and the note
 * says why nothing happened.
 *
 * ### Even at the ceiling, for a panel already there
 *
 * The drawer is a flex sibling of the column holding the dock (`Shell.tsx`,
 * `Drawer.tsx`), so it *displaces* rather than overlays, and an open one costs
 * the panel a large share of its width. A terminal in the dock refits correctly
 * to that, but is left at a little over half its columns, so wide output wraps
 * mid-word.
 *
 * So closing the drawer is not a nicety here; it is what gives the terminal its
 * width back. `room` counts without looking at what is asked for, so at the
 * cluster's eight it would refuse a panel on a pod **already in the tab
 * strip**, and the drawer would stay. `findLogsTab` is asked before `room`:
 * reopening a panel that is already there focuses it and answers `true`, so
 * the drawer closes. The refusal above is still right for a panel that
 * genuinely does not fit.
 *
 * `useShellIfPresent` and not `useShell`, for the reason `useDockIfPresent` is
 * used one line above it: this control is rendered where there may be no Shell,
 * and there a drawer to close does not exist rather than failing to.
 * `Terminals.tsx` does the same on its Shell control, for the identical
 * reason.
 */
export function LogsButton({
  cluster, kind, namespace, name,
}: {
  cluster: string;
  kind: string;
  namespace: string | undefined;
  name: string;
}): ReactNode {
  const dock = useDockIfPresent();
  const shell = useShellIfPresent();
  if (dock === null) return null;
  // Pods only. A Deployment has no log of its own - it has replicas that do, and
  // offering a control that would have to pick one for the operator is offering
  // a different thing under the same word.
  if (kind.toLowerCase() !== 'pods' && kind.toLowerCase() !== 'pod') return null;
  if (namespace === undefined || namespace === '') return null;

  return (
    <button
      type="button"
      // `watching`, and not `hover:text-fg` typed here: this control opens a
      // stream, so its hover moves the frame to the accent *and* the word to
      // `--fg`. The word alone would make the button that opens the log look
      // like the one that copies a value.
      className={`${btn} border-line text-muted ${watching}`}
      onClick={() => {
        if (dock.openLogs({ cluster, kind: 'pods', namespace, name })) shell?.closeDrawer();
      }}
    >
      <Icon name="logs" />
      Logs
    </button>
  );
}
