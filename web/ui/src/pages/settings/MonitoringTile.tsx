import { useState, type ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import { Pill } from '../../components/Pill';
import { NoMetrics } from '../../state/NoMetrics';
import { btnPrimary } from '../../components/buttons';
import { focusRingTight } from '../../focus';
import { metricsSourceKey, saveMetricsSource, useMetricsSource, useSettingsCluster, type MetricsSource } from './data';

/**
 * Settings › General's Monitoring tile: where the current cluster's charts read
 * their history from.
 *
 * A badge for what answers, the discovery's line in its tooltip. The field
 * (`namespace/service:port[/path]`) is drawn only where it is needed: no
 * history source was found (metrics-server keeps no history), or a source is
 * already set and may be cleared. Under the server
 * profile the deployment's `KT_PROMETHEUS_SERVICE` is read, never edited.
 */
export function MonitoringTile(): ReactNode {
  const cluster = useSettingsCluster();
  if (cluster === undefined) return null;
  return <Tile key={cluster.name} cluster={cluster.name} label={cluster.label} />;
}

function Tile({ cluster, label }: { cluster: string; label: string }): ReactNode {
  const source = useMetricsSource(cluster);
  const current = source.data;
  // A history source found. metrics-server keeps none, so it leaves the field.
  const found = current?.kind === 'prometheus';
  const editing = current?.editable === true && (!found || current.source !== '');
  return (
    <section data-settings="monitoring" data-monitoring-cluster={cluster} className="rounded-lg border border-line bg-panel p-3">
      <div className="flex items-center gap-2">
        <h2 className="text-base font-medium">Monitoring</h2>
        <span className="min-w-0 flex-1 truncate text-xs text-muted" title={cluster}>
          {label}
        </span>
        <SourceBadge source={current} failed={source.isError} />
      </div>
      {current === undefined ? null : editing ? (
        <SourceForm cluster={cluster} current={current} />
      ) : !current.editable && current.source !== '' ? (
        <code className="mt-2 block truncate font-mono text-xs text-muted" title={current.source}>
          {current.source}
        </code>
      ) : null}
    </section>
  );
}

const PLACEHOLDER = 'namespace/service:port/path';

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
      className="mt-2 flex flex-wrap items-center gap-2"
    >
      <input
        name="source"
        aria-label="Metrics service"
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

const ENGINE: Readonly<Record<string, string>> = {
  prometheus: 'Prometheus',
  victoriametrics: 'VictoriaMetrics',
};

/** What answers on this cluster, and the discovery's line in the tooltip. */
function SourceBadge({ source, failed }: { source: MetricsSource | undefined; failed: boolean }): ReactNode {
  if (source === undefined) {
    return (
      <span data-source-state={failed ? 'failed' : 'pending'}>
        {failed ? <NoMetrics variant="badge" /> : <Pill neutral="muted">…</Pill>}
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
      <NoMetrics variant="badge" detail={title} />
    </span>
  );
}
