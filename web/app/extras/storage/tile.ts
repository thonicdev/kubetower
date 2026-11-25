import type { ExtensionShortcutTarget } from '@kubetower/ui';

import { getOverview, pageHref } from './data';

/**
 * The Storage Inspector's tile on the Overview: claims, orphans and the
 * fullest, read once for the whole cluster. `status` grades it: red with a
 * claim at 90 % and over, amber at 80 % or with an orphan, green only once
 * the fill was read - with no kubelet read (the default, nodes/proxy being
 * off) and no orphan it carries no grade, and a `fill unread` badge.
 */
export async function storageTile(cluster: string, signal: AbortSignal): Promise<ExtensionShortcutTarget> {
  const data = await getOverview(cluster, '', signal);
  const c = data.counts;
  const fillKnown = data.fill.state === 'read' || data.fill.state === 'partial';
  const status =
    c.bad > 0
      ? { text: `${String(c.bad)} ≥ 90 %`, tone: 'bad' as const }
      : c.warn > 0
        ? { text: `${String(c.warn)} ≥ 80 %`, tone: 'warn' as const }
        : c.orphans > 0
          ? { text: `${String(c.orphans)} orphans`, tone: 'warn' as const }
          : fillKnown
            ? { text: 'OK', tone: 'ok' as const }
            : undefined;
  const badges = [`${String(c.orphans)} orphans`, `${String(c.volumes)} volumes`];
  if (!fillKnown) badges.push('fill unread');
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
