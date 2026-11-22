import { Suspense, type ComponentType, type ReactNode } from 'react';

import { useChangesPanel, type ObjectEvent } from '../../api';
import type { ExtensionTabProps } from '../../extension';
import { ValueView } from '../../state/Result';
import { Pending, StateNotice } from '../../state/StateNotice';
import { ChangeRow, changesQuiet, ChangesNotices, RolloutRow, type ChangeEntry, type Rollout } from './Changes';
import { EventRow, eventInstant, EventsNote, warning, type EventsPlace } from './Events';
import type { ObjectIdentity } from './address';

/**
 * What happened to this object: its events, its changes and - for a workload -
 * its rollouts, in one list.
 *
 * ## Two tabs read side by side were one question
 *
 * The strip offers *Events* (what Kubernetes reported) and *Changes* (who wrote
 * which part of the object), and they are read together to answer *what
 * happened to it* - flipping between two tabs would be needed to put a
 * `BackOff` next to the `kubectl` write that preceded it. So the two are one
 * list, newest first, and each line says which source it is from: an event
 * with its type, `Warning` standing out; a change with who wrote what.
 *
 * Rollouts are lines of it too, placed by time and marked as a rollout, so the `kubectl set image` write, the
 * revision it shipped and the `BackOff` that followed read in order.
 *
 * ## Each source keeps its own state
 *
 * Interleaving the rows does not merge the answers. The events ride with the
 * object and carry their sentence in `eventsNote`; the changes are a read of
 * their own, which can be pending, refused or unreadable while the events are
 * on screen, and the rollouts within it are a second read (the ReplicaSets)
 * that can be refused while the entries read fine. Each failure is said under
 * the list by its own source, and *nothing here* is drawn once, only when every
 * declared source answered that it has nothing - the line between an empty
 * answer and a failed one, kept per source.
 *
 * ## Only what the server declared
 *
 * `events` is `undefined` where the payload declared no events tab, and
 * `changes` is false where it declared no changes tab; the panel then draws the
 * one source it has. With neither, `ObjectDetail` draws no tab at all.
 */
export function Activity({
  identity,
  events,
  changes,
  place,
  view,
  waiting = false,
}: {
  identity: ObjectIdentity;
  events: { rows: readonly ObjectEvent[]; note: string | undefined } | undefined;
  changes: boolean;
  place: EventsPlace;
  /** An extension's view of the events, drawn at the top in place of the
   *  event lines; the changes and rollouts stay. */
  view?: ComponentType<ExtensionTabProps> | undefined;
  /** An extension's view may be offered and not yet known to be on: nothing is
   *  drawn but a wait, so the core's events are not shown and then swapped. */
  waiting?: boolean;
}): ReactNode {
  const query = useChangesPanel(identity.cluster, identity.kind, identity.namespace, identity.name, changes);
  const panel = changes && query.data?.state === 'ok' ? query.data.data : undefined;

  const View = view;
  const lines = interleave(View === undefined ? (events?.rows ?? []) : [], panel?.entries ?? [], panel?.rollouts ?? []);
  const note = View === undefined ? events?.note : undefined;
  const noted = note !== undefined && note !== '';
  // Nothing here, once: every declared source has answered, and none has a line
  // or a sentence of its own to draw.
  const quiet =
    View === undefined &&
    lines.length === 0 && !noted && (!changes || (panel !== undefined && changesQuiet(panel)));

  if (waiting) return <Pending what="its activity" />;

  return (
    <div className="flex flex-col gap-4">
      {View === undefined ? null : (
        <Suspense fallback={<Pending what="its activity" />}>
          <View cluster={identity.cluster} kind={identity.kind} namespace={identity.namespace} name={identity.name} />
        </Suspense>
      )}
      {lines.length === 0 ? null : (
        <ol className="flex flex-col gap-2">
          {lines.map((line, index) =>
            line.source === 'event' ? (
              <li
                // Neither the instant nor the reason is unique - a controller
                // fires the same reason twice in a second - so the index rides
                // with them.
                key={`event-${line.at ?? ''}-${line.event.reason}-${index}`}
                data-activity="event"
                className={`flex flex-col gap-1 border-l-2 py-1 pl-2 ${
                  warning(line.event) ? 'border-warn' : 'border-line'
                }`}
              >
                <div className="flex items-baseline gap-2">
                  <Mark source="event" />
                  {line.event.type === undefined || line.event.type === '' ? null : (
                    <span
                      data-event-type=""
                      className={`text-2xs ${warning(line.event) ? 'font-medium text-warn' : 'text-muted'}`}
                    >
                      {line.event.type}
                    </span>
                  )}
                </div>
                <EventRow event={line.event} place={place} />
              </li>
            ) : line.source === 'rollout' ? (
              <li
                key={`rollout-${line.rollout.revision}-${line.at}`}
                data-activity="rollout"
                className="flex flex-col gap-1 border-l-2 border-ok/40 py-1 pl-2"
              >
                <Mark source="rollout" />
                <RolloutRow rollout={line.rollout} />
              </li>
            ) : (
              <li
                key={`change-${line.at}-${index}`}
                data-activity="change"
                className="flex flex-col gap-1 border-l-2 border-accent/40 py-1 pl-2"
              >
                <Mark source="change" />
                <ChangeRow entry={line.entry} />
              </li>
            ),
          )}
        </ol>
      )}
      {events === undefined || View !== undefined ? null : <EventsNote note={note} empty={lines.length === 0} />}
      {changes ? (
        <ValueView query={query} what="these changes">
          {(answer) => <ChangesNotices panel={answer} quiet={quiet} />}
        </ValueView>
      ) : null}
      {quiet ? <StateNotice state="empty" /> : null}
    </div>
  );
}

/** Which source a line is from, in a word, before anything else on it. */
function Mark({ source }: { source: ActivityLine['source'] }): ReactNode {
  return (
    <span data-activity-mark={source} className="text-2xs tracking-eyebrow text-muted uppercase">
      {source}
    </span>
  );
}

export type ActivityLine =
  | { source: 'event'; at: string | undefined; event: ObjectEvent }
  | { source: 'change'; at: string; entry: ChangeEntry }
  | { source: 'rollout'; at: string; rollout: Rollout };

/**
 * The three lists as one, newest first.
 *
 * Each arrives newest first, so this is a merge by instant. A line with no
 * instant - an event a source stamped with nothing - cannot be placed in time,
 * so it goes last rather than first, and the sort being stable keeps each
 * source's own order wherever two instants tie or are missing.
 */
export function interleave(
  events: readonly ObjectEvent[],
  entries: readonly ChangeEntry[],
  rollouts: readonly Rollout[],
): ActivityLine[] {
  const lines: ActivityLine[] = [
    ...events.map((event) => ({ source: 'event' as const, at: eventInstant(event), event })),
    ...entries.map((entry) => ({ source: 'change' as const, at: entry.at, entry })),
    ...rollouts.map((rollout) => ({ source: 'rollout' as const, at: rollout.at, rollout })),
  ];
  const time = (at: string | undefined): number => {
    const parsed = at === undefined ? Number.NaN : Date.parse(at);
    return Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
  };
  return lines.sort((a, b) => {
    const ta = time(a.at);
    const tb = time(b.at);
    if (ta === tb) return 0;
    return tb > ta ? 1 : -1;
  });
}
