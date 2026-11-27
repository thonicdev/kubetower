import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Failure, ImageRef, Pending, Pill, type ExtensionTabProps } from '@kubetower/ui';

import { RUNTIME_LABEL, podQuery, type Container, type Runtime } from './data';
import { GoPprof } from './GoPprof';
import { NodeInspector } from './NodeInspector';
import { PythonPanel } from './PythonPanel';

/**
 * The tab of a runtime other than Java on a pod: which containers the spec
 * says run it, and what that rests on. It reads the spec alone (the same route
 * as the page) and runs nothing in a container. Go adds pprof (GoPprof),
 * Node.js its inspector (NodeInspector), Python its processes and
 * prometheus_client series (PythonPanel).
 */
function RuntimeTab({ runtime, cluster, namespace, name }: ExtensionTabProps & { runtime: Runtime }): ReactNode {
  if (namespace === undefined || namespace === '') return null;
  return <OnPod runtime={runtime} cluster={cluster} namespace={namespace} name={name} />;
}

function OnPod({ runtime, cluster, namespace, name }: { runtime: Runtime; cluster: string; namespace: string; name: string }): ReactNode {
  const pod = useQuery(podQuery(cluster, namespace, name));
  const [picked, setPicked] = useState<string | undefined>(undefined);
  if (pod.data === undefined) {
    return pod.isError ? <Failure error={pod.error} onRetry={() => void pod.refetch()} /> : <Pending what="this pod's containers" />;
  }
  const offered = pod.data.containers.filter((c) => c.runtime === runtime);
  const container = offered.find((c) => c.name === picked) ?? offered[0];
  if (container === undefined) {
    return null;
  }
  return (
    <div data-runtime-tab={runtime} className="flex flex-col gap-3 text-sm">
      {offered.length > 1 ? (
        <label className="flex items-center gap-1 text-xs text-muted">
          Container
          <select
            className="rounded border border-line bg-panel px-1 py-0.5 text-fg"
            value={container.name}
            onChange={(e) => setPicked(e.target.value)}
          >
            {offered.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <ContainerFacts runtime={runtime} container={container} />
      {runtime === 'node' ? (
        <NodeInspector key={container.name} cluster={cluster} namespace={namespace} name={name} container={container.name} />
      ) : null}
      {runtime === 'go' ? (
        <GoPprof key={container.name} cluster={cluster} namespace={namespace} name={name} container={container.name} />
      ) : null}
      {runtime === 'python' ? (
        <PythonPanel key={container.name} cluster={cluster} namespace={namespace} name={name} container={container.name} />
      ) : null}
    </div>
  );
}

function ContainerFacts({ runtime, container }: { runtime: Runtime; container: Container }): ReactNode {
  return (
    <div data-runtime-container={container.name} className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Pill tone="info">{RUNTIME_LABEL[runtime]}</Pill>
        <span className="font-medium">{container.name}</span>
        <ImageRef image={container.image} of={container.name} className="max-w-full" />
      </div>
      <div className="flex flex-wrap gap-1" data-runtime-hints="">
        {container.hints.map((hint) => (
          <Pill key={`${hint.source}:${hint.reason}`} neutral="muted" title={hint.reason}>
            {hint.source}
          </Pill>
        ))}
      </div>
    </div>
  );
}

/** The tab component for one runtime, as the extension's `tabs` lazy-load it. */
export function tabFor(runtime: Runtime): (props: ExtensionTabProps) => ReactNode {
  function Tab(props: ExtensionTabProps): ReactNode {
    return <RuntimeTab runtime={runtime} {...props} />;
  }
  Tab.displayName = `RuntimeTab(${runtime})`;
  return Tab;
}
