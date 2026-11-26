import type { ReactNode } from 'react';
import { Failure, NoMetrics, Pill, btnSecondary } from '@kubetower/ui';

import { ORPHAN_HINTS, ORPHAN_WORDS, StorageProblem, VIA_WORDS, bytes, type Fill, type FillSource, type Orphan, type Section, type SnapshotSection } from './data';

/** A level's fill, written out whole so Tailwind finds each class. */
export const LEVEL_FILL = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad' } as const;
export const LEVEL_TEXT = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad' } as const;

/**
 * One claim's fill: a bar graded at 80 and 90 %, with used of capacity beside
 * it. A claim with no figure draws its state, never an empty bar - an empty
 * bar reads as 0 %.
 */
export function FillBar({ fill, wide = false }: { fill: Fill; wide?: boolean }): ReactNode {
  switch (fill.state) {
    case 'unmounted':
      return (
        <span data-fill="unmounted" className="text-xs text-muted" title="No running pod mounts it">
          —
        </span>
      );
    case 'unread':
      return (
        <span data-fill="unread" className="text-xs text-warn" title="Its kubelet gave no figure">
          Unread
        </span>
      );
    case 'shared':
      return (
        <span data-fill="shared" className="flex items-center gap-1.5 text-xs" title="Shared filesystem: the capacity is the whole share">
          <Pill>shared</Pill>
          <span className="tabular-nums">{bytes(fill.usedBytes ?? 0)}</span>
        </span>
      );
  }
  const percent = Math.max(0, Math.min(100, fill.percent ?? 0));
  const level = fill.level ?? 'ok';
  const reading = `${bytes(fill.usedBytes ?? 0)} of ${bytes(fill.capacityBytes ?? 0)}`;
  return (
    <span data-fill="measured" data-level={level} className={`flex min-w-0 items-center gap-2 ${wide ? 'w-full' : ''}`} title={reading}>
      <span
        role="meter"
        aria-label="Used"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(percent)}
        className={`relative h-2 overflow-hidden rounded-full bg-panel-2 ${wide ? 'min-w-12 max-w-64 flex-1' : 'w-20 shrink-0'}`}
      >
        <span className={`absolute inset-y-0 left-0 rounded-full ${LEVEL_FILL[level]}`} style={{ width: `${String(percent)}%` }} />
        <span className="absolute inset-y-0 w-px bg-bg" style={{ left: '80%' }} />
        <span className="absolute inset-y-0 w-px bg-bg" style={{ left: '90%' }} />
      </span>
      <span className={`text-xs tabular-nums ${LEVEL_TEXT[level]}`}>{`${String(Math.round(percent))}%`}</span>
    </span>
  );
}

export function OrphanPill({ orphan }: { orphan: Orphan | undefined }): ReactNode {
  if (orphan === undefined) return null;
  return (
    <span data-orphan={orphan}>
      <Pill tone="warn" title={ORPHAN_HINTS[orphan]}>
        {ORPHAN_WORDS[orphan]}
      </Pill>
    </span>
  );
}

/** What gave the fill figures, as one badge: the metrics backend, the
 *  kubelets, or both (#91). Nothing when nothing gave one. */
export function FillVia({ fill }: { fill: FillSource }): ReactNode {
  const via = fill.via ?? [];
  if (via.length === 0) return null;
  const words = via.map((one) => VIA_WORDS[one] ?? one);
  return (
    <span data-fill-via={via.join(',')}>
      <Pill neutral="muted" title={`Fill read from ${words.join(' and ')}`}>
        {words.join(' + ')}
      </Pill>
    </span>
  );
}

/** Where the fill figures come from, when they are not all there: one line.
 *  None at all reads as the console's quiet No metrics available, the reason
 *  and what grants it on hover. */
export function SourceLine({ fill }: { fill: FillSource }): ReactNode {
  if (fill.state === 'read' || fill.state === 'none' || fill.message === undefined) return null;
  if (fill.state === 'unreadable') {
    const detail = [fill.message, fill.detail].filter((part) => part !== undefined && part !== '').join('\n');
    return (
      <span data-source="unreadable">
        <NoMetrics detail={detail} />
      </span>
    );
  }
  return (
    <p role="status" data-source={fill.state} className="text-xs text-warn" title={fill.detail}>
      {fill.message}
    </p>
  );
}

/** A read beside the claims that did not succeed, as a badge. */
export function SectionBadge({ section }: { section: Section }): ReactNode {
  if (section.state === 'read') return null;
  const tone = section.state === 'absent' ? undefined : section.state === 'refused' ? 'warn' : 'bad';
  return (
    <span data-section={section.state}>
      <Pill tone={tone} title={section.detail}>
        {section.message ?? section.state}
      </Pill>
    </span>
  );
}

/** The snapshot kinds' state: absent, refused and failed are three badges. */
export function SnapshotBadge({ snapshots }: { snapshots: SnapshotSection }): ReactNode {
  return <SectionBadge section={snapshots} />;
}

export function ProblemNotice({ error, onRetry }: { error: unknown; onRetry?: () => void }): ReactNode {
  if (!(error instanceof StorageProblem)) return <Failure error={error} onRetry={onRetry} />;
  return (
    <div role="status" data-problem={error.problem} className="flex flex-col gap-1 rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className={`font-medium ${error.refused ? 'text-warn' : 'text-bad'}`} title={error.detail}>
        {error.refused ? 'Refused' : 'Could not read'}
      </p>
      <p className="text-xs text-muted" title={error.detail}>
        {error.message}
      </p>
      {onRetry === undefined || error.refused ? null : (
        <button type="button" onClick={onRetry} className={`self-start ${btnSecondary}`}>
          Try again
        </button>
      )}
    </div>
  );
}
