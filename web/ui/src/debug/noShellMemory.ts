import { useSyncExternalStore } from 'react';

/**
 * Which containers were found to have no shell, for the browser session.
 *
 * The image heuristic (`probablyNoShell`) knows distroless, Chainguard and
 * scratch by name; an image built `FROM scratch` under any other name it
 * cannot know. A Shell on such a container fails with `no_shell` (#146), and
 * from then on the drawer offers Debug on it as it would on a distroless one.
 *
 * **Keyed by the image as well as the container**: a pod's image can be
 * changed in place, and the fact is about the image, not the name.
 *
 * In `sessionStorage`, so a reload keeps it and it ends with the session: it is
 * a fact about a running pod, which a day later may not exist. Every read and
 * write is guarded - a browser that refuses storage keeps the fact for the
 * page's life in `fallback`, and the console works without either.
 */

const KEY = 'kubetower.debug.noShell';
/** Old facts go first past this many: a pod a day of rollouts ago is gone. */
const CAP = 200;

export interface NoShellFact {
  readonly cluster: string;
  readonly namespace: string;
  readonly pod: string;
  readonly container: string;
  readonly image: string;
}

/** What storage could not hold, for this page's life. */
const fallback = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;

function idOf(fact: NoShellFact): string {
  return JSON.stringify([fact.cluster, fact.namespace, fact.pod, fact.container, fact.image]);
}

function stored(): string[] {
  try {
    const raw: unknown = JSON.parse(sessionStorage.getItem(KEY) ?? '[]');
    return Array.isArray(raw) ? raw.filter((one): one is string => typeof one === 'string') : [];
  } catch {
    return [];
  }
}

/** Whether a Shell on that container, with that image, found no shell. */
export function knownNoShell(fact: NoShellFact): boolean {
  const id = idOf(fact);
  return fallback.has(id) || stored().includes(id);
}

/** Note that a Shell on that container found no shell. */
export function rememberNoShell(fact: NoShellFact): void {
  if (knownNoShell(fact)) return;
  const id = idOf(fact);
  const next = [...stored(), id].slice(-CAP);
  try {
    sessionStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    fallback.add(id);
  }
  version += 1;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** A number that changes when a fact is learned, so a drawer open beside the
 *  dock draws Debug the moment its Shell tab ends with `no_shell`. */
export function useNoShellFacts(): number {
  return useSyncExternalStore(subscribe, () => version);
}
