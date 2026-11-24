import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Pending, type ExtensionTabProps } from '@kubetower/ui';

import { getProbe, serviceQuery, type Member, type ProbeState } from './data';
import { DeploymentCard, ExternalTile, ProblemNotice } from './parts';

/**
 * The Redis viewer in a Service's panel: the
 * deployment the Service belongs to - every member, every
 * door onto each with the reason it was offered and its role - the probes, and
 * a link to each instance's page.
 *
 * Drawn only on a Service the inventory would offer (the tab's `probe` asks the
 * route, which answers from discovery alone), and **it dials nothing on
 * mount**: the probe is pressed, as on the inventory.
 */
export default function RedisTab({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
  const [probes, setProbes] = useState<Record<string, ProbeState>>({});
  if (namespace === undefined || namespace === '') {
    return <p className="text-sm text-muted">A Service is namespaced; this one came without its namespace.</p>;
  }
  const probe = async (member: Member) => {
    const door = member.doors.find((d) => d.service === name) ?? member.doors[0];
    if (door === undefined) return;
    setProbes((p) => ({ ...p, [member.key]: { status: 'running' } }));
    try {
      const answer = await getProbe(cluster, door);
      setProbes((p) => ({ ...p, [member.key]: { status: 'answered', answer } }));
    } catch (error) {
      setProbes((p) => ({ ...p, [member.key]: { status: 'failed', error } }));
    }
  };
  return <OnService cluster={cluster} namespace={namespace} name={name} probes={probes} probe={(m) => void probe(m)} />;
}

function OnService({
  cluster,
  namespace,
  name,
  probes,
  probe,
}: {
  cluster: string;
  namespace: string;
  name: string;
  probes: Readonly<Record<string, ProbeState>>;
  probe: (m: Member) => void;
}): ReactNode {
  const service = useQuery(serviceQuery(cluster, namespace, name));
  if (service.data === undefined) {
    return service.isError ? (
      <div data-redis-tab="unchecked" className="flex flex-col gap-2 text-sm">
        <p className="font-medium text-warn">Could not check whether this Service is a Redis</p>
        <ProblemNotice error={service.error} onRetry={() => void service.refetch()} />
      </div>
    ) : (
      <Pending what="what is behind this Service" />
    );
  }
  if (service.data.external !== undefined) {
    return (
      <div className="flex flex-col gap-3 text-sm" data-redis-tab="external">
        <ExternalTile door={service.data.external} />
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-3 text-sm" data-redis-tab="service">
      {(service.data.podsNotes ?? []).map((note) => (
        <p key={note} data-pods="unread" className="text-xs text-warn">
          {note.charAt(0).toUpperCase() + note.slice(1)}.
        </p>
      ))}
      {service.data.deployments.map((deployment) => (
        <DeploymentCard
          key={deployment.key}
          cluster={cluster}
          deployment={deployment}
          members={deployment.members}
          scope=""
          probes={probes}
          onProbe={probe}
        />
      ))}
    </div>
  );
}
