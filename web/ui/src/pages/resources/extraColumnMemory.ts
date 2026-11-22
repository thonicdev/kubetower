import { useSyncExternalStore } from 'react';

/**
 * Which of the columns the extras add to the lists a person chose to show, kept
 * in their own browser.
 *
 * Every such column is **hidden until it is ticked** in Settings > Extra, under
 * its extra, and the choice is kept the way the console keeps column choices
 * where it keeps none of its own (`columnMemory.ts`): per browser,
 * no request. A key is `<extension>/<column>`, one choice for every cluster and
 * every list the column is offered on. Only `true` is stored; an unticked box
 * leaves the memory as it was found.
 *
 * Storage may be absent (a private window, blocked site data): every read and
 * write is wrapped, and the choice then lasts as long as the tab.
 */
const KEY = 'kubetower.extracolumns';

type Shown = Readonly<Record<string, true>>;

let snapshot: Shown | undefined;
const listeners = new Set<() => void>();

function read(): Shown {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(KEY) ?? '{}');
    if (typeof parsed !== 'object' || parsed === null) return {};
    return Object.fromEntries(Object.entries(parsed).filter(([, value]) => value === true)) as Shown;
  } catch {
    return {};
  }
}

function current(): Shown {
  snapshot ??= read();
  return snapshot;
}

const keyOf = (extension: string, column: string): string => `${extension}/${column}`;

/** Whether the column is shown in this browser. */
export function extraColumnShown(extension: string, column: string): boolean {
  return current()[keyOf(extension, column)] === true;
}

/** Shows or hides one extra's column in this browser, and redraws what reads it. */
export function setExtraColumnShown(extension: string, column: string, shown: boolean): void {
  const next = { ...read(), ...current() } as Record<string, true>;
  if (shown) next[keyOf(extension, column)] = true;
  else delete next[keyOf(extension, column)];
  snapshot = next;
  try {
    if (Object.keys(next).length === 0) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    // Not stored: the choice lasts as long as this tab.
  }
  for (const listener of [...listeners]) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** The chosen columns, redrawn when one is ticked. */
export function useExtraColumnsShown(): (extension: string, column: string) => boolean {
  const shown = useSyncExternalStore(subscribe, current);
  return (extension, column) => shown[keyOf(extension, column)] === true;
}

/** Forgets what this tab holds, and what storage does. Tests only. */
export function resetExtraColumnMemory(): void {
  snapshot = undefined;
  try {
    localStorage.removeItem(KEY);
  } catch {
    // Nothing stored to forget.
  }
  for (const listener of [...listeners]) listener();
}
