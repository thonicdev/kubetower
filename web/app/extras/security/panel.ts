import { ApiError, NotJsonError, request, type ExtensionCell, type ExtensionShortcutTarget, type ExtensionTabProps } from '@kubetower/ui';

import { cellsPath, getBrief, n, workloadHref, type Brief, type Counts } from './data';

/**
 * What Security Center adds to the console's own panels and lists, each from
 * one route read: the tab's probe and the shortcut ask `brief` (the object
 * alone), and a list's column asks `cells` once for the whole list.
 */

/** A 404 is the console's gate (plain text) or the extra's own: no tab. */
export function gated(error: unknown): boolean {
  return error instanceof NotJsonError || (error instanceof ApiError && error.status === 404);
}

/**
 * Whether the object gets the Security tab: only when a vulnerability report
 * covers it. A read that failed for another reason draws the tab, which then
 * says it could not look - hiding it would make "could not check" read as
 * "nothing found".
 */
export async function securityProbe({ cluster, kind, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<boolean> {
  if (namespace === undefined || namespace === '') return false;
  try {
    return (await getBrief(cluster, kind, namespace, name, signal)).scanned;
  } catch (error) {
    if (signal.aborted || gated(error)) return false;
    return true;
  }
}

/** "3 critical · 12 high"; the worst level found below them when neither;
 *  "clean" when the report found nothing. */
export function countsTitle(c: Counts): string {
  const severe = (['critical', 'high'] as const).filter((l) => c[l] > 0).map((l) => `${n(c[l])} ${l}`);
  if (severe.length > 0) return severe.join(' · ');
  const lesser = (['medium', 'low', 'unknown'] as const).find((l) => c[l] > 0);
  return lesser === undefined ? 'clean' : `${n(c[lesser])} ${lesser}`;
}

/** Where the brief leads: its own address when it is one of the console's,
 *  else the workload's page. */
export function briefHref(cluster: string, brief: Brief): string | undefined {
  if (brief.href !== undefined && brief.href.startsWith('/c/')) return brief.href;
  return brief.workload === undefined ? undefined : workloadHref(cluster, brief.workload);
}

/** The tile at the top of Details: the counts, the image, the workload's page.
 *  Nothing on an object no report covers. */
export async function securityShortcut({ cluster, kind, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  const brief = await getBrief(cluster, kind, namespace, name, signal);
  const to = briefHref(cluster, brief);
  if (!brief.scanned || brief.counts === undefined || to === undefined) return null;
  // The admission's verdict, where a pod of the workload carries one.
  const verdict = brief.signature?.verdict;
  return {
    title: countsTitle(brief.counts),
    badges: verdict === 'pass' ? ['Trivy', '✓'] : verdict === 'fail' ? ['Trivy', '✗'] : ['Trivy'],
    ...(brief.image === undefined || brief.image === '' ? {} : { detail: brief.image }),
    to,
  };
}

/** A list's column: one read for the whole list, its cells as the route draws them. */
export function securityCells(list: { cluster: string; kind: string }, signal: AbortSignal): Promise<Readonly<Record<string, ExtensionCell>>> {
  return request<Record<string, ExtensionCell>>(cellsPath(list.cluster, list.kind), { signal });
}
