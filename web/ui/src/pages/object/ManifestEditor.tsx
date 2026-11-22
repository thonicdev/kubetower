import { useMutation, useQueryClient } from '@tanstack/react-query';
import { lazy, Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useBlocker } from 'react-router';

import {
  applyCreate,
  applyManifest,
  invalidateActedObjects,
  previewCreate,
  previewManifest,
  revealDataValue,
  type ActOutcome,
  type ConfirmationPlan,
  type PlanFacts,
} from '../../api';
import { Dialog } from '../../components/Dialog';
import { FindBar, FindButton } from '../../components/FindBar';
import { findRanges, useFind } from '../../components/useFind';
import { btnDanger, btnPrimary, btnSecondary } from '../../components/buttons';
import { focusRing, focusRingTight, focusRoom } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import type { CodeHandle } from './ManifestCode';
import { changedLines, lineHunks, lineOffset, stepChange } from './manifestChanges';
import { anyMasked, remask, revealInto, toBase64, withoutMaskNote } from './manifestMask';

/** CodeMirror, in a chunk of its own: fetched the first time a document is
 *  edited or created, never with the console's first load. */
const ManifestCode = lazy(() => import('./ManifestCode'));

/**
 * Editing an object from the console, and the review in front of it.
 *
 * ## A real editor
 *
 * The document is edited in CodeMirror 6 (`ManifestCode.tsx`): YAML colors,
 * line numbers, and a caret the editor places itself.
 *
 * Not a colored textarea. Coloring
 * a textarea means a highlighted layer drawn under a transparent one, and if
 * its font metrics, its scroll offset or its wrapping are a fraction out, the
 * caret sits beside the character it is editing - on the surface that patches
 * production objects. **That objection is to the painted layer, and an editor
 * has none.** CodeMirror draws the text it edits as the colored text itself,
 * one DOM, measures where each character landed, and places its caret and its
 * selection from those measurements. There are not two layers to fall out of
 * register, so the caret is on the character because the character is what it
 * was measured from. Schema validation and completion of Kubernetes fields
 * are not part of it (the issue says so).
 *
 * The colors are the read view's (`ManifestLines.tsx`): the same theme tokens
 * for keys, strings, numbers and booleans, comments and anchors, so a document
 * reads the same on both sides of *Edit*. The editor is loaded lazily, so
 * CodeMirror costs nothing until a document is opened for editing.
 *
 * ## Three keys made to behave
 *
 * Tab indents, Shift+Tab outdents, Enter keeps the indentation, and it is spaces
 * and never tabs - a tab in a YAML document is a parse error, and an editor that
 * inserts one on the key named Tab is an editor that breaks documents for the
 * reason it was added. CodeMirror's own Tab and Enter are replaced by these
 * (`ManifestCode.tsx`), so they behave as the textarea's did; its undo and
 * redo come with it.
 *
 * Escape stays the dialog's. The keyboard stack listens in the capture phase
 * and gives it to the top layer before the editor sees it, and the editor's
 * keymap leaves Escape out besides.
 *
 * ## Closing an edited document asks first
 *
 * Escape, the X and Cancel close the dialog, and a document typed into would
 * go with it. So once the document differs from the one opened - a template
 * typed into counts - closing asks "Discard your changes?" (Discard, Keep
 * editing), with the keyboard on Keep editing and Escape meaning the same. A
 * document as it was opened closes at once: there is nothing to lose. The
 * question is a sibling at `KeyLayer.Confirm`, like the plan's confirmation.
 *
 * Leaving the page is closing it too. While the document is edited, a move to
 * another address inside the console - Back, a link, the redirect a lapsed
 * session takes - is held by React Router's `useBlocker` and asks the same
 * question: Discard goes on to where the person was going, Keep editing stays.
 * (`useBlocker` needs a data router; `createApp` mounts one.) A reload, a
 * closed tab or an address typed in leave the document, and there the browser
 * asks, through `beforeunload`. A document as it was opened, or one saved,
 * leaves freely.
 *
 * ## Review is two requests, and only the second one writes
 *
 * `preview` validates against the cluster with a dry run and changes nothing.
 * Only the dialog it draws posts to `apply`, and it posts the **token** rather
 * than the document: the act is bound to the plan the operator read, the server
 * re-derives the facts before acting, and a confirmation about an object that
 * has since changed is refused with a 409 instead of applied.
 *
 * ## A Secret's values come through the one door, on request, and leave as the mask
 *
 * The document arrives with `<masked>` over every value of a Secret
 * (`detail.Trimmed`), and the server says which keys it masked in
 * `ManifestPanel.maskedKeys`. An operator who needs the values presses *Reveal*,
 * and the editor asks `revealEnvValue` for each key - **one request per key**,
 * which is one line on the record per key, the same route and the same
 * line that *Show* on the Data tab writes. Not a per-manifest reveal endpoint:
 * the contract says a masked value "reaches a view through exactly one door",
 * and a second route that answered with every value at once would be a second
 * door with a single audit line for ten keys.
 *
 * On the way out, every revealed value the operator left alone is written back
 * as the mask (`manifestMask.ts` says why in full: `internal/edit` diffs
 * against the masked form, so a value left in place would enter the patch as a
 * change). "Nothing has been changed" is therefore judged on the document
 * *as it would be sent*, so looking at the values does not count as changing
 * them.
 *
 * A key the door refuses keeps its mask, and the refusal is drawn beside it in
 * the server's words; the key can still be changed by typing a new base64 over
 * the mask, which is what the server's header comment says.
 *
 * ## It is a dialog, and not the drawer
 *
 * Replacing the read view inside the object drawer would edit the document
 * through a letterbox: the drawer is `var(--drawer-w)` - `min(560px, 46vw)`,
 * and a manifest is 80 to 120 columns of YAML, so every line would wrap or
 * scroll. The editor is raised in a `Dialog`
 * at `size="viewport"`, which is the size this console already means by "most of
 * the window" - the metrics panel's full-size chart is the other one. **The read
 * view stays in the drawer**: only the edit act moved, so the colored document
 * is still where it was and the drawer still says which object this is.
 *
 * The height is on the body rather than on the `Dialog`, which is how
 * `Metrics.tsx` sizes its zoom (`h-[60vh]` inside the same `size`): the dialog
 * says how wide, the content says how tall. `h-[75vh]` plus the dialog's own
 * chrome is about 85% of the window, and it is a proportion
 * rather than a pixel count, so a short window gets a smaller dialog instead of
 * one that overflows.
 *
 * Bigger is colored too: the editor at the top of this file keeps the caret
 * beside the character it is editing, at 80% of the window as at 560px.
 *
 * ## The confirmation is a sibling of the dialog, not a child of it
 *
 * Both are `fixed` veils, so which one is on top is decided by paint order, and
 * paint order is tree order. As a sibling *after* the editor's dialog the
 * confirmation is over it without either one needing a `z-index` of its own -
 * which is the arrangement `keyboard/layers.ts` exists to get away from.
 *
 * **Escape is decided by the layer, not by that tree order.** The confirmation
 * claims `KeyLayer.Confirm`, which is above `KeyLayer.Dialog`, so it takes the
 * key however the two happen to be written.
 *
 * Leaning on the tie-break instead (both claiming `KeyLayer.Dialog`, with
 * "whatever opened last" winning) would make the sibling-after-the-dialog
 * arrangement load-bearing for two unrelated things at once: paint order, which
 * it genuinely decides, and the keyboard, which `keyboard/layers.ts` exists to
 * take away from it. Moving this element would silently close the document
 * the user was being asked about. The keyboard half is pinned in `keyboard/stack.test.ts`, in both
 * registration orders; the paint order is not pinned by anything and is the
 * weaker of the two.
 *
 * ## The changes, counted and stepped through
 *
 * Once the document differs from the one opened, the foot of the dialog says
 * how many lines changed, with ↑ and ↓ to step from one change to the next and
 * `k/N` for the one the editor is on, as a merge request's diff does.
 * `manifestChanges.ts` says what a change is: a
 * hunk of a line diff. It is computed on the document *as it would be sent*,
 * like "nothing has been changed", so a revealed value left alone is not a
 * change; `remask` rewrites a value on its own line, so the line numbers are
 * the editor's. A step puts the caret at the start of the change and scrolls
 * it to the middle of the editor, and it does not select the lines: a selection
 * in a field being typed into is one keystroke from replacing them. A rewrite
 * too large to compare is said to be uncounted, with no arrows, rather than
 * given a figure nobody computed.
 *
 * ## The tab's search, over the document being edited
 *
 * The same find bar as the read view (`useFind`, `FindBar`): the control at
 * the end of the row above the document, Ctrl+F (⌘F) with the keyboard in the
 * document, every match marked in place, `n/m`, Enter and Shift+Enter, Escape.
 * What is searched is the editor's text as it stands - what is on screen,
 * masks and revealed values alike - so it follows the typing. A match never
 * runs across a line break: the box takes one line. The marks are CodeMirror
 * decorations (`ManifestCode.tsx`), so the document's text is never touched,
 * and the current match is scrolled to the middle while the keyboard stays in
 * the box. The bar is on `KeyLayer.Find`, below the dialog, so the dialog
 * steps aside for the Escape that closes it.
 *
 * ## The same editor creates
 *
 * The list's "+" opens a template in this dialog with `create` set, and only
 * the two requests and the words change: the review is `previewCreate` and the
 * act `applyCreate`, a dry run of a create rather than of a patch, so nothing
 * existing can be overwritten from here. A template is reviewable as it came -
 * the person may well want exactly it - so *nothing has changed* does not hold
 * the button back, and there is no change counter: lines changed against a
 * template are not changes to anything. There is no object yet, so no name, no
 * `resourceVersion` and nothing masked. The edit mode is untouched by all of it.
 */
