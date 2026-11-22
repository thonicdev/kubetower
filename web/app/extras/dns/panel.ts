import type { ExtensionShortcutTarget, ExtensionTabObject, ExtensionTabProps } from '@kubetower/ui';

import { KEY, getStats, type Figures, type Stats } from './data';
import { TILE_LABEL, format, millis, percent, stateWord, worstOf } from './model';

/** CoreDNS's pods and its Service carry one of these two labels. */
export function isCoreDNS(object: ExtensionTabObject): boolean {
  return object.labels['k8s-app'] === 'kube-dns' || object.labels['app.kubernetes.io/name'] === 'coredns';
}

/** What the line shows: the page's own formatters and thresholds, over 15 minutes. */
export const LINE_SPAN = '15m';

function word(text: string, hint: string, to: string): ExtensionShortcutTarget {
  return { title: text, status: { text, tone: 'warn', hint }, to };
}

/** The line for one set of figures: "412 q/s", then the rest as the detail, the worst graded figure as the status. */
export function lineFor(cluster: string, figures: Figures | null | undefined, scope: string): ExtensionShortcutTarget {
  const to = `/c/${encodeURIComponent(cluster)}/x/${KEY}`;
  if (figures === null || figures === undefined) return word('No metrics', `Prometheus holds no series for ${scope}`, to);
  const worst = worstOf(figures);
  const out: ExtensionShortcutTarget = {
    title: `${format('qps', figures.qps)} q/s`,
    detail: `NXDOMAIN ${percent(figures.nxdomain)} · SERVFAIL ${percent(figures.servfail)} · p99 ${millis(figures.p99)} · cache ${percent(figures.cacheHit)}`,
    to,
  };
  if (worst.key !== null && worst.tone !== 'none') {
    out.status = { text: TILE_LABEL[worst.key], tone: worst.tone, hint: `${TILE_LABEL[worst.key]} is high over ${LINE_SPAN} (${format(worst.key, worst.key === 'servfail' ? figures.servfail : figures.p99)})` };
  }
  return out;
}

/** The line from one read of `stats`: the Service's is the cluster figure, a pod's is its own. */
export function lineFrom(cluster: string, kind: string, name: string, stats: Stats): ExtensionShortcutTarget {
  const to = `/c/${encodeURIComponent(cluster)}/x/${KEY}`;
  const state = stateWord(stats);
  if (state !== null) return word(state, stats.detail ?? state, to);
  if (kind === 'pods') return lineFor(cluster, stats.pods.find((p) => p.pod === name)?.figures ?? null, 'this pod');
  return lineFor(cluster, stats.now, 'CoreDNS');
}

/** One read when the panel opens (15 minutes, the page's own route); a rejection draws nothing. */
export async function dnsLine({ cluster, kind, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  return lineFrom(cluster, kind, name, await getStats(cluster, LINE_SPAN, signal));
}
