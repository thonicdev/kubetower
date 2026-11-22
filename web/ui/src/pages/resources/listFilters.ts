import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router';

import type { Column, ListPage } from '../../api';

/**
 * The filter control's half of the address: `?filter=<column label>:<value>`,
 * repeated.
 *
 * The box is a plain search over every column; a typed grammar such as
 * `node:kubeworker1` would be found by nobody, could not address `Controlled
 * by`, and would make the box do two jobs. Instead a filter is chosen from a
 * control that lists the columns the server marks `filterable`. **Filtering stays server-side**
 * (`internal/resources/match.go`): the parameter is the request, so the address
 * is shareable and the count is the server's.
 *
 * The column is named by its **label** and not its key. A key is the label plus
 * the column's position (`node-10`), so it moves when a column is added before
 * it, and a bookmark, or a link another page makes - the node panel's
 * "Show the pods running here" - would silently stop filtering. A label is what
 * the heading says, and the server matches it case-insensitively.
 *
 * Not `useUrlList`, deliberately: that hook splits a value on commas, because a
 * namespace selection may be typed as one, and a filter's *value* may hold one -
 * a node's status is `Ready,SchedulingDisabled`.
 */
export const FILTER_PARAMETER = 'filter';

/** One filter as the address carries it. */
export interface ChosenFilter {
  /** The column, as written: a label, any case. */
  column: string;
  value: string;
}

/** Reads one `filter` value. Split at the first colon, as the server splits it:
 *  a label never holds one and a value may. */
export function parseChosen(raw: string): ChosenFilter | undefined {
  const at = raw.indexOf(':');
  if (at < 0) return undefined;
  const column = raw.slice(0, at).trim();
  const value = raw.slice(at + 1).trim();
  return column === '' || value === '' ? undefined : { column, value };
}

/** The parameter value for a column and a value. */
export function chosenParameter(column: string, value: string): string {
  return `${column}:${value}`;
}

/** Whether two parameter values are one filter. Case-insensitive on both
 *  halves, which is how the server compares them. */
export function sameChosen(one: string, other: string): boolean {
  const a = parseChosen(one);
  const b = parseChosen(other);
  return (
    a !== undefined &&
    b !== undefined &&
    a.column.toLowerCase() === b.column.toLowerCase() &&
    a.value.toLowerCase() === b.value.toLowerCase()
  );
}

/** The filters with this one flipped. */
export function toggledChosen(chosen: readonly string[], column: string, value: string): string[] {
  const one = chosenParameter(column, value);
  return chosen.some((other) => sameChosen(other, one))
    ? chosen.filter((other) => !sameChosen(other, one))
    : [...chosen, one];
}

/** The filterable column a chosen filter names, by label, or undefined. */
export function columnOf(columns: readonly Column[], chosen: ChosenFilter): Column | undefined {
  const wanted = chosen.column.toLowerCase();
  return columns.find((column) => column.filterable && column.label.toLowerCase() === wanted);
}

/**
 * The one address shape the withdrawn grammar is still read from: a search that
 * is **exactly** `node:<name>` and nothing else.
 *
 * That is what the node panel's "Show the pods running here" link wrote before
 * the filter control existed (`?search=node:<name>`), so it is what bookmarks and
 * pasted links hold.
 * Every other `<label>:<value>` - `status:running`, `node:x nginx`, a pasted
 * taint - is a plain search, whether it was typed into the box or arrived in an
 * address.
 */
const LEGACY_NODE = /^node:(\S+)$/i;

