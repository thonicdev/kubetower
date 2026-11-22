import { ApiError, NotJsonError, request, type ExtensionCell, type ExtensionShortcutTarget, type ExtensionTabProps } from '@kubetower/ui';

import { CATEGORY_LABEL, cellsPath, getBrief, getPod, getWorkload, podHref } from './data';
import { leadReason } from './reasons';

/**
 * What Pending adds to the console's own panels and lists, each from one
 * route read: the tab's probe and the shortcut ask `brief` (the pod alone),
 * and the Pods list's column asks `cells` once for the whole list.
 */

/** A 404 that is not JSON is the console's gate: the extra is off here. */
function gated(error: unknown): boolean {
  return error instanceof NotJsonError || (error instanceof ApiError && error.status === 404);
}

/**
 * Whether a pod gets the Pending tab: only while it is Pending. A read that
 * failed for another reason draws the tab, which then says it could not look -
 * hiding it would make "could not check" read as "not pending".
 */
export async function pendingProbe({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<boolean> {
  if (namespace === undefined || namespace === '') return false;
  try {
    return (await getBrief(cluster, namespace, name, signal)).pending;
  } catch (error) {
    if (signal.aborted || gated(error)) return false;
    return true;
  }
}

/**
 * Whether a workload gets the Pending tab: while one of its pods is pending or
 * its controller was refused a pod. As for a pod, a read that failed for
 * another reason draws the tab, which then says it could not look.
 */
export async function workloadProbe({ cluster, kind, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<boolean> {
  if (namespace === undefined || namespace === '') return false;
  try {
    const view = await getWorkload(cluster, kind, namespace, name, signal);
    return view.pending > 0 || view.failure !== undefined;
  } catch (error) {
    if (signal.aborted || gated(error)) return false;
    return true;
  }
}

/**
 * The tile at the top of a pending pod's Details: its main reason, and the
 * node that comes closest. The closest node needs the nodes read, so it is
 * asked only once the pod is known to be pending; a failure there leaves the
 * reason alone on the tile.
 */
export async function pendingShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  const brief = await getBrief(cluster, namespace, name, signal);
  if (!brief.pending || brief.row === undefined) return null;
  const row = brief.row;
  let detail = row.stage === 'starting' ? row.message : (leadReason(row.verdict.reasons)?.text ?? row.message);
  if (row.stage === 'unschedulable') {
    try {
      const closest = (await getPod(cluster, namespace, name, signal)).row?.closest;
      if (closest !== undefined) detail = `closest ${closest.node} · ${closest.failing} failed`;
    } catch {
      // The reason alone: the tile is a shortcut, the page says why it could not look.
    }
  }
  return { title: CATEGORY_LABEL[row.main], badges: ['Pending'], detail, to: podHref(cluster, namespace, name) };
}

/** The Pods list's column: one pill per pending pod, from one read. */
export function pendingCells(list: { cluster: string; kind: string }, signal: AbortSignal): Promise<Readonly<Record<string, ExtensionCell>>> {
  return request<Record<string, ExtensionCell>>(cellsPath(list.cluster, list.kind), { signal });
}
