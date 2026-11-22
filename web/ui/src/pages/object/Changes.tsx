import type { ReactNode } from 'react';

import { type ChangesPanel, type Reason, type ReasonOnlyState, type State } from '../../api';
import { StateNotice } from '../../state/StateNotice';
import { formatInstant } from '../../components/instant';

/**
 * Who last wrote which part of this object - the field managers' record - as the
 * changes half of the Activity panel (`Activity.tsx`).
 *
 * The entries are not a list of their own: each one is a line of Activity,
 * interleaved by time with the object's events (`ChangeRow`), and the rollouts
 * are lines of it too (`RolloutRow`). What stays here is what is not a line: each half's own
 * failure, said under the timeline it could not fill.
 *
 * ## The operation is `/changes`, not a history
 *
 * `apireads.go` answers it from `internal/changes`, and the entries come back
 * as `kubelet Update (status)`: who wrote what, not an archive of the object.
 * `/archive` is the name reserved for the archive, which is deliberately
 * unbuilt: the archive keeps writing and no operation exposes it. An archive
 * entry and a change entry are the same `{at, summary}` row.
 *
 * ## The absences the contract insists are different
 *
 * `entriesState` exists so that *this object records no history*, *nothing has
 * changed* and *the read failed* do not render as one empty list - the contract
 * spends a paragraph on it, and the reason is the founding defect of this
 * project wearing a panel's clothes. So an `entries` array that is empty is
 * drawn through `StateNotice` carrying the state the server gave, and only a
 * genuinely empty `ok` says that nothing happened.
 *
 * **The server must fill `entriesState`**: if no Go field carried it, this
 * branch would see `undefined` on every response and fold to `empty`, drawing
 * "Nothing here" regardless. A fixture supplying the member itself would be
 * asserting its own invention, so `internal/api/changesmembers_test.go`
 * compares what the contract declares against what a real response carries, in
 * both directions.
 *
 * ## The two halves are drawn separately, because the server answers them
 * separately
 *
 * `entries` comes off the object; `rollouts` comes off the ReplicaSets it left
 * behind. A `forbidden` on `replicasets` under tight RBAC is ordinary while the
 * Deployment reads fine, so one notice covering both would report a refusal as a
 * workload that has never shipped, so the panel reads `rolloutsState`.
 */
export function ChangesNotices({ panel, quiet }: { panel: ChangesPanel; quiet: boolean }): ReactNode {
  const entries = panel.entries ?? [];
  const notice = entries.length === 0 ? noticeFor(panel.entriesState) : 'empty';
  return (
    <>
      {/* The entries half's own failure - `absent`, `unreadable`, `forbidden` -
          is said here, under the list it could not fill. An `empty` one is
          not: the Activity panel says *nothing here* once, for both sources. */}
      {notice === 'empty' ? null : <StateNotice state={notice} reason={panel.entriesReason} />}
      <RolloutsNotice
        rollouts={panel.rollouts ?? []}
        state={panel.rolloutsState}
        reason={panel.rolloutsReason}
        quiet={quiet}
      />
    </>
  );
}

/** Whether the changes read has nothing to say at all: no entries and no
 *  rollouts, and neither half's state a failure. The Activity panel's one empty
 *  notice reads it, so a refused rollouts read is never drawn as *nothing here*. */
export function changesQuiet(panel: ChangesPanel): boolean {
  return (
    (panel.entries ?? []).length === 0 &&
    noticeFor(panel.entriesState) === 'empty' &&
    (panel.rollouts ?? []).length === 0 &&
    noticeFor(panel.rolloutsState) === 'empty'
  );
}

/**
 * The state a half draws when it has no list, or `null` where it has one.
 *
 * A state the server declared wins over counting the array: `absent` with no
 * entries and `ok` with no entries are different sentences, and the user acts
 * on them differently.
 *
 * `ok` and `pending` are the two `StateNotice` will not draw, for its own good
 * reason - neither is a *failure* to explain. An `ok` with no entries is what
 * `empty` means, and a `pending` one is a read that has not been asked yet, which
 * is `empty` with nothing more to say at this altitude.
 *
 * `undefined` folds to `empty` **only for the entries half**. On the rollouts
 * half an absent state means the kind has no revisions at all, which is not an
 * emptiness to announce - that arm is handled by its caller, not here.
 */
function noticeFor(state: State | undefined): ReasonOnlyState | 'empty' {
  return state === undefined || state === 'ok' || state === 'pending' ? 'empty' : state;
}

