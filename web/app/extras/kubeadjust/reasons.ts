import type { Row } from './data';

/**
 * A finding's reason as a short label. The engine words each reason as a
 * sentence (and says so with a figure in it); a list of findings reads as a
 * column of labels, with the sentence on hover. A reason nobody here knows
 * keeps the engine's own words, cut at the first dash, so a new engine
 * message is shown, never hidden.
 */
const LABELS: ReadonlyArray<readonly [RegExp, string]> = [
  [/OOMKilled/i, 'OOMKilled'],
  [/^No .* request set/i, 'No request'],
  [/^No .* limit set|no sizeLimit|^No ephemeral-storage limit/i, 'No limit'],
  [/request is .*× its|fraction of what it requests/i, 'Over-requested'],
  [/usage is .*× the request|above its request/i, 'Above request'],
  [/usage at \d+% of limit|PVC .* at \d+%/i, 'Near limit'],
  [/trending to exceed/i, 'Trending up'],
  [/limit is .*× P95/i, 'Loose limit'],
];

export function reasonLabel(row: Pick<Row, 'message'>): string {
  const found = LABELS.find(([pattern]) => pattern.test(row.message));
  if (found !== undefined) return found[1];
  return row.message.split(/ [—-] /)[0] ?? row.message;
}
