import type { ExtensionShortcutTarget } from '@kubetower/ui';

import { fillKnown, getOverview, pageHref } from './data';

/**
 * The Storage Inspector's tile on the Overview: claims, orphans and the
 * fullest, read once for the whole cluster. `status` grades it: red with a
 * claim at 90 % and over, amber at 80 % or with an orphan, green only once
 * a claim was measured - with none measured (no kubelet read, the default
 * with nodes/proxy off, or kubelets reporting no volume stats) and no orphan
 * it carries no grade, and a `fill unread` badge; an `OK` also waits for every
 * mounted claim to be measured, and `N unread` says how many are not.
 */
export async function storageTile(cluster: string, signal: AbortSignal): Promise<ExtensionShortcutTarget> {
  const data = await getOverview(cluster, '', signal);
  const c = data.counts;
  const known = fillKnown(data);
  const status =
    c.bad > 0
      ? { text: `${String(c.bad)} ≥ 90 %`, tone: 'bad' as const }
      : c.warn > 0
        ? { text: `${String(c.warn)} ≥ 80 %`, tone: 'warn' as const }
        : c.orphans > 0
          ? { text: `${String(c.orphans)} orphans`, tone: 'warn' as const }
          : known && c.unread === 0
            ? { text: 'OK', tone: 'ok' as const }
            : undefined;
  const badges = [`${String(c.orphans)} orphans`, `${String(c.volumes)} volumes`];
  if (c.unread > 0) badges.push(`${String(c.unread)} unread`);
  // No claim, nothing to measure: no badge (#182).
  else if (!known && c.claims > 0) badges.push('fill unread');
  if (data.snapshots.state === 'read') badges.push(`${String(c.snapshotted)}/${String(c.claims)} snapshotted`);
  const tile: ExtensionShortcutTarget = {
    title: `${String(c.claims)} claims`,
    badges,
    to: pageHref(cluster, c.bad + c.warn > 0 ? 'full' : c.orphans > 0 ? 'orphans' : 'all'),
  };
  if (status !== undefined) tile.status = status;
  if (data.fill.message !== undefined) tile.detail = data.fill.message;
  return tile;
}
