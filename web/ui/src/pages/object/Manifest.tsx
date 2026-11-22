import { useId, useMemo, useRef, useState, type ReactNode } from 'react';

import { useManifest } from '../../api';
import { FindBar, FindButton } from '../../components/FindBar';
import { useFind } from '../../components/useFind';
import { btnSecondary } from '../../components/buttons';
import { ValueView } from '../../state/Result';
import { ManifestEditor } from './ManifestEditor';
import { ManifestEnlarged } from './ManifestEnlarged';
import { hitsByLine, ManifestLines, matchLines } from './ManifestLines';

/**
 * The object's manifest, read.
 *
 * ## Tokens, and never markup
 *
 * The server colors (`internal/web/yamlcolour.go` — one colouriser, so a custom
 * resource's manifest and a built-in one do not read differently) and returns
 * `{text, class}` pairs. This renders them as spans. The contract states the
 * reason in as many words: *a contract that ships HTML ships an injection
 * surface with it*, and `react/no-danger` is an error on this side of it. So
 * there is no `dangerouslySetInnerHTML` here and there cannot be one.
 *
 * ## The class list is closed, and an unknown class is not a crash
 *
 * Eight classes, from the contract's own enum. A ninth added to the YAML and not
 * here has to render as something, and the something is `plain` — legible, and
 * the token's text is never lost. Dropping the token, or throwing, would lose a
 * line of somebody's manifest to a styling change.
 *
 * ## A Secret's values are masked before they reach this
 *
 * By `detail.Trimmed`, server-side, which is also the base of the edit diff — so
 * the mask is identical on both sides and cannot be written back over a value.
 * The masking announces itself: the server prepends a comment to the document,
 * which arrives here as ordinary `comment` tokens and is drawn like any other
 * comment. Nothing in this file knows what a Secret is, and that is deliberate:
 * a client that decided what to hide would be the second copy of a rule whose
 * whole point is that there is one. The one thing handed on is what the server
 * said it masked - `panel.maskedKeys` - so the editor can ask the one door for
 * exactly those, and it too never has to know what a Secret is.
 *
 * ## A search, over what is drawn
 *
 * Ctrl+F (⌘F) while the object's panel has the keyboard, or the search control
 * in the toolbar, opens a find bar over the document: every match marked in
 * place, `n/m`, Enter and Shift+Enter to move, Escape to close. `useFind` holds
 * the bar and says when the key is the view's.
 *
 * **What is searched is the tokens' text and nothing else** - the lines as they
 * are drawn, one line at a time, so a match never runs across a line break the
 * reader cannot see it cross. A Secret's values are therefore searched exactly
 * as far as they are shown: the server masked them before they arrived, and
 * the placeholder it drew is the only thing here to find. `panel.raw` is the
 * editor's and is never searched, so this search cannot become a way of asking
 * whether a value is in a document the reader was not shown.
 *
 * Nothing in the read view folds, so a match is always on a drawn line, and
 * the current one is scrolled into the middle of the panel.
 */
export function Manifest({
  cluster,
  kind,
  namespace,
  name,
}: {
  cluster: string;
  kind: string;
  namespace: string | undefined;
  name: string;
}): ReactNode {
  const query = useManifest(cluster, kind, namespace, name);
  /* Reading and editing are two states of one tab rather than two tabs. The
     reason is that the editor is opened *from*
     the document: a user who has just read a value is one keystroke from
     changing it, and a second tab puts a navigation in between. */
  const [editing, setEditing] = useState(false);
  // The large read-only view. Edit in it is `setEditing(true)` with
  // this closed, so the two dialogs are never open together.
  const [enlarged, setEnlarged] = useState(false);
  const noteId = useId();

  // The search reads the payload outside `ValueView`, because the count is the
  // bar's and the bar must know it whichever line the current match is on. It
  // is the same object the render below draws: `ValueView` hands its `ok` arm
  // `query.data.data` and nothing else.
  const panel = query.data?.state === 'ok' ? query.data.data : undefined;
  const view = useRef<HTMLDivElement>(null);
  const find = useFind({
    view,
    enabled: panel !== undefined && panel.lines.length > 0,
    // The object's whole panel, so that Ctrl+F with the focus on the tab
    // strip that has just opened this tab is this tab's.
    region: '[data-object-chrome]',
  });
  const found = useMemo(() => matchLines(panel?.lines ?? [], find.pattern), [panel, find.pattern]);
  const byLine = hitsByLine(found, find.current(found.length));

  return (
    <ValueView query={query} what="this manifest">
      {(panel) => (
        <>
          <div ref={view} data-panel="manifest" className="flex min-h-0 flex-col">
            {/* The toolbar: Edit where the object may be written, the find bar
                while it is open, and the control that opens it at the far end,
                where it stays - pressing it again brings the keyboard back to
                the box, and it is what the focus returns to on Escape. */}
            <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-1.5">
              {panel.editable === true && panel.raw !== undefined && (
                <button
                  type="button"
                  onClick={() => setEditing(true)}
                  className={btnSecondary}
                >
                  Edit this manifest
                </button>
              )}
              {/* Refused with a sentence: drawn disabled, carrying it, as a verb
                  the console may not perform is. Refused with none: the person
                  may not write this object, and no control is drawn. */}
              {panel.editable === false && panel.editableNote !== undefined && (
                <button
                  type="button"
                  disabled
                  title={panel.editableNote}
                  aria-describedby={noteId}
                  className={btnSecondary}
                >
                  Edit this manifest
                </button>
              )}
              <button
                type="button"
                onClick={() => setEnlarged(true)}
                className={btnSecondary}
              >
                Enlarge
              </button>
              <FindBar find={find} count={found.length} label="Find in this manifest" />
              <span className="ml-auto">
                <FindButton find={find} what="this manifest" />
              </span>
            </div>
            {panel.editable === false && panel.editableNote !== undefined && (
              // Which setting refuses the write, and not only that one does.
              // `editable: false` on its own leaves a user looking at a
              // missing control with no way to find out what to change — and the
              // causes are different people's to fix.
              <p id={noteId} role="status" className="border-b border-line px-3 py-1 text-xs text-muted">
                {panel.editableNote}
              </p>
            )}
            <ManifestLines lines={panel.lines} byLine={byLine} className="min-h-0 overflow-auto px-3 py-2" />
          </div>

          {/* The editor is a dialog over the page, so the read view
              stays mounted underneath rather than being swapped out for it:
              closing the dialog returns to the document already drawn, and the
              drawer behind it still says which object is being edited. The
              editor's own `Cancel` and the dialog's Escape are the same act, so
              both arrive here as `setEditing(false)`. */}
          {enlarged && (
            <ManifestEnlarged
              name={name}
              lines={panel.lines}
              editable={panel.editable === true && panel.raw !== undefined}
              onEdit={() => {
                setEnlarged(false);
                setEditing(true);
              }}
              onClose={() => setEnlarged(false)}
            />
          )}
          {editing && panel.raw !== undefined && (
            <ManifestEditor
              cluster={cluster}
              kind={kind}
              namespace={namespace}
              name={name}
              document={panel.raw}
              resourceVersion={panel.resourceVersion}
              maskedKeys={panel.maskedKeys ?? []}
              onDone={() => setEditing(false)}
              onCancel={() => setEditing(false)}
            />
          )}
        </>
      )}
    </ValueView>
  );
}
