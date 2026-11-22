import type { ReactNode } from 'react';

import { reasonWords, type Chain, type ChainLink, type LinkTone } from './data';

/**
 * The issuance chain of one certificate, drawn as a vertical line of steps:
 * request, order, challenges, issuer. Each step has an icon, its kind and
 * name, its phase and its time; a reason is the step's own text, in the tone of
 * the step. A step that could not be shown is drawn as what it is: `refused`
 * (a lock), `failed` (a warning), `none` (a dashed ring) - never the same.
 */

const TONE_TEXT: Readonly<Record<LinkTone, string>> = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', neutral: 'text-muted' };
const TONE_RING: Readonly<Record<LinkTone, string>> = { ok: 'border-ok', warn: 'border-warn', bad: 'border-bad', neutral: 'border-line' };

type Glyph = 'check' | 'cross' | 'clock' | 'lock' | 'alert' | 'none' | 'off';

function glyphOf(l: ChainLink): Glyph {
  switch (l.state) {
    case 'found':
      return l.tone === 'ok' ? 'check' : l.tone === 'bad' ? 'cross' : 'clock';
    case 'refused':
      return 'lock';
    case 'failed':
    case 'cut':
      return 'alert';
    case 'unserved':
      return 'off';
    default:
      return 'none';
  }
}

const PATHS: Readonly<Record<Glyph, ReactNode>> = {
  check: <path d="M4 8.5l2.5 2.5L12 5" />,
  cross: <path d="M5 5l6 6M11 5l-6 6" />,
  clock: <path d="M8 4.5V8l2.5 1.5" />,
  lock: (
    <>
      <rect x="4.5" y="7" width="7" height="5" rx="1" />
      <path d="M6 7V5.5a2 2 0 014 0V7" />
    </>
  ),
  alert: <path d="M8 4.5v4M8 11v.01" />,
  none: <path d="M5 8h6" />,
  off: <path d="M5 11l6-6" />,
};

function Icon({ glyph, tone, dashed }: { glyph: Glyph; tone: LinkTone; dashed: boolean }): ReactNode {
  return (
    <span
      data-glyph={glyph}
      className={`flex size-6 shrink-0 items-center justify-center rounded-full border-2 bg-panel ${dashed ? 'border-dashed' : ''} ${TONE_RING[tone]} ${TONE_TEXT[tone]}`}
    >
      <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        {PATHS[glyph]}
      </svg>
    </span>
  );
}

/** What a step that was not read says, as a word. */
const STATE_WORD: Readonly<Record<string, string>> = {
  none: 'none',
  refused: 'refused',
  failed: 'unreadable',
  unserved: 'not installed',
  cut: 'read cut',
};

function when(iso: string | undefined): string {
  return iso === undefined || iso === '' ? '' : iso.replace('T', ' ').replace(/:\d\dZ$/, ' UTC');
}

/** The tone a step that was not read is drawn in: refused and unreadable are not neutral. */
function toneOf(l: ChainLink): LinkTone {
  return l.state === 'refused' || l.state === 'failed' ? 'warn' : l.tone;
}

function Step({ link, last, stuck }: { link: ChainLink; last: boolean; stuck: boolean }): ReactNode {
  const tone = toneOf(link);
  const read = link.state === 'found';
  const detail = link.reason ?? '';
  const hint = link.message !== undefined && link.message !== '' ? link.message : undefined;
  return (
    <li data-link={link.kind} data-state={link.state} data-tone={tone} data-stuck={stuck ? '' : undefined} className="relative flex gap-2.5 pb-3 last:pb-0">
      {last ? null : <span aria-hidden="true" className="absolute left-3 top-6 bottom-0 w-px bg-line" />}
      <Icon glyph={glyphOf(link)} tone={tone} dashed={!read} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-xs text-muted">{link.kind}</span>
          {link.name === undefined || link.name === '' ? null : <span className="text-sm font-medium text-fg">{link.name}</span>}
          {link.type === undefined || link.type === '' ? null : <span className="rounded border border-line px-1 text-2xs text-muted">{link.type}</span>}
          {link.dnsName === undefined || link.dnsName === '' ? null : <span className="text-xs text-muted">{link.dnsName}</span>}
          <span className={`text-xs font-medium ${TONE_TEXT[tone]}`} data-phase="" title={hint}>
            {read ? (link.phase ?? '') : (STATE_WORD[link.state] ?? link.state)}
          </span>
          <span className="flex-1" />
          <span className="text-2xs text-muted">{when(link.changed ?? link.created)}</span>
        </div>
        {detail === '' ? null : <span className={`break-words text-xs ${TONE_TEXT[tone]}`} data-reason="">{reasonWords(detail)}</span>}
        {(link.conditions ?? []).length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {(link.conditions ?? []).map((c) => (
              <span
                key={c.type}
                title={c.message === undefined || c.message === '' ? c.reason : `${c.reason ?? ''} ${c.message}`.trim()}
                className={`rounded border px-1 text-2xs ${c.status === 'True' ? (c.type === 'Denied' || c.type === 'InvalidRequest' ? 'border-bad text-bad' : 'border-ok text-ok') : 'border-line text-muted'}`}
              >
                {c.type} {c.status === 'True' ? '✓' : c.status === 'False' ? '✗' : '?'}
              </span>
            ))}
          </div>
        ) : null}
      </div>
    </li>
  );
}

export function ChainView({ chain }: { chain: Chain }): ReactNode {
  const steps: ChainLink[] = [chain.request, chain.order, ...chain.challenges, chain.issuer];
  return (
    <div data-chain="" data-stuck={chain.stuck} aria-label="Issuance chain" role="group" className="border-t border-line pt-3">
      <ol className="flex flex-col">
        {steps.map((l, i) => (
          <Step key={`${l.kind}/${l.name ?? ''}/${String(i)}`} link={l} last={i === steps.length - 1} stuck={chain.stuck === l.kind && l.state === 'found'} />
        ))}
      </ol>
      {chain.moreChallenges > 0 ? <span className="text-2xs text-muted">+{String(chain.moreChallenges)} challenges</span> : null}
    </div>
  );
}
