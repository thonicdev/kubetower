import type { ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Pending, type ExtensionTabProps } from '@kubetower/ui';

import { KEY, clusterQuery, getPod } from './data';
import { ClusterView } from './ClusterPage';
import { Problem } from './states';

/**
 * The Postgres tab, in the panel of an instance pod or of a CloudNativePG
 * Cluster: the cluster's page, compact, the pod's own instance ringed. A pod
 * is first asked which cluster it belongs to.
 */
export default function PostgresTab({ cluster, kind, namespace = '', name }: ExtensionTabProps): ReactNode {
  const isPod = kind === 'pods';
  const pod = useQuery({
    queryKey: [KEY, 'pod', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getPod(cluster, namespace, name, signal),
    enabled: isPod,
  });
  const owner = isPod ? pod.data?.of.name : name;
  const detail = useQuery({ ...clusterQuery(cluster, namespace, owner ?? ''), enabled: owner !== undefined && owner !== '' });

  const error = pod.error ?? detail.error;
  return (
    <div className="flex flex-col gap-3" data-cnpg-tab="" aria-busy={pod.isFetching || detail.isFetching}>
      {detail.data !== undefined ? (
        <ClusterView detail={detail.data} cluster={cluster} compact highlight={isPod ? name : undefined} />
      ) : error !== null ? (
        <Problem error={error} onRetry={() => void (pod.isError ? pod.refetch() : detail.refetch())} />
      ) : (
        <Pending what="this Postgres cluster" compact />
      )}
    </div>
  );
}
