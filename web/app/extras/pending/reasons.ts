import type { Reason } from './data';

/**
 * The reasons in the order a row leads with: what the pod asks for that no
 * node can give (resources, node selector, node affinity, pod affinity,
 * spread, volumes) first, anything else after, and the control-plane taint
 * last - it explains one node, usually the same one for every pod, and says
 * nothing about this pod. The scheduler's own order is kept inside a rank.
 */
const LEAD = ['resources', 'nodeAffinity', 'podAffinity', 'spread', 'volumes'];

function rank(r: Reason): number {
  const lead = LEAD.indexOf(r.category);
  if (lead >= 0) return lead;
  if (r.category === 'taints' && /node-role\.kubernetes\.io\/(control-plane|master)/.test(r.text)) return LEAD.length + 1;
  return LEAD.length;
}

export function orderedReasons(reasons: readonly Reason[] | null | undefined): Reason[] {
  return (reasons ?? [])
    .map((r, i) => ({ r, i }))
    .sort((a, b) => rank(a.r) - rank(b.r) || a.i - b.i)
    .map(({ r }) => r);
}

export function leadReason(reasons: readonly Reason[] | null | undefined): Reason | undefined {
  return orderedReasons(reasons)[0];
}
