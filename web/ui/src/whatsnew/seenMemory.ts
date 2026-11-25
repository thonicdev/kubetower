import { useSyncExternalStore } from 'react';

/**
 * The last release whose What's new (#80) this browser has seen, where the
 * server keeps none (the server profile, several replicas). A version number,
 * which the page already shows; nothing else. Hence the storage exemption in
 * `eslint.config.js`.
 *
 * Storage may be refused (a private window, a policy): then nothing was seen
 * that this page can know of, and a closed banner stays closed until reload.
 */

const KEY = 'kubetower.seenVersion';

/**
 * A release version, MAJOR.MINOR.PATCH[-pre] with no `v`: `internal/seen`'s
 * Pattern, which the desktop store, the API's write and cmd/ktbuild share. A
 * test pins this copy to api/openapi.yaml, and a Go test pins that to the store.
 */
export const RELEASE = /^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$/;

let snapshot: string | undefined;
let read = false;
const listeners = new Set<() => void>();

function load(): string | undefined {
  try {
    const raw = localStorage.getItem(KEY);
    return raw !== null && RELEASE.test(raw) ? raw : undefined;
  } catch {
    return undefined;
  }
}

/** The version this browser saw last, or undefined. */
export function seenHere(): string | undefined {
  if (!read) {
    snapshot = load();
    read = true;
  }
  return snapshot;
}

/** Records the version seen. */
export function rememberSeen(version: string): void {
  if (!RELEASE.test(version)) return;
  snapshot = version;
  read = true;
  try {
    localStorage.setItem(KEY, version);
  } catch {
    // Kept for this page only; see the comment at the top.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** The version this browser saw last, re-read when it changes. */
export function useSeenHere(): string | undefined {
  return useSyncExternalStore(subscribe, seenHere, seenHere);
}

/** Test seam: forgets the cached snapshot and the stored key. */
export function resetSeenMemory(): void {
  snapshot = undefined;
  read = false;
  try {
    localStorage.removeItem(KEY);
  } catch {
    // Nothing kept.
  }
}