export function ManifestEditor(props: EditProps | CreateProps): ReactNode {
  const { cluster, kind, namespace, document, onCancel } = props;
  const create = props.create;
  const name = create === undefined ? props.name : '';
  const maskedKeys: readonly string[] = (create === undefined ? props.maskedKeys : undefined) ?? [];
  const [text, setText] = useState(document);
  const [plan, setPlan] = useState<ConfirmationPlan | null>(null);
  /* Key -> the base64 the door returned. Only what is here is ever masked
     again on the way out, so a key the door refused, or one the operator had
     typed over before revealing, is never touched by `remask`. */
  const [revealed, setRevealed] = useState<Readonly<Record<string, string>>>({});
  const [refused, setRefused] = useState<readonly string[]>([]);
  /* Whether the reveal took out the server's note that the values are masked.
     The document opened is then compared without it too, so taking out a
     comment that had become untrue is not counted as an edit. */
  const [noteDropped, setNoteDropped] = useState(false);
  /* Closing an edited document asks first: the question is up. */
  const [asking, setAsking] = useState(false);
  const code = useRef<CodeHandle | null>(null);
  const client = useQueryClient();

  const body = useRef<HTMLDivElement>(null);
  const find = useFind({ view: body, enabled: text !== '' });
  const found = useMemo(() => findRanges(text, find.pattern), [text, find.pattern]);
  const at = find.current(found.length);
  // Scrolled to on a new search or a step, and not on every keystroke in the
  // document: the reader typing is not looking for the match.
  const foundNow = useRef(found);
  useLayoutEffect(() => {
    foundNow.current = found;
  });
  useEffect(() => {
    const match = foundNow.current[at];
    if (match !== undefined) code.current?.reveal(match.from);
  }, [find.pattern, at]);

  /* The document as it would leave: revealed-and-untouched values as the mask.
     Both the "nothing changed" sentence and the request read this and never
     `text`, so the two cannot disagree about what a change is. */
  const outgoing = remask(text, revealed);
  const original = noteDropped ? withoutMaskNote(document) : document;
  const edited = outgoing !== original;
  // A template has nothing to differ from: it is reviewable as it came.
  const unchanged = create === undefined && !edited;
  const hunks = useMemo(
    () => (create === undefined ? lineHunks(original, outgoing) : []),
    [create, original, outgoing],
  );

  /* Escape, the X and Cancel all come here. An edited document - a template
     typed into included - asks before it is thrown away; one as it was opened
     closes at once, as there is nothing to lose. */
  const close = (): void => {
    if (edited) setAsking(true);
    else onCancel();
  };
  /* Leaving the page with the document edited: another address in the
     console, held until answered, and the page itself, which the browser asks
     about. A move that stays on the same page - only its hash - loses nothing. */
  const leaving = useBlocker(
    ({ currentLocation, nextLocation }) =>
      edited &&
      (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search),
  );
  useEffect(() => {
    if (!edited) return undefined;
    const ask = (event: BeforeUnloadEvent): void => {
      event.preventDefault();
      // Older browsers ask only when this is set.
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', ask);
    return () => window.removeEventListener('beforeunload', ask);
  }, [edited]);
  /* Which change the counter is on. Held as a number and clamped on read, so
     a change that disappears under the user's typing moves the counter to
     the last one left instead of pointing past the end. */
  const [atChange, setAtChange] = useState(0);
  const current = Math.min(atChange, Math.max(0, hunks.length - 1));

  const goTo = (by: 1 | -1): void => {
    const next = stepChange(current, by, hunks.length);
    setAtChange(next);
    const hunk = hunks[next];
    if (hunk === undefined) return;
    code.current?.moveCaret(lineOffset(text, hunk.line));
  };

  const reveal = useMutation({
    mutationFn: () =>
      Promise.all(
        maskedKeys.map(async (key) => ({
          key,
          answer: await revealDataValue(cluster, kind, namespace, name, key),
        })),
      ),
    onSuccess: (answers) => {
      let next = text;
      const got: Record<string, string> = { ...revealed };
      const problems: string[] = [];
      for (const { key, answer } of answers) {
        if (answer.state !== 'ok') {
          problems.push(`${key}: ${answer.reason.message}`);
          continue;
        }
        const base64 = toBase64(answer.data.value);
        const opened = revealInto(next, key, base64);
        // `undefined` is a key that is not masked in the document any more -
        // the user typed over it first - and a reveal must not write over
        // what they typed.
        if (opened === undefined) continue;
        next = opened;
        got[key] = base64;
      }
      // Every value shown: the note saying they are masked is untrue now.
      if (!anyMasked(next)) {
        const bare = withoutMaskNote(next);
        if (bare !== next) {
          next = bare;
          setNoteDropped(true);
        }
      }
      setText(next);
      setRevealed(got);
      setRefused(problems);
    },
    onError: (error: Error) => setRefused([error.message]),
  });

  const review = useMutation({
    mutationFn: () => {
      if (props.create !== undefined) return previewCreate(cluster, outgoing);
      const { resourceVersion } = props;
      return previewManifest(cluster, {
        document: outgoing,
        ...(resourceVersion === undefined ? {} : { resourceVersion }),
      });
    },
    onSuccess: setPlan,
  });

  const commit = useMutation({
    mutationFn: (token: string) =>
      create === undefined ? applyManifest(cluster, token) : applyCreate(cluster, token),
    onSuccess: async (outcome: ActOutcome) => {
      setPlan(null);
      if (props.create !== undefined) {
        // No object's reads to name - there was no object - so the kind alone:
        // its list is where the new row appears, and awaited so the dialog
        // closes onto a list that already shows it.
        await invalidateActedObjects(client, cluster, kind, []);
        props.onDone(outcome);
        return;
      }
      // Every read of this object is stale, and they are separate keys on
      // purpose - the drawer, the manifest and the data block answer different
      // questions about one thing. The list's row is one of them: a manifest
      // write changes labels, replicas and annotations, which is what the
      // columns draw.
      //
      // Awaited, unlike the other three callers: `onDone` closes the dialog on
      // to the document underneath, and closing before the refetch lands puts
      // the pre-save YAML back on screen as the result of saving.
      await invalidateActedObjects(client, cluster, kind, [
        { kind, ...(namespace === undefined ? {} : { namespace }), name },
      ]);
      props.onDone();
    },
  });

  const title = create === undefined ? `Edit ${name}` : `New ${create.kind}`;

  return (
    <>
      <Dialog title={title} onClose={close} size="viewport" takesEscape={!find.open}>
        <div ref={body} data-panel="manifest-editor" className="flex h-[75vh] min-h-0 flex-col gap-2">
          {create?.generic === true && (
            <p data-note="generic-template" className="border-b border-line pb-2 text-xs text-muted">
              This kind has no written template: the document holds only its{' '}
              <code className="font-mono">apiVersion</code>, <code className="font-mono">kind</code>{' '}
              and <code className="font-mono">metadata</code>, and the body is yours to write.
            </p>
          )}
          {maskedKeys.length > 0 && (
            <Reveal
              count={maskedKeys.length}
              revealed={Object.keys(revealed).length}
              pending={reveal.isPending}
              refused={refused}
              onReveal={() => reveal.mutate()}
            />
          )}

          {/* The read view's find row: the bar while it is open, and the
              control that opens it at the far end. */}
          <div className="flex flex-wrap items-center gap-2">
            <FindBar find={find} count={found.length} label="Find in this manifest" />
            <span className="ml-auto">
              <FindButton find={find} what="this manifest" />
            </span>
          </div>

          {/* The frame carries the font, the size and the line height the
              editor inherits, and the focus ring, around the gutter and the
              text together. `focus-within` rather than `focus-visible`: the
              element focused is a text field, where the two fire together
              anyway (`focus.ts`), and it is a descendant of this frame. */}
          <div className="min-h-0 flex-1 overflow-hidden rounded border border-line bg-bg font-mono text-xs leading-5 text-fg focus-within:outline-2 focus-within:outline-accent focus-within:-outline-offset-1">
            <Suspense
              fallback={
                <p role="status" className="px-3 py-2 text-muted">
                  Loading the editor…
                </p>
              }
            >
              <ManifestCode
                value={text}
                onChange={setText}
                followsName={create !== undefined}
                label={create === undefined ? `${name} manifest` : `New ${create.kind} manifest`}
                onReady={(handle) => {
                  code.current = handle;
                }}
                found={found}
                current={at}
              />
            </Suspense>
          </div>

          {/* The dry run's refusal, whole: the cluster's answer names every
              field it refused, and the one that explains it is often the last.
              Wrapped, and scrolling past a few lines so the document keeps the
              room, rather than cut at the edge where nobody can act on it. */}
          {review.isError && (
            <p
              role="alert"
              className="max-h-24 shrink-0 overflow-auto whitespace-pre-wrap wrap-anywhere text-xs text-bad"
            >
              {messageOf(review.error)}
            </p>
          )}

          {/* Pinned at the bottom: the document is what the dialog is for, and
              these are the two things done with it. */}
          <div className="flex flex-wrap items-center gap-2 border-t border-line pt-2">
            <button
              type="button"
              onClick={() => review.mutate()}
              disabled={review.isPending || unchanged}
              title={!review.isPending && unchanged ? 'No change to review' : undefined}
              className={btnPrimary}
            >
              {review.isPending ? 'Reviewing…' : create === undefined ? 'Review changes' : 'Review'}
            </button>
            <button
              type="button"
              onClick={close}
              className={btnSecondary}
            >
              Cancel
            </button>
            {hunks.some((hunk) => hunk.approximate === true) ? (
              <span data-manifest-changes="" className="ml-auto text-xs text-muted">
                Too many changed lines to count
              </span>
            ) : hunks.length > 0 && (
              <Changes
                lines={changedLines(hunks)}
                at={current}
                count={hunks.length}
                onStep={goTo}
              />
            )}
          </div>
        </div>
      </Dialog>

      {asking && <Discard onDiscard={onCancel} onKeep={() => setAsking(false)} />}

      {leaving.state === 'blocked' && <Discard onDiscard={() => leaving.proceed()} onKeep={() => leaving.reset()} />}

      {plan !== null && (
        <Confirmation
          title={create === undefined ? 'Apply this manifest?' : createTitle(plan, create.kind)}
          plan={plan}
          pending={commit.isPending}
          error={commit.isError ? messageOf(commit.error) : undefined}
          onCancel={() => setPlan(null)}
          onApply={() => commit.mutate(plan.token)}
        />
      )}
    </>
  );
}

interface Common {
  cluster: string;
  /** The discovery plural: the list's slug, whose reads a write makes stale. */
  kind: string;
  namespace: string | undefined;
  document: string;
  onCancel: () => void;
}

/** Editing an object that exists: the mode this editor was written for. */
interface EditProps extends Common {
  create?: undefined;
  name: string;
  resourceVersion: string | undefined;
  /** The `data` keys whose value in `document` is the mask, as the server
   *  said - `ManifestPanel.maskedKeys`. Empty on anything but a Secret. */
  maskedKeys?: readonly string[];
  onDone: () => void;
}

/** Creating one from a template. */
interface CreateProps extends Common {
  create: {
    /** The API's spelling of the kind, `Deployment`, for the title. */
    kind: string;
    /** The template is the bare skeleton: say the body is the person's. */
    generic?: boolean;
  };
  onDone: (outcome: ActOutcome) => void;
}

/**
 * The create confirmation's title, from the document the plan was made for -
 * its kind and its namespace, as the server read them - and never from the
 * list the "+" was pressed on: a document of another kind is created as that
 * kind, and a title naming the list's would ask about one object and create
 * another. The list's kind stands in only when the plan names no target.
 */
function createTitle(plan: ConfirmationPlan, fallbackKind: string): string {
  const target = plan.facts.targets[0];
  const kind = target?.kind ?? fallbackKind;
  const namespace = target?.namespace;
  return namespace === undefined || namespace === ''
    ? `Create this ${kind}?`
    : `Create this ${kind} in ${namespace}?`;
}

/**
 * Asked before an edited document is thrown away. A sibling of the editor's
 * dialog, after it, at `KeyLayer.Confirm`, for the reasons the confirmation of
 * a plan is (the header says them): above the editor in the keyboard stack, so
 * Escape here answers "keep editing" and never also closes the editor.
 *
 * The act first, as in every dialog's foot, and the keyboard on the way back:
 * a reflex Enter keeps the work.
 */
function Discard({ onDiscard, onKeep }: { onDiscard: () => void; onKeep: () => void }): ReactNode {
  return (
    <Dialog title="Discard your changes?" onClose={onKeep} layer={KeyLayer.Confirm} showClose={false}>
      <div className="flex flex-col gap-3 text-xs">
        <p className="text-muted">What was typed in this document is lost; nothing was sent.</p>
        <div className="flex gap-2">
          <button type="button" onClick={onDiscard} className={btnDanger}>
            Discard
          </button>
          <button type="button" data-dialog-focus="" onClick={onKeep} className={btnSecondary}>
            Keep editing
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/**
 * "N changed lines", then ↑ `k/N` ↓: the count is of lines, the counter of
 * changes, as on a merge request (the header of `manifestChanges.ts` defines
 * both). Pushed to the right end of the foot row, away from the two buttons
 * that act on the document.
 */
function Changes({
  lines,
  at,
  count,
  onStep,
}: {
  lines: number;
  at: number;
  count: number;
  onStep: (by: 1 | -1) => void;
}): ReactNode {
  const arrow = `inline-flex size-6 items-center justify-center rounded-md border border-line text-muted hover:text-fg pointer-coarse:size-11 ${focusRingTight}`;
  return (
    <div data-manifest-changes="" className="ml-auto flex items-center gap-2 text-xs text-muted">
      <span>
        {String(lines)} changed {lines === 1 ? 'line' : 'lines'}
      </span>
      <button type="button" aria-label="Previous change" title="Previous change" onClick={() => onStep(-1)} className={arrow}>
        ↑
      </button>
      <span aria-label={`Change ${String(at + 1)} of ${String(count)}`}>
        {String(at + 1)}/{String(count)}
      </span>
      <button type="button" aria-label="Next change" title="Next change" onClick={() => onStep(1)} className={arrow}>
        ↓
      </button>
    </div>
  );
}

/**
 * The reveal, and what it costs, said before the click.
 *
 * One control for all the keys rather than one per line: the editor is a
 * text editor and has no rows to hang a control on, and a user who opened the
 * editor has already decided to edit. The cost is still stated - one request
 * per key, each on the record - because a reveal is an act with a subject and
 * the user is the subject.
 *
 * After the reveal the sentence changes to what it means for the write: a value
 * left as it came back leaves as its mask. That is the one thing about this
 * editor a user cannot see in the document, and it decides what the
 * confirmation will list.
 */
function Reveal({
  count,
  revealed,
  pending,
  refused,
  onReveal,
}: {
  count: number;
  revealed: number;
  pending: boolean;
  refused: readonly string[];
  onReveal: () => void;
}): ReactNode {
  const values = (n: number): string => (n === 1 ? 'value' : 'values');
  const done = revealed > 0 || refused.length > 0;
  return (
    <div className="flex flex-col gap-1 border-b border-line pb-2 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        {!done && (
          <>
            <button
              type="button"
              onClick={onReveal}
              disabled={pending}
              className={btnSecondary}
            >
              {pending ? 'Revealing…' : `Reveal ${String(count)} ${values(count)}`}
            </button>
            <span role="status" className="text-muted">
              {String(count)} {values(count)} {count === 1 ? 'is' : 'are'} masked. Revealing reads
              each key through the same door as Show on the Data tab - one request per key, each
              one on the record. To change a value without revealing, type the new base64 over
              its mask.
            </span>
          </>
        )}
        {done && (
          <span role="status" className="text-muted">
            {String(revealed)} {values(revealed)} revealed. A value you leave as it came back is
            sent as its mask, so only what you change reaches the cluster.
          </span>
        )}
      </div>
      {refused.map((problem) => (
        <p key={problem} role="alert" className="text-bad">
          {problem} - its mask stays; type a new base64 over it to change it.
        </p>
      ))}
    </div>
  );
}

/**
 * What the user agrees to.
 *
 * Four things, each answering a question the others do not: the diff lines, the fields the merge patch
 * touches, the precondition the write carries, and the exact request behind a
 * `<details>`. A diff of a reindented block is large and touches nothing, which
 * is why the field list is not decoration; and a confirmation showing a request
 * that is not the one being sent is worse than showing none.
 */
function Confirmation({
  title,
  plan,
  pending,
  error,
  onCancel,
  onApply,
}: {
  title: string;
  plan: ConfirmationPlan;
  pending: boolean;
  error: string | undefined;
  onCancel: () => void;
  onApply: () => void;
}): ReactNode {
  const facts: PlanFacts = plan.facts;

  return (
    <Dialog
      title={title}
      onClose={onCancel}
      layer={KeyLayer.Confirm}
      showClose={false}
    >
      <div className={`flex max-h-[70vh] min-w-0 flex-col gap-3 overflow-auto text-xs ${focusRoom}`}>
        {/* When the server composed a sentence the client renders it and does
            not compose its own: two authors for one sentence is worse than
            either author alone. `facts` stays the authority either way. */}
        {plan.sentence !== undefined && plan.sentence !== null && (
          <p className="text-fg">{plan.sentence}</p>
        )}

        {(facts.warnings ?? []).map((warning) => (
          <p key={warning} role="alert" className="text-warn">
            {warning}
          </p>
        ))}

        {facts.diff !== undefined && facts.diff !== '' && (
          <pre className="overflow-auto rounded border border-line bg-panel p-2 font-mono leading-5">
            <code>
              {facts.diff.split('\n').map((line, at) => (
                <div key={at} className={diffTone(line)}>
                  {line === '' ? ' ' : line}
                </div>
              ))}
            </code>
          </pre>
        )}

        {(facts.fields ?? []).length > 0 && (
          <p className="text-muted">
            Fields:{' '}
            <span className="font-mono text-fg">{(facts.fields ?? []).join(', ')}</span>
          </p>
        )}

        {facts.request !== undefined && facts.request !== '' && (
          <details>
            <summary className={`cursor-pointer text-muted ${focusRing}`}>
              The exact request
            </summary>
            <pre className="mt-1 overflow-auto rounded border border-line bg-panel p-2 font-mono leading-5 text-fg">
              <code>{facts.request}</code>
            </pre>
          </details>
        )}

        {error !== undefined && (
          <p role="alert" className="whitespace-pre-wrap wrap-anywhere text-bad">
            {error}
          </p>
        )}

        {/* The act first and the way back after it, as in every dialog's foot. */}
        <div className="flex gap-2">
          <button
            type="button"
            onClick={onApply}
            disabled={pending}
            className={btnPrimary}
          >
            {pending ? 'Applying…' : 'Apply'}
          </button>
          {/* Not "Cancel": the editor behind this dialog has a Cancel of its
              own and they do not do the same thing - one abandons the edit, this
              one returns to it with everything typed still there. Two controls
              with one name on screen at once is a control a user has to
              guess at. */}
          <button
            type="button"
            onClick={onCancel}
            className={btnSecondary}
          >
            Back
          </button>
        </div>
      </div>
    </Dialog>
  );
}

function diffTone(line: string): string {
  if (line.startsWith('+')) return 'text-ok';
  if (line.startsWith('-')) return 'text-bad';
  if (line.startsWith('@')) return 'text-accent';
  return 'text-muted';
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : 'the write was refused';
}
