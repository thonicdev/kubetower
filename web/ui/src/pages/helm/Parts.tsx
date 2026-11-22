import { useState, type ReactNode } from 'react';

import { Pill } from '../../components/Pill';
import { useNow } from '../../components/useNow';
import type { HelmRelease } from './data';
import { formatInstant, shortDuration } from '../../components/instant';

/**
 * The shapes both Helm pages draw, in one file because two wordings for one fact is how two pages come
 * to describe the same cluster differently.
 */

/**
 * A release's status word.
 *
 * `tone` is the server's grade; nothing here re-buckets Helm's own status word,
 * which `internal/helm` says is never renamed and never re-bucketed - a caller
 * may choose a color for it and nothing else.
 *
 * The chip is `components/Pill.tsx`, so a `failed` release reads as the same
 * class of thing as a `CrashLoopBackOff` pod elsewhere in the console.
 *
 * An unrecognized tone falls to the **neutral** pill: Helm may send status words
 * this console has never seen, and a panel-colored chip would disappear on any
 * surface that is already `bg-panel-2`.
 */
export function StatusPill({ status, tone }: { status: string; tone?: string | undefined }): ReactNode {
  return (
    <Pill tone={tone} neutral="muted">
      {status}
    </Pill>
  );
}

/**
 * The status, and the line that stops it being read as "nothing is running".
 *
 * A failed upgrade leaves revision 41 `failed` and revision 40 `deployed`. A row
 * reporting only "41, failed" invites the conclusion that nothing is running; a
 * row reporting only "40, deployed" would hide the failure. **Both are on the
 * row**, and the note goes immediately under the status it contradicts rather
 * than in a column of its own; the placement is the point.
 *
 * The sentence is the **server's**. Composing it here from `revision` and
 * `deployedRevision` would be a second place the rule has to be got right.
 */
export function ReleaseStatus({ release }: { release: HelmRelease }): ReactNode {
  const tone = statusTone(release.status);
  return (
    <>
      {release.state === 'ok' ? (
        <StatusPill status={release.status ?? 'unknown'} tone={tone} />
      ) : (
        <StatusPill status="could not be read" tone="bad" />
      )}
      {release.note === undefined ? null : (
        <span className="mt-0.5 block text-2xs text-muted">{release.note}</span>
      )}
      {release.reason === undefined ? null : (
        <span className="mt-0.5 block break-words text-2xs text-muted">
          {release.reason.message}
        </span>
      )}
    </>
  );
}

/**
 * A tone for Helm's own status word, on the list page.
 *
 * A **client-side** grade, and it is the one place on these two pages where that
 * is right: `HelmRelease` carries no `tone` in the contract, while `HelmRevision`
 * does, and the reason is that a revision's status is one of a closed set the
 * server has already graded. Adding `tone` to `HelmRelease` would be a contract
 * change to say something the client can read off a word Helm itself promises
 * never to rename.
 *
 * The pending states are amber rather than green: an upgrade that has been
 * pending for an hour is not one that worked, and it is not one that failed.
 */
function statusTone(status: string | undefined): string {
  switch (status) {
    case 'deployed':
      return 'ok';
    case 'failed':
    case 'unknown':
      return 'bad';
    case 'pending-install':
    case 'pending-upgrade':
    case 'pending-rollback':
    case 'uninstalling':
      return 'warn';
    case 'superseded':
    case 'uninstalled':
      return 'muted';
    default:
      return 'muted';
  }
}

/**
 * When, relative, with the instant on hover.
 *
 * Relative in the cell because the question is "recently or not"; the exact
 * instant in the tooltip because the follow-up question is "was that during the
 * 03:00 incident". An absent timestamp draws the em dash and says so on hover
 * rather than leaving a blank to be read as a rendering fault.
 *
 * The age is taken against the shared clock, not against `Date.now()` at
 * render: `helmQuery` does not poll, so a list left open would otherwise say
 * "4m" for the rest of the afternoon - the defect `useNow` was written for.
 *
 * ## The server's word and the browser's clock
 *
 * `HelmRelease.age` is the server's own wording, as `EventEntry.age` is, *"so
 * that two panels drawing the same event cannot spell it two ways"* It is a **snapshot taken at the read**, and
 * this page does not poll, so the two compose like this:
 *
 *   - **At the read, the server's word.** It is the reading the rest of the
 *     console spells its ages with, and it was measured by the clock that read
 *     the release rather than by one that may be minutes off.
 *   - **From the first tick, the browser's own reading** from `updatedAt`. A
 *     snapshot is stale by definition once the clock has moved, and the tick is
 *     what keeps the cell honest between two reads that may be hours apart. It
 *     is spelled on the server's scale - `ago` below mirrors `ShortDuration` -
 *     so that the handover changes the word only when time has passed, never
 *     because two implementations round differently.
 *   - **An instant ahead of this clock keeps the server's word** for as long as
 *     the page is open, with the tooltip saying whose reading it is. The
 *     browser's subtraction there is a clock disagreement, not an age; before
 *     the contract carried `age` the cell draws the instant when the server sent
 *     no word. It is not clamped to `0s`: on a laptop whose clock is behind the
 *     cluster's that would make every release read as deployed this second, in a
 *     column whose point is to tell recent from not.
 *
 * `age` is optional so that a revision row, whose schema carries no age, draws
 * through the same cell and cannot spell the same instant differently from the
 * release above it.
 */
export function Updated({ at, age }: { at: string | undefined; age?: string | undefined }): ReactNode {
  const now = useNow();
  // The clock's reading when this cell was first drawn. `useNow` starts at
  // Date.now() and moves in ticks, so `now === drawnAt` is "no tick yet", which
  // is the whole of what "the server's snapshot is still current" means here.
  const [drawnAt] = useState(now);
  if (at === undefined) {
    return <span title="Helm recorded no time for this">—</span>;
  }
  const when = new Date(at);
  if (Number.isNaN(when.getTime())) return <span title={at}>—</span>;
  const instant = formatInstant(when, { year: true, seconds: true });
  if (when.getTime() > now) {
    if (age !== undefined) {
      return (
        <span
          title={`${instant} — the server's reading at the time of the read; this browser's clock is behind the cluster's`}
        >
          {age}
        </span>
      );
    }
    return <span title="Later than this browser's clock: one of the two is skewed">{instant}</span>;
  }
  if (age !== undefined && now === drawnAt) {
    return <span title={instant}>{age}</span>;
  }
  return <span title={instant}>{ago(when, now)}</span>;
}

/** "4m", "3d" - the **server's** scale, `resources.ShortDuration`, in
 *  `components/instant.shortDuration`, which the pod panel's last termination
 *  uses too. A different scale (rounding, hours up to two days) would re-age a
 *  release the server aged "1d" as "30h" on the first tick. `now` is the caller's clock, never read
 *  here, so that two cells on one row cannot disagree about what time it is. */
function ago(when: Date, now: number): string {
  return shortDuration(now - when.getTime());
}
