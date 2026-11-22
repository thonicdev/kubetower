import type { ReactNode } from 'react';
import { Link } from 'react-router';

import type { ObjectEvent } from '../../api';
import { Message } from '../../components/FoldedMessage';
import { formatInstant } from '../../components/instant';
import { focusRing } from '../../focus';
import { relatedPath, type ObjectIdentity } from './address';

/** Where the object on screen lives, so a row about an object it owns can link
 *  to that object. Absent, such a row names it without a link. */
export interface EventsPlace {
  readonly cluster: string;
  readonly namespace?: string | undefined;
  readonly chrome: 'drawer' | 'page';
}

/**
 * What the cluster still remembers happening to this object: one event, as a
 * line of the Activity panel (`Activity.tsx`).
 *
 * Events and *Changes* are read together to answer the same question - *what
 * happened to it* - so they are one list; what follows is why the event half
 * looks the way it does.
 *
 * ## The panel a user opens first when something is wrong
 *
 * It is the one that says *why*: the scheduler's reason for not placing a
 * pod, the kubelet's image-pull error, an admission webhook's refusal.
 *
 * ## It costs nothing, which is why it has no operation
 *
 * `internal/detail` fetches these with the object. So the rows arrive in
 * `DetailPage.events`, its `Tab` carries no `endpoint` - the contract's own
 * *absent means the tab is inline* - and opening this panel makes no request at
 * all.
 *
 * ## Empty is a sentence here, and it is rarely the obvious one
 *
 * **Kubernetes drops events after about an hour.** So an empty list nearly
 * always means *nothing recently*, and drawn as a bare empty panel it reads as
 * *nothing ever* - which is the founding collapse of this project in the one
 * place a user is looking for a cause. `eventsNote` is the server's
 * sentence for that, and it is drawn whether or not there are rows: a list can
 * be short **and** capped.
 *
 * ## On a controller, the rows of what it owns too
 *
 * A Deployment's reason is usually on its ReplicaSet or its pods, so the server
 * sends their events with the Deployment's, each carrying
 * `about`: the owned object it is about. That row names the object - a link to
 * it, as a relation is - so a pod's `BackOff` under a Deployment is not read
 * as the Deployment's own. One empty state covers the whole family; it is the
 * server's sentence like every other.
 */
export function EventRow({
  event,
  place,
}: {
  event: ObjectEvent;
  place?: EventsPlace | undefined;
}): ReactNode {
  return (
    <>
      <div className="flex flex-wrap items-baseline gap-2 text-xs">
        <span
          className={`rounded px-1.5 py-0.5 text-2xs ${
            warning(event) ? 'bg-warn/15 text-warn' : 'bg-panel-2 text-muted'
          }`}
        >
          {event.reason}
        </span>
        {about(event, place)}
        {event.from === undefined || event.from === '' ? null : (
          <span className="text-muted">{event.from}</span>
        )}
        {event.count === undefined || event.count <= 1 ? null : (
          // A BackOff that has fired 340 times and one that fired once are
          // the same row without this, and the first is an incident.
          <span className="text-muted tabular-nums">×{event.count}</span>
        )}
        <span className="flex-1" />
        {span(event)}
      </div>
      {/* Never clipped and always allowed to wrap: the message is the
          entire content of an event, it runs to several hundred
          characters, and the answer is at the end as often as at the
          start. **One that runs to several lines is pasted output** - a
          mount's stderr, a hook's log - and folds like a container's
          termination message, so one event cannot
          push every other off the panel. */}
      <Message
        message={event.message}
        label={warning(event) ? 'show error' : 'show message'}
        inline={(text) => <p className="text-xs break-words">{text}</p>}
      />
    </>
  );
}

/**
 * What the event list says when it has nothing, or about what it has.
 *
 * **One empty state.** The same member carries "Events could not be read: …",
 * which a "No events" title would call empty - an empty answer and a failed
 * answer drawn alike - so a note on
 * an empty list is drawn alone, as the notice. The payload says nothing else
 * about which case it is - there is no state beside `eventsNote` - and reading
 * the sentence to decide would be the client re-deciding from the server's
 * wording. Beside rows, the note is a plain sentence: a list can be short
 * **and** capped. `null` where there is no note at all; the caller decides
 * whether the whole panel is empty.
 */
export function EventsNote({ note, empty }: { note: string | undefined; empty: boolean }): ReactNode {
  if (note === undefined || note === '') return null;
  if (!empty) return <p className="text-xs text-muted">{note}</p>;
  return (
    <p
      role="status"
      data-state="empty"
      data-events-note=""
      className="rounded-lg border border-line bg-panel-2 p-3 text-sm text-muted"
    >
      {note}
    </p>
  );
}

/** When an event last happened, as the instant the Activity list orders by:
 *  `last`, else `first`, else nothing. */
export function eventInstant(event: ObjectEvent): string | undefined {
  if (event.last !== undefined && event.last !== '') return event.last;
  if (event.first !== undefined && event.first !== '') return event.first;
  return undefined;
}

/** `Warning` is the only type worth coloring, and it is matched case-blind:
 *  Kubernetes defines `Normal` and `Warning`, a controller can write anything,
 *  and anything else is drawn plainly rather than dropped. */
export function warning(event: ObjectEvent): boolean {
  return (event.type ?? '').toLowerCase() === 'warning';
}

/**
 * Which owned object a row is about, or nothing on the object's own events.
 *
 * A link where the panel knows where it is, to the same destinations a relation
 * uses: the object's list in the drawer, its own page on a page. The owned
 * object is in the namespace of the one on screen - a controller owns nothing
 * outside it - so that namespace is the one the link carries.
 */
function about(event: ObjectEvent, place: EventsPlace | undefined): ReactNode {
  const target = event.about;
  if (target === undefined) return null;
  const label = `${target.title} ${target.name}`;
  if (place === undefined || target.kind === '' || target.name === '') {
    return (
      <span data-event-about="" className="font-mono text-muted">
        {label}
      </span>
    );
  }
  const identity: ObjectIdentity =
    place.namespace === undefined || place.namespace === ''
      ? { cluster: place.cluster, kind: target.kind, name: target.name }
      : { cluster: place.cluster, kind: target.kind, namespace: place.namespace, name: target.name };
  return (
    <Link
      data-event-about=""
      to={relatedPath(identity, place.chrome)}
      className={`font-mono text-muted hover:text-accent ${focusRing}`}
    >
      {label}
    </Link>
  );
}

/**
 * When it happened, and over what span.
 *
 * *since 09:12, still firing at 11:48* is a different sentence from *at 11:48*,
 * and a series event that has been going for three hours is the case this panel
 * exists to make visible. Both instants are optional: a source that gave none
 * gets no stamp rather than `0001-01-01`.
 */
function span(event: ObjectEvent): ReactNode {
  const last = event.last;
  if (last === undefined || last === '') return null;
  const first = event.first;
  const repeated = first !== undefined && first !== '' && first !== last;
  return (
    <time dateTime={last} className="text-muted tabular-nums">
      {repeated ? `${when(first)} → ${when(last)}` : when(last)}
    </time>
  );
}

/** Local time, seconds included: two events a second apart is the ordinary case
 *  in this panel, and a minute-resolution stamp draws them as simultaneous. */
function when(at: string): string {
  return formatInstant(at, { seconds: true });
}
