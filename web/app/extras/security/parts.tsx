import type { ReactNode } from 'react';
import { btnSecondary } from '@kubetower/ui';

import { PAGE_ROWS, n, type Counts, type Source, type State } from './data';

/**
 * The pieces both pages draw: a severity's color, the stacked bar, the
 * sources' pills and the pager. Words are labels and short states only; what
 * a thing means is in its `title`.
 */

/** The one tab look of this page: the section tabs and the cluster reports' kinds. */
export const tabLook = (on: boolean): string =>
  `rounded-md px-2 py-0.5 text-xs ${on ? 'bg-panel-2 text-fg ring-1 ring-accent' : 'text-muted hover:bg-panel-2'}`;

export type Level = 'critical' | 'high' | 'medium' | 'low' | 'unknown';
export const LEVELS: readonly Level[] = ['critical', 'high', 'medium', 'low', 'unknown'];

/** Full class strings, never assembled: Tailwind reads them from the source. */
export const SEV: Readonly<Record<Level, { label: string; bar: string; text: string; chip: string }>> = {
  critical: { label: 'Critical', bar: 'bg-bad', text: 'text-bad', chip: 'border-bad/50 text-bad' },
  high: { label: 'High', bar: 'bg-y-tag', text: 'text-y-tag', chip: 'border-y-tag/50 text-y-tag' },
  medium: { label: 'Medium', bar: 'bg-warn', text: 'text-warn', chip: 'border-warn/50 text-warn' },
  low: { label: 'Low', bar: 'bg-muted/60', text: 'text-muted', chip: 'border-line text-muted' },
  unknown: { label: 'Unknown', bar: 'bg-line', text: 'text-muted', chip: 'border-line text-muted' },
};

/** A severity as the reports spell it (`CRITICAL`, `High`), as a level. */
export function levelOf(severity: string): Level {
  const s = severity.toLowerCase();
  return s === 'critical' || s === 'high' || s === 'medium' || s === 'low' ? s : 'unknown';
}

export function hint(c: Counts): string {
  return `${n(c.critical)} critical · ${n(c.high)} high · ${n(c.medium)} medium · ${n(c.low)} low · ${n(c.unknown)} unknown`;
}

/**
 * One stacked bar, critical to low. `scale` is the bar's length against the
 * longest one beside it (0 to 1); the segments split it by count.
 */
export function SevBar({ counts, scale = 1, className = 'h-2' }: { counts: Counts; scale?: number; className?: string }): ReactNode {
  const sum = counts.critical + counts.high + counts.medium + counts.low;
  return (
    <span data-sevbar="" title={hint(counts)} className={`flex min-w-0 flex-1 overflow-hidden rounded-full bg-white/5 ${className}`}>
      {sum === 0 ? null : (
        <span className="flex h-full" style={{ width: `${String(Math.max(2, Math.min(1, scale) * 100))}%` }}>
          {(['critical', 'high', 'medium', 'low'] as const).map((l) =>
            counts[l] === 0 ? null : <span key={l} data-seg={l} className={`h-full ${SEV[l].bar}`} style={{ width: `${String((counts[l] / sum) * 100)}%` }} />,
          )}
        </span>
      )}
    </span>
  );
}

/** A bar's length on a log scale, so one image with thousands does not flatten the rest. */
export function scaleOf(value: number, max: number): number {
  if (max <= 0 || value <= 0) return 0;
  return Math.log1p(value) / Math.log1p(max);
}

/** The four counts, colored, zero muted. */
export function SevCounts({ counts }: { counts: Counts }): ReactNode {
  return (
    <span className="flex gap-2 tabular-nums" title={hint(counts)}>
      {(['critical', 'high', 'medium', 'low'] as const).map((l) => (
        <span key={l} data-count={l} className={`w-8 text-right ${counts[l] === 0 ? 'text-muted/50' : SEV[l].text}`}>
          {n(counts[l])}
        </span>
      ))}
    </span>
  );
}

/** A severity as a small outlined chip. */
export function SevChip({ severity }: { severity: string }): ReactNode {
  const l = levelOf(severity);
  return (
    <span data-sev={l} className={`inline-block rounded-full border px-1.5 text-2xs leading-4 ${SEV[l].chip}`}>
      {SEV[l].label}
    </span>
  );
}

