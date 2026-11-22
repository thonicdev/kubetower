import type { ReactNode } from 'react';
import { useParams } from 'react-router';

import { Icon } from '../components/Icon';
import { focusRing } from '../focus';
import { useClusterVerdict } from './downCluster';
import { useKindsAt } from './railDrawn';
import { setColumnHidden, useMenuHidden } from './menuMemory';

/**
 * Shows and hides the kinds column.
 *
 * ## At the foot of the rail, below Arrange
 *
 * It is `form="rail"` in the
 * rail's foot, below Arrange and above the session, as a glyph with its word on
 * `title` and in the accessible name - Arrange's shape, because the rail is
 * 56px at every width.
 *
 * **Where there is no rail it is in the same corner** (`form="corner"`): the
 * open column's foot, or on the page when the column is folded (`KindsCorner`)
 * - one cluster on the desktop, and the in-cluster console.
 * The same corner in every mode, so the column folds in-cluster too and takes
 * the phone-width overlay.
 *
 * ## When it is drawn
 *
 * On a cluster's pages only: elsewhere there is no column (`Menu.tsx`,
 * `empty:hidden`). And not on a cluster the probe found unreachable, which draws
 * no column either: a button there would toggle
 * nothing anybody can see. Ctrl+B is `Menu.tsx`'s and does not depend on where
 * this button is.
 *
 * One control for both directions, saying which it is: `aria-expanded` on a
 * button that `aria-controls` the column, named *Kinds*, and the shortcut on
 * its `title`.
 */
export function KindsToggle({ form }: { form: 'rail' | 'corner' }): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const { down } = useClusterVerdict(cluster);
  // The width decides which state this is: the stored
  // choice on a wide window, closed-unless-opened over the page on a phone.
  const hidden = useMenuHidden();
  if (cluster === undefined || down !== undefined) return null;

  const title = hidden ? 'Show the kinds column (Ctrl+B)' : 'Hide the kinds column (Ctrl+B)';
  // The same button in both places, the glyph alone as Arrange is: the word is
  // never printed at 56px, so the name it would have given is the label's.
  return (
    <button
      type="button"
      aria-controls="menu"
      aria-expanded={!hidden}
      aria-label="Kinds"
      title={title}
      data-kinds-at={form}
      onClick={() => setColumnHidden(!hidden)}
      className={`flex min-h-6 items-center justify-center rounded-md border border-line px-1 py-1 text-xs text-muted hover:bg-panel-2 pointer-coarse:min-h-11 ${focusRing}`}
    >
      <Icon name="workloads" />
    </button>
  );
}

/** Whether `KindsCorner` is on the page: no rail, and the column folded. A list
 *  reads it to keep a row free under its last one. */
export function useKindsOnPage(): boolean {
  const { cluster } = useParams<{ cluster: string }>();
  const { down } = useClusterVerdict(cluster);
  const hidden = useMenuHidden();
  const at = useKindsAt();
  return at === 'corner' && hidden && cluster !== undefined && down === undefined;
}

/** The Kinds button where there is no rail and the column is folded: at the
 *  bottom left of the page, over it, and the page keeps the window's whole width.
 *  A strip of its own held a 40px column that was empty but for this button, the
 *  width a phone can least give. A list keeps a row free for it
 *  (`ResourceList.tsx`), so it covers no cell there; under the page's sheet of a
 *  drawer it waits, and the dock is below it. Open, the column carries the button
 *  at its own foot (`Menu.tsx`). The rail's foot's place either way. */
export function KindsCorner(): ReactNode {
  if (!useKindsOnPage()) return null;
  return (
    <div data-kinds-corner="" className="absolute bottom-1.5 left-1.5 z-10 rounded-md bg-panel">
      <KindsToggle form="corner" />
    </div>
  );
}
