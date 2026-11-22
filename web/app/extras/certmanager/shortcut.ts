import type { ExtensionShortcutTarget, ExtensionTabProps } from '@kubetower/ui';

import { certificateHref, daysWord, getCells, getObject } from './data';

/**
 * The tile at the top of a Certificate's, a Secret's or an Ingress's Details:
 * the soonest certificate behind it and its days left, leading to the page on
 * that certificate. `null` - no tile - when there is none.
 */
export async function certificateShortcut(
  { cluster, kind, namespace, name }: ExtensionTabProps,
  signal: AbortSignal,
): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  const answer = await getObject(cluster, kind, namespace, name, signal);
  const c = answer?.certificates[0];
  if (c === undefined) return null;
  return {
    title: c.name,
    badges: [daysWord(c), c.ready === 'True' ? 'Ready' : 'Not ready'],
    detail: c.notAfter === '' ? 'never issued' : `not after ${c.notAfter.slice(0, 10)}`,
    to: certificateHref(cluster, c),
  };
}

/** The column's cells for one list kind, from one read. */
export function certificateCells(list: { cluster: string; kind: string }, signal: AbortSignal) {
  return getCells(list.cluster, list.kind, signal);
}