/**
 * The address an answer says this one should be, or undefined when it already
 * is. Two rewrites, both made once the list has answered the request the
 * address describes:
 *
 *  - **The node panel's old link is translated.** A search that is exactly
 *    `node:<name>` becomes the Node filter, on a list that has a filterable
 *    Node column; the address is replaced, so Back does not return to it.
 *    **Nothing else is.** Translating any `<label>:<value>` word of an arriving
 *    address would be wrong, because the address cannot tell a typed one from a
 *    link: a typed `status:running` would find nothing, and the same address
 *    reloaded would become a Status filter. So `status:running` is a search
 *    before and after a reload. What remains is the one shape this rule
 *    exists for: a typed `node:<name>`, searched as text, is the Node filter
 *    after a reload, because its address is byte for byte the old link's.
 *    `translate` is false for what the box has just written, so that text is
 *    not taken out of the box while somebody is typing it.
 *  - **A filter the server did not apply is dropped**: it names a column this
 *    kind cannot be filtered by, so it narrowed nothing, and a chip for it would
 *    say the list is filtered when it is not. `page.filters` is the server's
 *    statement of what it did.
 */
export function rewrittenAddress(
  page: ListPage,
  search: string,
  chosen: readonly string[],
  translate: boolean,
  /** The labels of the columns extensions add, whose filters the console
   *  applies itself: the server never echoes them, and they are not dropped. */
  extensionLabels: readonly string[] = [],
): { search: string; filters: string[] } | undefined {
  const legacy = translate ? LEGACY_NODE.exec(search.trim()) : null;
  const node = legacy?.[1] === undefined ? undefined : columnOf(page.columns, { column: 'node', value: legacy[1] });
  if (legacy?.[1] !== undefined && node !== undefined) {
    const one = chosenParameter(node.label, legacy[1]);
    const filters = chosen.some((other) => sameChosen(other, one)) ? [...chosen] : [...chosen, one];
    return { search: '', filters };
  }

  const echoed = page.filters ?? [];
  const applied = chosen.filter((one) => {
    const parsed = parseChosen(one);
    const column = parsed === undefined ? undefined : columnOf(page.columns, parsed);
    if (parsed !== undefined && extensionLabels.some((label) => label.toLowerCase() === parsed.column.toLowerCase())) {
      return true;
    }
    return (
      parsed !== undefined &&
      column !== undefined &&
      echoed.some(
        (filter) => filter.column === column.key && filter.value.toLowerCase() === parsed.value.toLowerCase(),
      )
    );
  });
  return applied.length === chosen.length ? undefined : { search, filters: applied };
}

/**
 * The `filter` parameter, read and written - and a writer for the box's
 * parameters beside it in **one** navigation: two writes in one handler lose
 * one of the two (`useUrlParameter` says why), and both the translation above
 * and the empty state's *Clear the filter* change the box and the control
 * together. An empty scalar is the parameter removed.
 */
export function useChosenFilters(): {
  chosen: readonly string[];
  setChosen: (next: readonly string[]) => void;
  rewrite: (box: Readonly<Partial<Record<'search' | 'selector', string>>>, next: readonly string[]) => void;
} {
  const [params, setParams] = useSearchParams();
  // Keyed on the content: `useSearchParams` hands back a new object on every
  // render, and a value may hold any character a separator could be.
  const joined = JSON.stringify(params.getAll(FILTER_PARAMETER));
  const chosen = useMemo(() => JSON.parse(joined) as string[], [joined]);

  const rewrite = useCallback(
    (box: Readonly<Partial<Record<'search' | 'selector', string>>>, next: readonly string[]) => {
      setParams(
        (previous) => {
          const updated = new URLSearchParams(previous);
          for (const [name, value] of Object.entries(box)) {
            if (value === '') updated.delete(name);
            else updated.set(name, value);
          }
          updated.delete(FILTER_PARAMETER);
          for (const one of next) if (parseChosen(one) !== undefined) updated.append(FILTER_PARAMETER, one);
          return updated;
        },
        { replace: true },
      );
    },
    [setParams],
  );
  const setChosen = useCallback(
    (next: readonly string[]) => {
      rewrite({}, next);
    },
    [rewrite],
  );
  return { chosen, setChosen, rewrite };
}
