import type { ReactNode } from 'react';
import { Pill, type TileTone } from '@kubetower/ui';

/**
 * Argo CD's and Flux CD's words, graded once for every surface of this extra.
 *
 * The words are the engines' own, read as words (`spoken`: `OutOfSync` is drawn
 * "Out of sync"), and the grade is the console's: `ok`, `warn`,
 * `bad`, and the neutral pill for a word that is not graded. The chip is the
 * core's `Pill` (`ui/src/components/Pill.tsx`), the console's one status
 * vocabulary; a pill of this extra's own, from a stylesheet of its own, would
 * be a second vocabulary in one console.
 *
 * A word missing from a table is drawn neutral rather than colored by a
 * guess: a color invented for an unknown word is a claim nobody made.
 */

export type Tone = 'ok' | 'warn' | 'bad' | 'normal' | 'muted';

/**
 * The tile's tone for Argo CD's two words, as Argo CD's own UI colors them
 * (one Application, one tile, as in Argo CD). `info` is a
 * thing in progress; `off` is a word that says nothing either way.
 */
export const SYNC_TONE: Readonly<Record<string, TileTone>> = {
  Synced: 'ok',
  OutOfSync: 'warn',
  Unknown: 'off',
};

export const HEALTH_TONE: Readonly<Record<string, TileTone>> = {
  Healthy: 'ok',
  Progressing: 'info',
  Degraded: 'bad',
  Missing: 'bad',
  Suspended: 'warn',
  Unknown: 'warn',
};

/** A tile tone as a chip's: `Pill` grades three, and draws the rest neutral. */
const CHIP: Readonly<Record<TileTone, Tone>> = { ok: 'ok', warn: 'warn', bad: 'bad', info: 'normal', off: 'muted' };

const chips = (tones: Readonly<Record<string, TileTone>>): Readonly<Record<string, Tone>> =>
  Object.fromEntries(Object.entries(tones).map(([word, tone]) => [word, CHIP[tone]]));

/** `status.sync.status`, and a resource's own sync word: the chip's grade,
 *  derived from the tile's so the two cannot disagree. */
export const SYNC: Readonly<Record<string, Tone>> = chips(SYNC_TONE);

/** `status.health.status`, the same way. */
export const HEALTH: Readonly<Record<string, Tone>> = chips(HEALTH_TONE);

/** A word's tile tone. A word Argo CD adds later, or none at all, is `off`:
 *  a color invented for an unknown word is a claim nobody made. */
export function tileToneOf(word: string, tones: Readonly<Record<string, TileTone>>): TileTone {
  return tones[word] ?? 'off';
}

/** Worst first: what the board sorts by and what a tile's bar shows. */
const SEVERITY: Readonly<Record<TileTone, number>> = { bad: 4, warn: 3, off: 2, info: 1, ok: 0 };

export function severity(tone: TileTone): number {
  return SEVERITY[tone];
}

/** The worst of several tones. */
export function worst(tones: readonly TileTone[]): TileTone {
  return tones.reduce<TileTone>((a, b) => (SEVERITY[b] > SEVERITY[a] ? b : a), 'ok');
}

/** Flux's `Ready` condition. `True` is not rewritten as Healthy, and `Unknown`
 *  is Flux saying it is still deciding. */
export const READY: Readonly<Record<string, Tone>> = {
  True: 'ok',
  False: 'bad',
  Unknown: 'muted',
};

/** Ours, not Argo's: the field is absent. Distinct from Argo's `Unknown`, which
 *  is a state Argo assessed and could not name; this one has not been assessed. */
export const NOT_REPORTED = 'not reported';

/** Full class strings, looked up and never built by interpolation: Tailwind
 *  finds utilities by scanning source text. */
export const TEXT: Readonly<Record<Tone, string>> = {
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
  normal: 'text-fg',
  muted: 'text-muted',
};

/** An engine's CamelCase word as plain words: `OutOfSync` is "Out of sync". */
export function spoken(word: string): string {
  const parts = word.replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(' ');
  return parts.map((p, i) => (i === 0 ? p : p.toLowerCase())).join(' ');
}

export function toneOf(word: string, grades: Readonly<Record<string, Tone>>): Tone {
  return grades[word] ?? 'normal';
}

/** One engine word, in the console's chip. An absent word is ours to say. */
export function Word({
  word,
  grades,
  title,
}: {
  word: string | undefined;
  grades: Readonly<Record<string, Tone>>;
  title?: string | undefined;
}): ReactNode {
  if (word === undefined || word === '') {
    return (
      <Pill tone="unreported" neutral="muted" title={title ?? 'Not reported yet.'}>
        {NOT_REPORTED}
      </Pill>
    );
  }
  const tone = toneOf(word, grades);
  return (
    <Pill tone={tone} neutral={tone === 'muted' ? 'muted' : 'normal'} title={title}>
      {spoken(word)}
    </Pill>
  );
}
