import type { ReactNode } from 'react';
import type { UseQueryResult } from '@tanstack/react-query';

import type { ListResult, ValueResult } from '../api';
import { Failure, Pending, StateNotice } from './StateNotice';

/**
 * The only way a list reaches the screen.
 *
 * A component is handed a `ListResult`, whose `items` exists on two arms and on
 * no other, and `children` is called only on the arm that has something in it.
 * There is no `items ?? []` anywhere in this application because there is nowhere
 * one would compile.
 *
 * **Every other outcome is drawn rather than fallen through.** That is the whole
 * design: the cheapest correct component is a `<ListView>` with a render
 * function, and getting the six states wrong requires deliberately not using
 * it. Retrofitting this after forty lists exist means auditing forty lists,
 * which is the same argument `eslint.config.js` makes about stylesheets and
 * about `fetch`.
 */
export interface ListViewProps<T> {
  query: UseQueryResult<ListResult<T>>;
  /** What is being read, in the operator's words: "clusters", "the menu". It
   *  appears in the pending line and nowhere else. */
  what: string;
  /** Drawn on `empty`. Without one, the standard notice says "Nothing here" -
   *  which is right, and is never what `unreachable` gets.
   *
   *  **`null` means draw nothing, and `undefined` means nothing was said.**
   *  They are not the same instruction, and reading them as one shipped a
   *  banner on every cluster page: the menu passes `null` for the extras list,
   *  because a build with no extras is not a fact worth a paragraph in a
   *  192-pixel column. The test was `empty ?? <StateNotice/>`, and `??` falls
   *  through on `null` - so the sentinel written to suppress the notice
   *  selected it. The type checked, the tests passed, and the console said
   *  "Nothing here" under its own menu for as long as the code existed. */
  empty?: ReactNode;
  /** The rail and the menu have no room for a paragraph. */
  compact?: boolean;
  children: (items: readonly T[]) => ReactNode;
}

export function ListView<T>({ query, what, empty, compact = false, children }: ListViewProps<T>): ReactNode {
  const { data } = query;

  // Nothing to draw yet. A failed *request* is not a state - `StateNotice` and
  // `Failure` are different components for that reason.
  if (data === undefined) {
    if (query.isError) return <Failure error={query.error} onRetry={() => void query.refetch()} />;
    return <Pending what={what} compact={compact} />;
  }

  const body =
    data.state === 'ok' ? (
      children(data.items)
    ) : data.state === 'empty' ? (
      // `=== undefined` and not `??`: see ListViewProps.empty.
      (empty === undefined ? (
        <StateNotice state="empty" reason={data.reason} compact={compact} />
      ) : (
        empty
      ))
    ) : (
      <StateNotice
        state={data.state}
        reason={data.reason}
        onRetry={() => void query.refetch()}
        compact={compact}
      />
    );

  // A refetch that failed over an answer we already have. The answer stays on
  // screen - blanking a rail because one background poll failed is worse than
  // the staleness - and the failure is said rather than swallowed.
  return (
    <>
      {body}
      {query.isError ? <Stale compact={compact} /> : null}
    </>
  );
}

export interface ValueViewProps<T> {
  query: UseQueryResult<ValueResult<T>>;
  what: string;
  compact?: boolean;
  children: (data: T) => ReactNode;
}

/** The same, for a single value. There is no `empty` arm to draw: a thing that
 *  is not there is `absent`. */
export function ValueView<T>({ query, what, compact = false, children }: ValueViewProps<T>): ReactNode {
  const { data } = query;

  if (data === undefined) {
    if (query.isError) return <Failure error={query.error} onRetry={() => void query.refetch()} />;
    return <Pending what={what} compact={compact} />;
  }

  const body =
    data.state === 'ok' ? (
      children(data.data)
    ) : (
      <StateNotice
        state={data.state}
        reason={data.reason}
        onRetry={() => void query.refetch()}
        compact={compact}
      />
    );

  return (
    <>
      {body}
      {query.isError ? <Stale compact={compact} /> : null}
    </>
  );
}

function Stale({ compact }: { compact: boolean }): ReactNode {
  return (
    <p role="status" className={compact ? 'px-2 py-1 text-xs text-warn' : 'px-3 py-1 text-xs text-warn'}>
      Not refreshed
    </p>
  );
}
