import { useCallback, useMemo, useRef, useState, type ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import {
  applyData,
  invalidateActedObjects,
  previewData,
  revealDataValue,
  useDataPanel,
  type ConfirmationPlan,
  type DataEntry,
  type DataPanel,
} from '../../api';
import { CopyButton } from '../../components/CopyButton';
import { Dialog } from '../../components/Dialog';
import { FindBar, FindButton, Marked, type Hit } from '../../components/FindBar';
import { Icon } from '../../components/Icon';
import { findRanges, useFind, type Range } from '../../components/useFind';
import { btnPrimary, btnSecondary } from '../../components/buttons';
import { focusRing, focusRoom } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import { ValueView } from '../../state/Result';
import { StateNotice } from '../../state/StateNotice';

/**
 * A ConfigMap's or a Secret's own keys.
 *
 * ## Where it is drawn
 *
 * **Inside the details panel, between the object's own facts and Related** -
 * never a tab of its own: one page per object, with the details, the data and
 * Related at the bottom, as for a pod. `ObjectDetail.tsx`'s `Body` mounts it,
 * gated on `DetailPage.data` - the server's address, which is also the server's
 * statement that this kind has a block at all.
 *
 * Where the panel is mounted does not touch how a masked value is obtained:
 * the one door is `/env` (`revealDataValue`), not `/data`.
 *
 * **Opening a Secret directly shows its keys, a reveal and an edit.** The
 * reveal also exists for a *container's environment* (`Containers.tsx`), so a
 * value is reachable through the pod that mounts it, and it must be reachable
 * through the object that holds it too.
 *
 * ## One door for a masked value, and it is not this file
 *
 * `revealEnvValue` is the door, addressed at the **source object** - here, this
 * object. Nothing in this panel may obtain a value another way: the point of one
 * door is that the audit line in a shared profile is complete, and a second path
 * would be a second thing to remember to log.
 *
 * An `ok` carrying the empty string is a **revealed empty value**, which is a
 * real and confusing thing to meet in a Secret. Reading it as "nothing came
 * back" is the collapse this contract exists to remove, on the one payload where
 * being wrong hides or exposes a credential - so the envelope's state decides,
 * never the string's length.
 *
 * ## A revealed value is whole, and Edit goes through the same door
 *
 * *Show* on a Secret does not cut a long value or offer a second click to
 * *Expand* it: every value is drawn whole, a ConfigMap's inline value included,
 * and a long one scrolls inside its own block.
 * `entry.masked` says which control a row offers (Show), never what is drawn.
 *
 * *Edit* cannot open the textarea on `entry.value`, which a masked key does not
 * carry - a user who had not pressed *Show* first would edit a blank box. So
 * the edit control on an unrevealed masked key **is** a reveal: the same
 * `revealDataValue`, one round trip, one audit line, and the textarea opens on
 * the answer. A refused reveal refuses the edit with the reason the server gave;
 * there is no box to edit a value nobody may see.
 *
 * ## And the way back exists
 *
 * Once shown, a value must not stay on screen until the drawer closes: a user
 * who checked a password and then shared their screen needs a control to
 * press. *Hide* beside a revealed value clears it and asks nothing -
 * the audit line was written on *Show* and that is the record; pressing *Show*
 * again is a new reveal and a new line. The row's state is also that object's
 * only: the list keys each row on the object's address (see `Block`), so a
 * value revealed on one Secret is never drawn under another's key.
 *
 * The revealed values themselves are held one level up, by `DataView`, since
 * the search below has to know which values are on screen to count what it
 * finds in them - and `DataView` is keyed on the object's address the same way,
 * so moving the drawer to another Secret drops every value revealed on this
 * one.
 *
 * ## A search over the keys, and over the values that are on screen
 *
 * Ctrl+F (⌘F) while the object's panel has the
 * keyboard, or the search control beside the heading, opens a find bar over
 * this block: matches marked in place, `n/m`, Enter and Shift+Enter to move,
 * Escape to close. `useFind` holds the bar; **this file decides what is
 * searched, and the rule is the one this panel already draws by**:
 *
 * - every key, always;
 * - a ConfigMap's inline value, drawn whole, scrolling in its own block;
 * - a Secret's value **only once somebody pressed Show on it**, and only until
 *   *Hide*. A masked value is not in this client until the door has answered,
 *   so there is nothing to search, and the search never knocks: it does not
 *   reveal, it does not fetch, and a masked row matches on its key alone. The
 *   rule is written on `entry.masked` and the revealed value, never on whether
 *   a value happens to be present, so a server that one day sent a masked
 *   value inline would still not have it searched.
 *
 * ## Editing is one key at a time, and it is preview-then-apply
 *
 * The manifest editor's shape: `previewData` validates and
 * returns a plan that touches nothing, and only the dialog that plan draws posts
 * its **token**. `resourceVersion` rides on the write as a precondition, so a
 * save somebody else got to first is refused rather than silently winning.
 *
 * `editable: false` disables the controls and draws `editableNote` beside them -
 * the contract asks for that by name, because the causes are different
 * people's: this object is immutable, this cluster is guarded, or this
 * console's ServiceAccount may not patch it. `editable: false` with no note is
 * a person who may not patch the object, and no Edit is drawn at all.
 */
export function Data({
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
  // Keyed on the object, so that everything below - the revealed values above
  // all, and what the search had found - belongs to one object and goes with
  // it when the drawer is moved to another.
  return (
    <DataView
      key={`${cluster}/${kind}/${namespace ?? ''}/${name}`}
      cluster={cluster}
      kind={kind}
      namespace={namespace}
      name={name}
    />
  );
}

function DataView({
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
  const query = useDataPanel(cluster, kind, namespace, name, true);
  // The values revealed through the door, by key. Here rather than in each row
  // because the search counts in them; see the header.
  const [revealed, setRevealed] = useState<ReadonlyMap<string, string>>(() => new Map());
  const onRevealed = useCallback((key: string, value: string | undefined) => {
    setRevealed((was) => {
      const next = new Map(was);
      if (value === undefined) next.delete(key);
      else next.set(key, value);
      return next;
    });
  }, []);

  // The search reads the payload outside `ValueView` for the count, which the
  // bar needs whichever row the current match is in. It is the same object
  // `ValueView` hands its `ok` arm.
  const entries = useMemo(
    () => (query.data?.state === 'ok' ? query.data.data.entries ?? [] : []),
    [query.data],
  );
  const view = useRef<HTMLElement>(null);
  const find = useFind({
    view,
    enabled: entries.length > 0,
    // The object's whole panel: this block is one section of the Details tab,
    // and Ctrl+F from its facts or its tab strip is a search of its keys.
    region: '[data-object-chrome]',
  });
  const { found, capped } = useMemo(
    () => matchEntries(entries, revealed, find.pattern),
    [entries, revealed, find.pattern],
  );
  const hits = markCurrent(found, find.current(countOf(found)));

  return (
    <section ref={view} data-section="data">
      {/* **The heading is not decoration here, it is what makes an empty answer
          readable.** In the middle of a details page this is one block among
          several, and an unattributed notice between the
          annotations and Related says nothing about *what* is empty. The same
          `<h3>` Related uses, so the two read as siblings.

          It sits outside `ValueView` on purpose: the loading, empty and failed
          renders each need it just as much as the list does - more, since those
          are exactly the states with no other clue to what they are about. */}
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-medium text-muted uppercase">Data</h3>
        <FindBar
          find={find}
          count={countOf(found)}
          label="Find in this data"
          more={capped ? `the data marks its first ${String(BLOCK_MARKS)} matches only` : undefined}
        />
        {/* Only where there are keys: a search over an empty ConfigMap, or
            over a block still loading, is a control with nothing behind it. */}
        {entries.length === 0 ? null : (
          <span className="ml-auto">
            <FindButton find={find} what="this data" />
          </span>
        )}
      </div>
      <ValueView query={query} what="this data">
        {(panel) => (
          <Block
            cluster={cluster}
            namespace={namespace}
            panel={panel}
            revealed={revealed}
            onRevealed={onRevealed}
            hits={hits}
          />
        )}
      </ValueView>
    </section>
  );
}

function Block({
  cluster,
  namespace,
  panel,
  revealed,
  onRevealed,
  hits,
}: {
  cluster: string;
  namespace: string | undefined;
  panel: DataPanel;
  revealed: ReadonlyMap<string, string>;
  onRevealed: (key: string, value: string | undefined) => void;
  /** The search's matches, by key; a key with none is absent. */
  hits: ReadonlyMap<string, EntryHits<Hit>>;
}): ReactNode {
  const entries = panel.entries ?? [];
  const editable = panel.editable === true;
  // Drawn, enabled or disabled with its note; absent where the person may not
  // write this object, which the server says by refusing with no note.
  const editOffered = editable || panel.editableNote !== undefined;

  if (entries.length === 0) {
    // An object with no keys is a real and ordinary thing - an empty ConfigMap -
    // and it is not a failure. `StateNotice` says which.
    return <StateNotice state="empty" />;
  }

  return (
    <div className="flex flex-col gap-2">
      {editable || panel.editableNote === undefined ? null : (
        <p className="rounded-md border border-line bg-panel px-2 py-1 text-xs text-muted">
          {panel.editableNote}
        </p>
      )}

      <ul className="flex flex-col gap-2">
        {entries.map((entry) => (
          <Entry
            // The object's address and not the key alone. `ObjectDetail` is the
            // same component at the same position when the drawer is re-targeted
            // or the page moves to a neighbor, and a panel already in the cache
            // arrives without passing through *Pending* - so a row keyed on
            // `password` alone would keep the previous Secret's revealed value,
            // and its draft, under the next Secret's key of the same name.
            key={`${cluster}/${panel.kind}/${panel.namespace ?? ''}/${panel.name}/${entry.key}`}
            cluster={cluster}
            namespace={namespace}
            panel={panel}
            entry={entry}
            editable={editable}
            editOffered={editOffered}
            editNote={panel.editableNote}
            revealed={revealed.get(entry.key)}
            onRevealed={(value) => onRevealed(entry.key, value)}
            hits={hits.get(entry.key) ?? NO_HITS}
          />
        ))}
      </ul>
    </div>
  );
}

function Entry({
  cluster,
  namespace,
  panel,
  entry,
  editable,
  editOffered,
  editNote,
  revealed,
  onRevealed,
  hits,
}: {
  cluster: string;
  namespace: string | undefined;
  panel: DataPanel;
  entry: DataEntry;
  editable: boolean;
  /** Whether Edit is drawn at all. */
  editOffered: boolean;
  /** Why Edit is disabled, when it is. */
  editNote: string | undefined;
  /** What the door answered for this key, until Hide. */
  revealed: string | undefined;
  onRevealed: (value: string | undefined) => void;
  hits: EntryHits<Hit>;
}): ReactNode {
  const client = useQueryClient();
  const [refused, setRefused] = useState<string | null>(null);
  const [wrap, setWrap] = useState(true);
  const [draft, setDraft] = useState<string | undefined>(undefined);
  const [plan, setPlan] = useState<ConfirmationPlan | undefined>(undefined);
  const [failure, setFailure] = useState<string | null>(null);

  // What the reveal is for rides with the request rather than in a second piece
  // of state: the answer decides what opens, and only the answer. `'edit'` is
  // the edit control on a masked key nobody has revealed - one round trip and
  // one audit line, the same as *Show*, with the textarea opened on what came
  // back. A refusal is drawn the same way for both and opens nothing.
  const reveal = useMutation({
    mutationFn: (_then: 'show' | 'edit') =>
      revealDataValue(cluster, panel.kind, namespace, panel.name, entry.key),
    onSuccess: (answer, then) => {
      if (answer.state === 'ok') {
        onRevealed(answer.data.value);
        setRefused(null);
        if (then === 'edit') setDraft(answer.data.value);
        return;
      }
      setRefused(answer.reason.message);
    },
    onError: (error: Error) => setRefused(error.message),
  });

  const review = useMutation({
    mutationFn: (value: string) =>
      previewData(cluster, {
        kind: panel.kind,
        ...(panel.namespace === undefined ? {} : { namespace: panel.namespace }),
        name: panel.name,
        resourceVersion: panel.resourceVersion,
        // `delete` is the other thing a write can say, and it is false here
        // because this panel edits a key rather than removing one. Removing a
        // key is a separate gesture that this panel does not offer.
        entries: [{ key: entry.key, value, delete: false }],
      }),
    onSuccess: (made) => {
      setPlan(made);
      setFailure(null);
    },
    onError: (error: Error) => setFailure(error.message),
  });

  const commit = useMutation({
    mutationFn: (token: string) => applyData(cluster, token),
    onSuccess: () => {
      setPlan(undefined);
      setDraft(undefined);
      onRevealed(undefined);
      // Every read of this object, not only this panel's.
      //
      // Not this panel's alone: the Manifest tab - the other place to check
      // what was just done - would go on drawing the document from before the
      // save. Neither read carries an interval, so the two tabs would disagree
      // and the wrong one would look authoritative, a manifest reading as *the
      // object as the cluster holds it*.
      //
      // This panel's own re-read is still the load-bearing one for a different
      // reason: it carries the `resourceVersion` every later write is checked
      // against, so a stale one here makes the *next* save fail for a reason
      // nobody can see.
      void invalidateActedObjects(client, cluster, panel.kind, [
        { kind: panel.kind, ...(panel.namespace === undefined ? {} : { namespace: panel.namespace }), name: panel.name },
      ]);
    },
    onError: (error: Error) => {
      setPlan(undefined);
      setFailure(error.message);
    },
  });

  // What is on screen for this key: the revealed value, the inline one, or
  // neither. `masked` decides which control is offered; it never decides what is
  // shown, because a revealed value is shown whether or not the row was masked.
  const shown = revealed ?? entry.value;
  const editing = draft !== undefined;

  return (
    <li className="rounded-md border border-line">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-2 py-1">
        <span className="font-mono text-xs text-fg">
          <Marked text={entry.key} hits={hits.key} />
        </span>
        {entry.binary === true ? (
          // Binary is not a value a textarea can hold, and saying so is better
          // than drawing mojibake or an empty box.
          <span className="rounded border border-line px-1 text-2xs text-muted">binary</span>
        ) : null}
        {/* The badge says the row's state now: masked while the value is held
            back, revealed while it is on screen, and masked again on Hide. */}
        {entry.masked ? (
          <span data-mask={shown === undefined ? 'masked' : 'revealed'} className="rounded border border-line px-1 text-2xs text-muted">
            {shown === undefined ? 'masked' : 'revealed'}
          </span>
        ) : null}

        <span className="flex-1" />

        {entry.masked && shown === undefined && entry.binary !== true ? (
          <button
            type="button"
            onClick={() => reveal.mutate('show')}
            disabled={reveal.isPending}
            className={btnSecondary}
          >
            {reveal.isPending ? 'Showing…' : 'Show'}
          </button>
        ) : null}

        {revealed !== undefined && !editing ? (
          <button
            type="button"
            onClick={() => {
              onRevealed(undefined);
            }}
            className={btnSecondary}
          >
            Hide
          </button>
        ) : null}

        {shown !== undefined && shown !== '' ? (
          <CopyButton value={shown} label={`Copy ${entry.key}`} />
        ) : null}

        {shown !== undefined && shown.includes('\n') ? (
          <button
            type="button"
            aria-pressed={wrap}
            onClick={() => setWrap((was) => !was)}
            className={btnSecondary}
          >
            Wrap
          </button>
        ) : null}

        {entry.binary === true || !editOffered ? null : (
          <button
            type="button"
            // Disabled rather than absent, so that an operator looking for the
            // control finds it and the note above says which setting refuses.
            disabled={!editable || editing || reveal.isPending}
            onClick={() => {
              // A masked key nobody has revealed: the edit is a reveal first,
              // and the textarea opens on the answer or not at all.
              if (entry.masked && shown === undefined) {
                reveal.mutate('edit');
                return;
              }
              setDraft(shown ?? '');
            }}
            title={editable ? `Edit ${entry.key}` : (editNote ?? 'This object cannot be edited')}
            className={btnSecondary}
          >
            <Icon name="edit" />
            Edit
          </button>
        )}
      </div>

      <div className="px-2 py-1">
        {editing ? (
          <div className="flex flex-col gap-2">
            <textarea
              aria-label={`${entry.key} value`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              rows={Math.min(Math.max(draft.split('\n').length, 3), 20)}
              className={`w-full rounded border border-line bg-bg p-2 font-mono text-xs ${focusRing}`}
            />
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => review.mutate(draft)}
                disabled={review.isPending}
                className={btnPrimary}
              >
                {review.isPending ? 'Checking…' : 'Save…'}
              </button>
              <button
                type="button"
                onClick={() => {
                  setDraft(undefined);
                  setFailure(null);
                }}
                className={btnSecondary}
              >
                Cancel
              </button>
            </div>
          </div>
        ) : shown === undefined ? (
          // And these two keep no box. Three things can stand where a
          // value stands: a value, *binary, not shown*, and *held back until you
          // ask for it* - and the last two are this console talking, not the
          // object. A uniform surface over all three would say they are the same
          // kind of thing, which is exactly the collapse the first rule of this
          // panel exists to prevent: an absent answer is not an empty one. So
          // prose stays muted prose, a value wears the surface, and a revealed
          // Secret value wears it too - it is as literal as any other.
          //
          // *Held back* is said on `entry.masked` and on nothing else: a fallback
          // for every absence is how a sentence ends up said about a case nobody
          // chose it for. The contract names the three absences and the panel
          // reads them off it rather than assuming the last one:
          //
          //   `binary`  the stored value is not text
          //   `masked`  a Secret's key, and the door is the Show control above
          //   neither   a ConfigMap's `binaryData` - base64 in the object, which
          //             this panel does not decode
          //
          // The third has a sentence of its own: borrowing the second's would
          // tell the user a ConfigMap is withholding something. That is a
          // security affordance spent on a case with nothing to protect, which
          // teaches the wrong thing about what masking means on the one surface
          // where masking is the point.
          <p className="text-xs text-muted">
            {entry.binary === true
              ? 'Binary, not shown.'
              : entry.masked
                ? 'Held back until you ask for it.'
                : 'Base64 in the object. This panel does not decode it.'}
          </p>
        ) : (
          // A value wears the surface this console already uses for literal
          // content - `bg-panel-2`, the same block `HealthParts` draws a command
          // in and `Certificates` draws a serial in. Without it the key and the
          // value would measure identically (same color, size and family, over
          // the same transparent row), and nothing would say which is the name
          // and which is the content.
          //
          // The accent is deliberately not borrowed for it: `border-accent`
          // already means *this is the one you are on* at five controls in this
          // console, and a value is never selected - it is literal. A surface
          // costs no color vocabulary at all.
          //
          // The value is never clipped. `max-h-[60vh]` bounds the block and
          // `overflow-auto` (ahead of the padding) makes it scroll inside itself:
          // a long script scrolls down, and an unwrapped long line scrolls
          // across, rather than bursting the box.
          <pre
            className={`max-h-[60vh] overflow-auto rounded-sm bg-panel-2 px-2 py-1 font-mono text-xs ${
              wrap ? 'whitespace-pre-wrap wrap-anywhere' : ''
            }`}
          >
            {shown === '' ? (
              // A `<pre>` holding the empty string renders as
              // nothing at all: an 8px sliver of the surface and no text, which
              // is a value that is empty and a panel that failed to draw wearing
              // one face again - the collapse this row exists to prevent,
              // arrived at from the far end. So the emptiness is *said*.
              //
              // Inside the box rather than beside it, because it is a value and
              // the box is what says so. Muted and italic is what keeps it from
              // reading as a value whose text is the word `empty`.
              <code className="text-muted italic">empty</code>
            ) : (
              <code>
                <Marked text={shown} hits={hits.value} />
              </code>
            )}
          </pre>
        )}

        {refused === null ? null : (
          <p role="alert" className="text-xs text-bad">
            {refused}
          </p>
        )}
        {failure === null ? null : (
          <p role="alert" className="text-xs text-bad">
            {failure}
          </p>
        )}
      </div>

      {plan === undefined ? null : (
        <Confirm
          plan={plan}
          entryKey={entry.key}
          pending={commit.isPending}
          onCancel={() => setPlan(undefined)}
          onApply={() => commit.mutate(plan.token)}
        />
      )}
    </li>
  );
}

/**
 * The confirmation, from the plan and never composed here.
 *
 * The server writes the sentence where it has one, because two authors for one
 * sentence is worse than either alone; `facts` stays the authority. The
 * precondition is not drawn out in words here or in the manifest editor.
 */
function Confirm({
  plan,
  entryKey,
  pending,
  onCancel,
  onApply,
}: {
  plan: ConfirmationPlan;
  entryKey: string;
  pending: boolean;
  onCancel: () => void;
  onApply: () => void;
}): ReactNode {
  const facts = plan.facts;

  return (
    // `Confirm` and not the `Dialog` default: this is a confirmation, and
    // `keyboard/layers.ts` puts a confirmation above a dialog so that answering
    // one does not dismiss whatever raised it. Nothing raises this from inside
    // a dialog today - the data panel is a tab in the object view - and the
    // layer says what the thing is rather than where it currently happens to be
    // opened from.
    <Dialog title={`Save ${entryKey}?`} onClose={onCancel} layer={KeyLayer.Confirm}>
      <div className={`flex max-h-[70vh] min-w-0 flex-col gap-3 overflow-auto text-xs ${focusRoom}`}>
        {plan.sentence === undefined || plan.sentence === null ? null : (
          <p className="text-fg">{plan.sentence}</p>
        )}

        {(facts.warnings ?? []).map((warning) => (
          <p key={warning} role="alert" className="text-warn">
            {warning}
          </p>
        ))}

        {facts.destructive === true ? (
          <p className="text-warn">
            This replaces the stored value. What was there is not kept anywhere by this
            console.
          </p>
        ) : null}

        <div className="flex gap-2">
          <button
            type="button"
            onClick={onApply}
            disabled={pending}
            className={btnPrimary}
          >
            {pending ? 'Saving…' : 'Save'}
          </button>
          <button
            type="button"
            onClick={onCancel}
            className={btnSecondary}
          >
            Cancel
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/** What the search found in one row: in its key, and in its value. */
interface EntryHits<T extends Range> {
  readonly key: readonly T[];
  readonly value: readonly T[];
}

/** A row with no matches, shared, so an unmatched row is handed the same
 *  empty lists on every render. */
const NO_HITS: EntryHits<Hit> = { key: [], value: [] };

/**
 * How many hits the whole data block marks, at most.
 *
 * A value is drawn whole, and every hit is a `<mark>`: a
 * value of a megabyte searched for one character would be a million nodes, and
 * the tab would stop answering while React drew them. A cap per value is not
 * enough - a hundred values of a thousand hits each are a hundred thousand
 * marks - so the budget is the block's, spent in reading order: keys and values
 * alike, row after row, until it runs out, and nothing after that is marked.
 * A thousand is more than anybody steps through, and a few milliseconds to
 * draw. The counter says `n/1000+` when the block went past it, and the stepper
 * goes round the hits that are marked - all of them, and only them.
 */
const BLOCK_MARKS = 1000;

/**
 * Every match in the block, by key, in reading order: each row's key, then its
 * value, row after row - at most `BLOCK_MARKS` of them, and whether the block
 * held more.
 *
 * **What is searched is decided here and only here**, and the header says
 * why: a masked row's value is searched only when the door has answered for
 * it, and then the revealed value is what is searched - `entry.value` is never
 * read for a masked row. A binary key carries no value to search.
 */
function matchEntries(
  entries: readonly DataEntry[],
  revealed: ReadonlyMap<string, string>,
  pattern: RegExp | null,
): { found: ReadonlyMap<string, EntryHits<Range>>; capped: boolean } {
  const found = new Map<string, EntryHits<Range>>();
  let capped = false;
  if (pattern === null) return { found, capped };
  let used = 0;
  // What is left of the budget, plus one: the one past it says there were more
  // without walking them. Once it is spent, nothing further is searched.
  const take = (text: string | undefined): Range[] => {
    if (capped || text === undefined) return [];
    const left = BLOCK_MARKS - used;
    const ranges = findRanges(text, pattern, left + 1);
    if (ranges.length > left) {
      capped = true;
      ranges.length = left;
    }
    used += ranges.length;
    return ranges;
  };
  for (const entry of entries) {
    if (capped) break;
    const value = entry.masked ? revealed.get(entry.key) : entry.binary === true ? undefined : entry.value;
    const key = take(entry.key);
    const inValue = take(value);
    if (key.length > 0 || inValue.length > 0) found.set(entry.key, { key, value: inValue });
  }
  return { found, capped };
}

function countOf(found: ReadonlyMap<string, EntryHits<Range>>): number {
  let count = 0;
  for (const one of found.values()) count += one.key.length + one.value.length;
  return count;
}

/** The same matches, with the one the bar is on marked as current. The order
 *  is the map's, which is the rows' - `matchEntries` filled it row by row. */
function markCurrent(
  found: ReadonlyMap<string, EntryHits<Range>>,
  current: number,
): ReadonlyMap<string, EntryHits<Hit>> {
  const out = new Map<string, EntryHits<Hit>>();
  let index = 0;
  const mark = (range: Range): Hit => ({ ...range, current: index++ === current });
  for (const [key, one] of found) {
    out.set(key, { key: one.key.map(mark), value: one.value.map(mark) });
  }
  return out;
}
