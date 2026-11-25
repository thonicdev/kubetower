import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { Failure, Pending as Waiting, type ExtensionTabProps } from '@kubetower/ui';

import { DEFAULT_WINDOW, objectQuery, type Window } from './data';
import { NO_FILTERS, narrow, normalCount, shownTypes } from './model';
import { Causes, Correlations, Sources, Timeline, WindowPicker, useHighlight } from './parts';

/**
 * What Event Intelligence says about a Pod, Deployment, StatefulSet, DaemonSet,
 * ReplicaSet or Job, drawn by the core in the object's Activity tab in place of
 * its event lines: the timeline of what changed with the events under it, and
 * the causes, narrowed by the server to the object and its pods. Asked again
 * every 30 s only while it is on screen.
 */
export default function ActivityView({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  const [window, setWindow] = useState<Window>(DEFAULT_WINDOW);
  const query = useQuery({
    ...objectQuery(cluster, kind, namespace ?? '', name, window),
    enabled: namespace !== undefined,
    placeholderData: keepPreviousData,
  });
  const answer = query.data;
  const narrowed = useMemo(() => (answer === undefined ? [] : narrow(answer, NO_FILTERS)), [answer]);
  const [normal, setNormal] = useState(false);
  const causes = useMemo(() => shownTypes(narrowed, NO_FILTERS, normal), [narrowed, normal]);
  const highlight = useHighlight();
  if (namespace === undefined) return null;
  return (
    <div data-activity-view="" className="flex flex-col gap-2" aria-busy={query.isFetching}>
      <div className="flex items-center">
        <WindowPicker window={window} onWindow={setWindow} />
      </div>
      {answer === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Waiting what="its events" />
        )
      ) : (
        <>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          <Sources answer={answer} />
          <Correlations answer={answer} causes={causes} highlight={highlight} compact />
          <Timeline answer={answer} causes={causes} highlight={highlight} normal={{ on: normal, count: normalCount(narrowed), onToggle: () => setNormal(!normal) }} compact />
          <Causes cluster={cluster} answer={answer} causes={causes} highlight={highlight} hidden={normal ? 0 : normalCount(narrowed)} />
        </>
      )}
    </div>
  );
}
