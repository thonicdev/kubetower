import { useEffect, useRef, type ReactNode } from 'react';

import type { ActOutcome } from '../../api';
import { useResourceDetail } from '../../api';
import { Verbs } from './Verbs';

/**
 * The console's verbs on one object, for a page that draws the object its own
 * way - an extra's page about a CronJob, say - and must still offer what the
 * object's own page offers.
 *
 * Without it, an extension's page about one CronJob would draw its history and
 * no verb at all, and a user who reached the CronJob through it would find
 * no Delete. The list is `DetailPage.actions`, read with the query the
 * drawer and a row's menu make, and pressed through the same plan dialog
 * (`Verbs`): an extension composes no verb, no permission and no confirmation.
 * Nothing is drawn until the object is read, nor where it offers no verb.
 *
 * **A page about an object that is gone has nothing left to show**, so
 * `onGone` is called once when the object's read, having answered, answers
 * `absent` - after a Delete from here, or from anywhere else. The page decides
 * where to go; nothing here knows which verb removes things.
 */
export function ObjectVerbs({
  cluster,
  kind,
  namespace,
  name,
  onActed,
  onGone,
}: {
  cluster: string;
  kind: string;
  namespace?: string | undefined;
  name: string;
  /** Called once an act has finished. */
  onActed?: (outcome: ActOutcome, verb: string) => void;
  /** Called once the object, read before, is read as absent: the page leaves it. */
  onGone?: () => void;
}): ReactNode {
  const detail = useResourceDetail(cluster, kind, namespace, name);
  const answer = detail.data;
  // Which object was last read as present. Keyed, because a route component
  // is reused from one object to the next: having read A tells nothing about
  // B, and B read absent first is not B gone.
  const seen = useRef<string | undefined>(undefined);
  const key = JSON.stringify([cluster, kind, namespace ?? '', name]);
  const state = answer?.state;
  useEffect(() => {
    if (state === 'ok') seen.current = key;
    else if (state === 'absent' && seen.current === key) {
      seen.current = undefined;
      onGone?.();
    }
  }, [state, key, onGone]);
  const actions = answer?.state === 'ok' ? (answer.data.actions ?? []) : [];
  if (actions.length === 0) return null;
  return (
    <Verbs
      identity={{ cluster, kind, namespace, name }}
      actions={actions}
      {...(onActed === undefined ? {} : { onActed })}
    />
  );
}
