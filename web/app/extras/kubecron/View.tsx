import type { UseQueryResult } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Failure, Pending, StateNotice, type ValueResult } from '@kubetower/ui';

/**
 * The core's `ValueView`, with one difference: an object that is not there is
 * titled **Not found**, not *Not installed*.
 *
 * `absent`'s own word is about a cluster missing a component, and it is right
 * for no collector. A CronJob, a Job, a Pod or a run that is gone, or that the
 * person may not read, is not something to install, and drawing it under that
 * title sends the reader looking for an install - for a Job Kubernetes has
 * already deleted, say. `StateNotice`'s `title` exists for exactly
 * this; the state, its tone and `data-state` stay `absent`. `data.ts` tells the
 * two apart by the route's code, carried as the reason's code.
 */
export function View<T>({
  query,
  what,
  children,
}: {
  query: UseQueryResult<ValueResult<T>>;
  what: string;
  children: (data: T) => ReactNode;
}): ReactNode {
  const { data } = query;
  if (data === undefined) {
    if (query.isError) return <Failure error={query.error} onRetry={() => void query.refetch()} />;
    return <Pending what={what} />;
  }
  if (data.state === 'ok') return <>{children(data.data)}</>;
  const title = data.state === 'absent' && data.reason.code !== 'not_installed' ? 'Not found' : undefined;
  return <StateNotice state={data.state} reason={data.reason} title={title} onRetry={() => void query.refetch()} />;
}
