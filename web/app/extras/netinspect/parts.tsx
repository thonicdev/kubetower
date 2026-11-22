import type { ReactNode } from 'react';
import { focusRing } from '@kubetower/ui';

import type { CiliumRead, CNI, Flows, Mode, Unevaluated } from './data';
import { firstN, restTitle } from './text';

/**
 * The mode, said before anything else on every view: what is drawn is the
 * rules, never traffic. Its line sample is the graph's own dash, so the badge
 * and the edges read as one thing.
 */
export function ModeBadge({ mode, on = true, onToggle }: { mode: Mode; on?: boolean; onToggle?: () => void }): ReactNode {
  const rules = mode === 'rules';
  const Tag = onToggle === undefined ? 'span' : 'button';
  return (
    <Tag
      {...(onToggle === undefined ? {} : { type: 'button' as const, onClick: onToggle, 'aria-pressed': on })}
      data-mode={mode}
      title={rules ? 'What the NetworkPolicies allow, computed from the objects. Not observed traffic.' : 'Flows the network plugin reported.'}
      className={`inline-flex items-center gap-2 rounded-full border px-2.5 py-0.5 text-xs ${on ? 'border-accent/50 bg-accent/10' : 'border-line opacity-50'} ${focusRing}`}
    >
      <svg viewBox="0 0 24 6" className="h-1.5 w-6" aria-hidden="true">
        <line x1="0" y1="3" x2="24" y2="3" stroke="var(--color-accent)" strokeWidth="2" strokeDasharray={rules ? '5 3' : undefined} />
      </svg>
      <strong className="font-semibold text-accent">{rules ? 'Rules' : 'Observed'}</strong>
    </Tag>
  );
}

/**
 * The observed layer's switch: solid lines, what the network plugin saw. Its label is
 * the state - how many flows, over how long - or the one word that says why
 * there are none, its detail on hover.
 */
export function FlowsBadge({
  flows,
  pending,
  failed,
  on,
  onToggle,
}: {
  flows: Flows | undefined;
  pending: boolean;
  failed: boolean;
  on: boolean;
  onToggle: () => void;
}): ReactNode {
  const word =
    flows === undefined
      ? pending
        ? '…'
        : failed
          ? 'failed'
          : ''
      : flows.state === 'read' || flows.state === 'empty'
        ? `${String(flows.forwarded + flows.dropped)}${flows.truncated ? '+' : ''} · ${String(Math.round(flows.window / 60))} min`
        : flows.state === 'tls' && flows.source === 'calico'
          ? 'TLS failed'
          : FLOW_STATE[flows.state];
  const usable = flows !== undefined && (flows.state === 'read' || flows.state === 'empty');
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={on}
      disabled={!usable}
      data-flows={flows?.state ?? (failed ? 'failed' : 'pending')}
      data-source={flows?.source ?? ''}
      title={flows?.detail ?? (usable ? `Flows ${flows.source === 'calico' ? 'Calico' : 'Hubble'} reported: forwarded (green), dropped (red).` : undefined)}
      className={`inline-flex items-center gap-2 rounded-full border px-2.5 py-0.5 text-xs ${usable && on ? 'border-ok/50 bg-ok/10' : 'border-line'} ${usable ? '' : 'text-muted'} ${focusRing}`}
    >
      <svg viewBox="0 0 24 6" className="h-1.5 w-6" aria-hidden="true">
        <line x1="0" y1="3" x2="24" y2="3" stroke={usable ? 'var(--color-ok)' : 'var(--color-muted)'} strokeWidth="2" />
      </svg>
      <strong className={`font-semibold ${usable ? 'text-ok' : ''}`}>Observed</strong>
      {word === '' ? null : <span className={usable ? 'text-muted' : ''}>{word}</span>}
    </button>
  );
}

const FLOW_STATE: Record<'absent' | 'refused' | 'tls' | 'unreachable', string> = {
  absent: 'no flow source',
  refused: 'not allowed',
  tls: 'TLS only',
  unreachable: 'unreachable',
};

/**
 * What can make the picture wrong, one short line each: a plugin that does
 * not apply NetworkPolicies, policies of another kind not evaluated, a
 * partial read. The plugin's name otherwise, with its detail on hover.
 */
export function Notices({
  complete,
  notes,
  cni,
  unevaluated,
  cilium,
}: {
  complete: boolean;
  notes: readonly string[];
  cni: CNI;
  unevaluated: readonly Unevaluated[];
  cilium?: CiliumRead | undefined;
}): ReactNode {
  const line = 'rounded-md border px-2.5 py-1 text-xs';
  return (
    <div className="flex flex-wrap items-center gap-2" data-notices="">
      {cni.enforces === 'no' ? (
        <span data-cni="no" title={cni.detail} className={`${line} border-bad/45 bg-bad/10 text-bad`}>
          Not enforced: {cni.name}
        </span>
      ) : null}
      {unevaluated.length > 0 ? (
        <span
          data-unevaluated=""
          title={`These apply too; a verdict may be wrong where they speak.\n${restTitle(unevaluated.map((u) => u.kind))}`}
          className={`${line} border-warn/45 bg-warn/10 text-warn`}
        >
          Not evaluated: {firstN(unevaluated.map((u) => u.kind), 3).shown.join(', ')}
          {unevaluated.length > 3 ? <span data-more=""> +{unevaluated.length - 3}</span> : null}
        </span>
      ) : null}
      {cilium !== undefined && cilium.notEvaluated.length > 0 ? (
        <span
          data-cilium-unevaluated=""
          title={`Drawn as not evaluated, never as allow or deny.${cilium.hostPolicies > 0 ? ` ${String(cilium.hostPolicies)} host policies (nodeSelector) not drawn.` : ''}`}
          className={`${line} border-line text-muted`}
        >
          Not evaluated: {cilium.notEvaluated.join(' · ')}
        </span>
      ) : null}
      {!complete ? (
        <span data-partial="" title={notes.join('\n')} className={`${line} border-warn/45 bg-warn/10 text-warn`}>
          Partial read
        </span>
      ) : null}
      {cni.enforces === 'no' ? null : (
        <span data-cni={cni.enforces} title={cni.detail} className="text-2xs text-muted">
          CNI: {cni.name === '' ? 'unknown' : cni.name}
        </span>
      )}
    </div>
  );
}

/** A pod's two isolations, as chips. */
export function IsolationChips({ isolatedIn, isolatedOut, hostNetwork }: { isolatedIn: boolean; isolatedOut: boolean; hostNetwork: boolean }): ReactNode {
  if (hostNetwork) {
    return (
      <span className="rounded-full border border-dotted border-muted px-1.5 text-2xs text-muted" title="Uses its node's network: the plugin decides">
        host network
      </span>
    );
  }
  const chip = (on: boolean, word: string, direction: string) => (
    <span
      data-chip={word}
      data-isolated={on ? '' : undefined}
      title={on ? `Isolated for ${direction}: only what a rule allows` : `Open for ${direction}: no policy selects it`}
      className={`rounded-full border px-1.5 text-2xs whitespace-nowrap ${on ? 'border-ok/60 bg-ok/10 text-ok' : 'border-dashed border-line text-muted'}`}
    >
      {word} {on ? 'isolated' : 'open'}
    </span>
  );
  return (
    <span className="inline-flex flex-wrap gap-1">
      {chip(isolatedIn, 'in', 'ingress')}
      {chip(isolatedOut, 'out', 'egress')}
    </span>
  );
}