/** One write of the object's own `managedFields`: who, and what. */
export function ChangeRow({ entry }: { entry: ChangeEntry }): ReactNode {
  return (
    <div className="flex flex-wrap items-baseline gap-2 text-xs">
      <span className={`min-w-0 flex-1 ${toneClass(entry.tone)}`}>
        {entry.summary}
        {entry.reason === undefined || entry.reason === '' ? null : (
          <span className="text-muted"> · {entry.reason}</span>
        )}
      </span>
      {entry.count === undefined || entry.count <= 1 ? null : (
        // A repeat count rather than the same line fifty times: the count is
        // the information.
        <span className="shrink-0 text-muted tabular-nums">×{entry.count}</span>
      )}
      <time dateTime={entry.at} className="shrink-0 text-muted tabular-nums">
        {when(entry.at)}
      </time>
    </div>
  );
}

/** One entry of `ChangesPanel.entries`. */
export type ChangeEntry = ChangesPanel['entries'][number];

/** One revision of `ChangesPanel.rollouts`. */
export type Rollout = NonNullable<ChangesPanel['rollouts']>[number];

/**
 * What shipped, and when: one revision, as a line of the Activity timeline.
 *
 * ## A line of the timeline, not a list of its own
 *
 * `entries` answers *who wrote which field*; a rollout answers *which image was
 * running between Tuesday and now*, which is what somebody choosing a rollback
 * is reading. They are distinct, and the rollouts still join the timeline:
 * placed by time beside the events and the writes that caused them,
 * each line marked as a rollout. The line keeps everything the list drew - the
 * revision, the image, the replicas, the instant and the change cause.
 *
 * ## `current` is marked, never inferred from the order
 *
 * The highest revision is not always the deployed one — that is exactly the
 * state a failed rollout leaves behind, and it is the state somebody opens this
 * panel in.
 */
export function RolloutRow({ rollout }: { rollout: Rollout }): ReactNode {
  return (
    <div className="flex flex-wrap items-baseline gap-2 text-xs">
      <span className="shrink-0 tabular-nums">{rollout.revision}</span>
      {rollout.current === true ? (
        <span className="shrink-0 rounded bg-ok/15 px-1.5 py-0.5 text-2xs text-ok">current</span>
      ) : null}
      {/* The column a rollback is chosen from, so it wraps rather than
          truncating: a digest is long and the end of it is the part that
          differs. */}
      {rollout.image === undefined || rollout.image === '' ? null : (
        <span className="min-w-0 flex-1 break-all">{rollout.image}</span>
      )}
      {rollout.replicas === undefined ? null : (
        <span className="shrink-0 text-muted tabular-nums">{rollout.replicas}</span>
      )}
      <time dateTime={rollout.at} className="shrink-0 text-muted tabular-nums">
        {when(rollout.at)}
      </time>
      {rollout.cause === undefined || rollout.cause === '' ? null : (
        // `kubernetes.io/change-cause`, where anybody set it — which is
        // rarely, and is worth drawing when they did.
        <span className="w-full text-xs text-muted">{rollout.cause}</span>
      )}
    </div>
  );
}

/**
 * The rollouts half's own answer, where it has no line to draw.
 *
 * ## An absent `rolloutsState` is the one absence that draws nothing
 *
 * Three answers, and only the first is silence:
 *
 * - **no state at all** — the kind has no revisions. A ConfigMap is not a
 *   workload with an empty history, and a heading over nothing says it lost one.
 * - **`empty`** — it has revisions and has never shipped. Said, not drawn as
 *   silence - unless the whole panel is quiet (`quiet`), where the one
 *   *nothing here* already says it.
 * - **anything else** — the ReplicaSets could not be read. `forbidden` there is
 *   ordinary under tight RBAC while the Deployment itself reads fine, and
 *   rendering it as “never shipped” would confidently report the absence of
 *   its own subject. Said beside the timeline, as a failed changes read is.
 */
function RolloutsNotice({
  rollouts,
  state,
  reason,
  quiet,
}: {
  rollouts: readonly Rollout[];
  state: State | undefined;
  reason: Reason | undefined;
  quiet: boolean;
}): ReactNode {
  if (rollouts.length > 0 || state === undefined) return null;
  const notice = noticeFor(state);
  if (notice === 'empty' && quiet) return null;
  return (
    <section className="flex flex-col gap-1">
      <h3 className="text-xs font-medium text-muted uppercase">Rollouts</h3>
      <StateNotice state={notice} reason={reason} />
    </section>
  );
}

/** Local time, seconds included: two writes a second apart is the ordinary case
 *  in this panel, and a minute-resolution stamp draws them as simultaneous. */
function when(at: string): string {
  return formatInstant(at, { seconds: true });
}

/** The contract's five tones. An unknown one is `normal` rather than nothing:
 *  losing a line to a tone nobody added here would be losing the information to
 *  the decoration. */
function toneClass(tone: string | undefined): string {
  switch (tone) {
    case 'muted':
      return 'text-muted';
    case 'ok':
      return 'text-ok';
    case 'warn':
      return 'text-warn';
    case 'bad':
      return 'text-bad';
    default:
      return '';
  }
}
