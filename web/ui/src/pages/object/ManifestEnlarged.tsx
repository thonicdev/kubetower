import { useMemo, useRef, type ReactNode } from 'react';

import type { ManifestLine } from '../../api';
import { Dialog } from '../../components/Dialog';
import { FindBar, FindButton } from '../../components/FindBar';
import { useFind } from '../../components/useFind';
import { focusRing } from '../../focus';
import { btnSecondary } from '../../components/buttons';
import { KeyLayer } from '../../keyboard/layers';
import { hitsByLine, ManifestLines, matchLines } from './ManifestLines';

/**
 * The manifest, enlarged: the same document as the tab, in a dialog as wide as
 * the editor's, with an *Edit* button that hands over to the editor.
 *
 * It exists to read, so nothing here can change the document. What it draws is
 * the lines as the tab draws them, never `panel.raw`: a Secret's values are
 * masked in both, and a search over the editor's text could not be told apart
 * from a way of asking whether a value is in it.
 *
 * ## In the tab's colors
 *
 * A text area cannot color a token, so a read-only text area would lose the
 * YAML colors when the document is enlarged. It draws the same
 * `ManifestLines` as the tab - the server's tokens in the theme's classes, the
 * gutter, the matches marked in place - so the same manifest reads the same in
 * both. The text is still selectable and copies as the document (the gutter is
 * not selectable), and the block takes the keyboard to scroll.
 *
 * ## Ctrl+F
 *
 * `useFind` holds the bar, and `FindButton` opens it from the end of the
 * toolbar, as in the tab: every match is marked in place and
 * the current one is scrolled into the middle of the block; the box keeps the
 * keyboard, so Enter steps on.
 */
export function ManifestEnlarged({
  name,
  lines,
  editable,
  onEdit,
  onClose,
}: {
  name: string;
  lines: readonly ManifestLine[];
  editable: boolean;
  onEdit: () => void;
  onClose: () => void;
}): ReactNode {
  const view = useRef<HTMLDivElement>(null);
  const find = useFind({ view, enabled: lines.length > 0 });
  const found = useMemo(() => matchLines(lines, find.pattern), [lines, find.pattern]);
  const byLine = hitsByLine(found, find.current(found.length));

  // The bar is on `KeyLayer.Find`, below the dialog layer: the first Escape
  // is the bar, the second the dialog.
  return (
    <Dialog
      title={`${name} manifest`}
      onClose={onClose}
      size="viewport"
      layer={KeyLayer.Dialog}
      takesEscape={!find.open}
    >
      <div ref={view} data-panel="manifest-enlarged" className="flex h-[75vh] min-h-0 flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {editable && (
            <button
              type="button"
              onClick={onEdit}
              className={btnSecondary}
            >
              Edit
            </button>
          )}
          <FindBar find={find} count={found.length} label="Find in this manifest" />
          <span className="ml-auto">
            <FindButton find={find} what="this manifest" />
          </span>
        </div>
        {/* Focusable so the keyboard scrolls it, and the dialog's first focus:
            a named, read-only region. */}
        <div
          data-dialog-focus
          tabIndex={0}
          role="region"
          aria-label={`${name} manifest, read-only`}
          className={`min-h-0 flex-1 overflow-auto rounded border border-line bg-bg ${focusRing}`}
        >
          <ManifestLines lines={lines} byLine={byLine} className="px-3 py-2 text-fg" />
        </div>
      </div>
    </Dialog>
  );
}