const SOURCE_LABEL: Readonly<Record<string, string>> = {
  vulnerabilityreports: 'Vulnerabilities',
  configauditreports: 'Config',
  exposedsecretreports: 'Secrets',
  'policyreports.wgpolicyk8s.io': 'PolicyReport',
  'clusterpolicyreports.wgpolicyk8s.io': 'ClusterPolicyReport',
  'reports.openreports.io': 'Report',
  'clusterreports.openreports.io': 'ClusterReport',
  'constraints.gatekeeper.sh': 'Gatekeeper',
  'clusterpolicies.kyverno.io': 'ClusterPolicy',
  'policies.kyverno.io': 'Policy',
  'validatingpolicies.policies.kyverno.io': 'ValidatingPolicy',
  'imagevalidatingpolicies.policies.kyverno.io': 'ImageValidatingPolicy',
  'namespacedvalidatingpolicies.policies.kyverno.io': 'NamespacedValidatingPolicy',
  'namespacedimagevalidatingpolicies.policies.kyverno.io': 'NamespacedImageValidatingPolicy',
  clustervulnerabilityreports: 'Components',
  clusterconfigauditreports: 'Cluster config',
  infraassessmentreports: 'Infra',
  clusterinfraassessmentreports: 'Nodes',
  rbacassessmentreports: 'Roles',
  clusterrbacassessmentreports: 'Cluster roles',
  clustercompliancereports: 'Compliance',
  sbomreports: 'SBOM',
  signatures: 'Signatures',
};

/** A report kind's short name. */
export const sourceLabel = (kind: string): string => SOURCE_LABEL[kind] ?? kind;

const STATE_WORD: Readonly<Record<State, string>> = { read: 'read', absent: 'absent', denied: 'denied', unreadable: 'unreadable', off: 'off' };

/** The dot of a source that was read whole: its state is a color, not a word. */
const STATE_DOT: Readonly<Record<State | 'partial', string>> = {
  read: 'bg-ok',
  partial: 'bg-warn',
  absent: 'bg-muted/60',
  denied: 'bg-warn',
  unreadable: 'bg-bad',
  off: 'bg-muted/60',
};

/** The word's color per state; `partial` is a read narrowed to what the person may list. */
const STATE_LOOK: Readonly<Record<State | 'partial', string>> = {
  read: 'text-ok',
  partial: 'text-warn',
  absent: 'text-muted',
  denied: 'text-warn',
  unreadable: 'text-bad',
  off: 'text-muted',
};

export function sourceState(s: Source): State | 'partial' {
  return s.state === 'read' && !s.complete ? 'partial' : s.state;
}

/**
 * The sources as one compact group: a dot and a name each, the state's word
 * only where the read was not whole; the note stays in the title.
 */
export function SourcePills({ sources }: { sources: readonly Source[] }): ReactNode {
  return (
    <ul aria-label="Sources" className="flex flex-wrap items-center gap-x-2.5 gap-y-1 rounded-full border border-line px-2.5 py-0.5" data-sources="">
      {sources.map((s) => {
        const state = sourceState(s);
        // The signatures are an annotation on pods: none found is no verdict, drawn `—`.
        const word = state === 'read' ? '' : state === 'partial' ? 'partial' : s.kind === 'signatures' && state === 'absent' ? '—' : STATE_WORD[state];
        const title = [`${n(s.reports)} ${s.kind === 'signatures' ? 'pods' : 'reports'}`, s.note].filter((x) => x !== undefined && x !== '').join(' · ');
        return (
          <li key={s.kind} data-source={s.kind} data-state={state} title={`${STATE_WORD[s.state]}: ${title}`} className="flex items-center gap-1 text-2xs">
            <span aria-hidden="true" className={`size-1.5 rounded-full ${STATE_DOT[state]}`} />
            <span className="text-fg">{SOURCE_LABEL[s.kind] ?? s.kind}</span>
            {word === '' ? null : <span className={STATE_LOOK[state]}>{word}</span>}
          </li>
        );
      })}
    </ul>
  );
}

/** "1–50 of 5,000" and the two arrows. Nothing when one page holds it all. */
export function Pager({ page, pages, count, onPage }: { page: number; pages: number; count: number; onPage: (page: number) => void }): ReactNode {
  if (pages <= 1) return null;
  const from = page * PAGE_ROWS + 1;
  const to = Math.min(count, (page + 1) * PAGE_ROWS);
  return (
    <nav aria-label="Pages" className="flex items-center gap-2 text-xs" data-pager="">
      <button type="button" className={btnSecondary} disabled={page === 0} onClick={() => onPage(page - 1)} aria-label="Previous page" data-prev="">
        ‹
      </button>
      <span className="tabular-nums text-muted" data-range="">
        {n(from)}–{n(to)} / {n(count)}
      </span>
      <button type="button" className={btnSecondary} disabled={page >= pages - 1} onClick={() => onPage(page + 1)} aria-label="Next page" data-next="">
        ›
      </button>
    </nav>
  );
}

