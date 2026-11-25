import { useState, type ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import { useClusters } from '../../api';
import { Pill } from '../../components/Pill';
import { btnPrimary } from '../../components/buttons';
import { focusRingTight } from '../../focus';
import { arrange } from '../../shell/clusterState';
import { metricsSourceKey, saveMetricsSource, useMetricsSource, type MetricsSource } from './data';

/**
 * Settings › General: where each cluster's charts read their history from.
 *
 * One row per cluster: its name, the source (`namespace/service:port[/path]`,
 * empty for discovery) and a badge for what answers. The desktop edits the
 * field; under the server profile the deployment's `KT_PROMETHEUS_SERVICE` is
 * the source of every cluster and the row only reads it (`editable: false`).
 */
export function MetricsSources(): ReactNode {
  const clusters = useClusters();
  const listed = clusters.data?.state === 'ok' ? arrange(clusters.data.items) : [];
  if (listed.length === 0) return null;
  return (
    <section data-settings="metrics-sources" className="rounded-lg border border-line bg-panel p-3 xl:col-span-2">
      <h2 className="text-base font-medium">Metrics source</h2>
      <ul className="mt-2 flex flex-col divide-y divide-line">
        {listed.map((cluster) => (
          <SourceRow key={cluster.name} cluster={cluster.name} label={cluster.label} />
        ))}
      </ul>
    </section>
  );
}

const PLACEHOLDER = 'namespace/service:port/path';

function SourceRow({ cluster, label }: { cluster: string; label: string }): ReactNode {
  const source = useMetricsSource(cluster);
  const current = source.data;
  return (
    <li
      data-metrics-source={cluster}
      className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 py-2 text-sm sm:grid-cols-[8rem_minmax(0,1fr)_10rem]"
    >
      <span className="min-w-0 truncate font-medium" title={cluster}>
        {label}
      </span>
      <div className="col-span-2 row-start-2 min-w-0 sm:col-span-1 sm:col-start-2 sm:row-start-1">
        {current === undefined ? null : current.editable ? (
          <SourceForm cluster={cluster} current={current} />
        ) : (
          <code className="block truncate font-mono text-xs text-muted" title={current.source}>
            {current.source === '' ? '—' : current.source}
          </code>
        )}
      </div>
      <div className="col-start-2 row-start-1 justify-self-end sm:col-start-3">
        <SourceBadge source={current} failed={source.isError} />
      </div>
    </li>
  );
}

function SourceForm({ cluster, current }: { cluster: string; current: MetricsSource }): ReactNode {
  const cache = useQueryClient();
  const [value, setValue] = useState(current.source);
  const save = useMutation({
    mutationFn: (next: string) => saveMetricsSource(cluster, next),
    onSuccess: (next) => {
      cache.setQueryData(metricsSourceKey(cluster), next);
      setValue(next.source);
    },
  });
  const changed = value.trim() !== current.source;
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        save.mutate(value.trim());
      }}
      className="flex min-w-0 flex-wrap items-center gap-2"
    >
      <input
        name="source"
        aria-label={`Metrics source, ${cluster}`}
        value={value}
        onChange={(event) => {
          setValue(event.target.value);
        }}
        placeholder={PLACEHOLDER}
        autoComplete="off"
        spellCheck={false}
        className={`min-w-0 flex-1 rounded-md border border-line bg-panel-2 px-2 py-1 font-mono text-xs ${focusRingTight}`}
      />
      <button type="submit" disabled={save.isPending || !changed} className={`w-fit shrink-0 ${btnPrimary}`}>
        {save.isPending ? 'Saving…' : 'Save'}
      </button>
      {save.isError ? (
        <span role="alert" className="w-full text-xs text-bad">
          {save.error instanceof Error ? save.error.message : 'Refused.'}
        </span>
      ) : save.data?.persisted === false ? (
        <span role="status" className="w-full text-xs text-warn" title={save.data.note}>
          Not kept after a restart
        </span>
      ) : null}
    </form>
  );
}

export const NO_METRICS = 'No metrics available';

const ENGINE: Readonly<Record<string, string>> = {
  prometheus: 'Prometheus',
  victoriametrics: 'VictoriaMetrics',
};

/** What answers on this cluster, and the discovery's line in the tooltip. */
function SourceBadge({ source, failed }: { source: MetricsSource | undefined; failed: boolean }): ReactNode {
  if (source === undefined) {
    return (
      <span data-source-state={failed ? 'failed' : 'pending'}>
        <Pill neutral="muted">{failed ? NO_METRICS : '…'}</Pill>
      </span>
    );
  }
  const title = [source.address, source.detail].filter(Boolean).join('\n');
  if (source.kind === 'prometheus') {
    return (
      <span data-source-state="found">
        <Pill tone="ok" title={title}>
          {ENGINE[source.engine ?? 'prometheus'] ?? 'Prometheus'}
        </Pill>
      </span>
    );
  }
  if (source.kind === 'metrics-server') {
    return (
      <span data-source-state="metrics-server">
        <Pill neutral="normal" title={title}>
          metrics-server
        </Pill>
      </span>
    );
  }
  return (
    <span data-source-state={source.kind}>
      <Pill neutral="muted" title={title}>
        {NO_METRICS}
      </Pill>
    </span>
  );
}
