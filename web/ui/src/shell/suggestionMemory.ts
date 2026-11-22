/**
 * Which suggestions a person answered "Not now" to: per
 * cluster and extension, in this browser only. A preference, not a credential -
 * it holds `cluster/extension` pairs the address and the menu already carry, and
 * can reach nothing else - which is why this file is in the storage exemption
 * of `eslint.config.js`.
 *
 * Storage may be refused (a private window, a policy): then nothing was
 * dismissed that this page can know of, and "Not now" still hides the line
 * until the page is reloaded.
 */

const KEY = 'kubetower.suggestions.dismissed';

const entry = (cluster: string, key: string): string => `${cluster}/${key}`;

/** Every pair answered "Not now" in this browser. */
export function dismissedSuggestions(): string[] {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(KEY) ?? '[]');
    return Array.isArray(raw) ? raw.filter((one): one is string => typeof one === 'string') : [];
  } catch {
    return [];
  }
}

/** Whether one cluster's suggestion of one extension was answered "Not now". */
export function isDismissed(list: readonly string[], cluster: string, key: string): boolean {
  return list.includes(entry(cluster, key));
}

/** Records "Not now", and answers the list now kept. */
export function dismissSuggestion(list: readonly string[], cluster: string, key: string): string[] {
  const next = [...list, entry(cluster, key)];
  try {
    localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    // Kept for this page only; see the comment at the top.
  }
  return next;
}
