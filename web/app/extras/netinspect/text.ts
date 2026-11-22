/**
 * Shortening for labels that must fit a fixed space: a cut is always shown
 * as an ellipsis, and the caller keeps the whole text for a tooltip.
 */

const ELLIPSIS = '…';

/**
 * `text` in at most `max` characters. A long `key=value` keeps the start of
 * the key and the end of the value around a middle ellipsis; any other long
 * text keeps its start and its end.
 */
export function shortLabel(text: string, max: number): string {
  const chars = [...text];
  if (chars.length <= max) return text;
  if (max <= 1) return ELLIPSIS;
  const room = max - 1;
  const eq = text.indexOf('=');
  // Key start + ellipsis + value end, when the value alone does not fit whole.
  if (eq > 0 && eq < text.length - 1) {
    const value = [...text.slice(eq)]; // starts with "="
    if (value.length < room) {
      const key = [...text.slice(0, eq)];
      return `${key.slice(0, room - value.length).join('')}${ELLIPSIS}${value.join('')}`;
    }
  }
  const tail = Math.floor(room / 2);
  const head = room - tail;
  return `${chars.slice(0, head).join('')}${ELLIPSIS}${tail === 0 ? '' : chars.slice(-tail).join('')}`;
}

/** The first `n` of `items`, and the rest, for a `+N` badge. */
export function firstN<T>(items: readonly T[], n: number): { shown: T[]; rest: T[] } {
  const k = Math.max(0, n);
  return { shown: items.slice(0, k), rest: items.slice(k) };
}

/** The tooltip of a `+N` badge: the rest, one per line. */
export const restTitle = (rest: readonly string[]): string => rest.join('\n');
