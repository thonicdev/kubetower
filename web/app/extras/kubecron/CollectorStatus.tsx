import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { btnSecondary, type ExtensionSettingsStatusProps } from '@kubetower/ui';

import { KEY, getStatus } from './data';
import { CollectorChecks, CollectorSource, sentence } from './InstallCollector';

/**
 * The collector's standing on this cluster, in KubeCron's tile on the Settings
 * page: found or not, its Secret, reached, its token -
 * with where each value came from. Read again after each save of the tile's
 * settings (`revision`), and on Recheck.
 */
export default function CollectorStatus({ cluster, revision }: ExtensionSettingsStatusProps): ReactNode {
  const status = useQuery({
    queryKey: [KEY, 'status', cluster, revision],
    queryFn: ({ signal }) => getStatus(cluster, signal),
    retry: false,
  });
  const recheck = (
    <button
      type="button"
      onClick={() => void status.refetch()}
      disabled={status.isFetching}
      className={`self-start ${btnSecondary}`}
    >
      {status.isFetching ? 'Checking…' : 'Recheck'}
    </button>
  );
  if (status.isPending) {
    return <p className="text-xs text-muted">Looking for the collector…</p>;
  }
  if (status.isError) {
    return (
      <div className="flex flex-col gap-1" data-collector-status="error">
        <p role="alert" className="text-xs text-bad">
          The collector&apos;s standing could not be read: {status.error instanceof Error ? status.error.message : 'no reason was given'}
        </p>
        {recheck}
      </div>
    );
  }
  const state = status.data;
  const headline =
    state.state === 'ok'
      ? `Reading the collector at ${state.where ?? 'its Service'}.`
      : state.state === 'absent'
        ? 'No collector is read on this cluster.'
        : sentence(state.detail ?? 'The collector could not be read');
  return (
    <section className="flex flex-col gap-1 border-t border-line pt-2" data-collector-status={state.state} aria-label="KubeCron collector">
      <p className={`text-xs ${state.state === 'ok' ? 'text-ok' : state.state === 'absent' ? 'text-muted' : 'text-warn'}`}>
        {headline}
      </p>
      <CollectorChecks state={state} />
      <CollectorSource state={state} />
      {recheck}
    </section>
  );
}
