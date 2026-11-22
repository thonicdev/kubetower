import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { Pill, focusRing, metricsQuery, type MetricsPanel, btnSecondary, btnToggle } from '@kubetower/ui';

import { runQuery, type RunAnswer, type RunRecord, type Samples } from './data';
import { bytes, duration, moment } from './format';
import { View } from './View';

/**
 * One run: its outcome and what it cost, wherever it is drawn - in the runs
 * list's place on a CronJob's page, or in an object's KubeCron tab.
 *
 * ## The log is a button
 *
 * The run draws no log of its own until asked: **Log** unfolds it in the page, beside or under the run (`InlineLog`):
 * full-page by default would leave no way back and no way to another
 * CronJob, so full-page is that panel's own button. The run is read in focus
 * inside the page, not in a window of its own.
 */
export function RunPanel({
  cluster,
  id,
  onClose,
  closeLabel = 'Close',
  older,
  newer,
  onOpen,
  logOpen = false,
  onLog,
}: {
  cluster: string;
  id: string;
  onClose?: (() => void) | undefined;
  /** What the control that leaves the run says: "Close" under a list, "← All
   *  runs" where the run has taken the list's place. */
  closeLabel?: string;
  /** The runs on either side, in the list it was opened from. The
   *  arrows show only with `onOpen`, and one is disabled at its end. */
  older?: string | undefined;
  newer?: string | undefined;
  onOpen?: ((id: string) => void) | undefined;
  /** Whether this run's log is unfolded, and the button that folds it. */
  logOpen?: boolean;
  onLog?: (() => void) | undefined;
}): ReactNode {
  const run = useQuery(runQuery(cluster, id));
  return (
    <section aria-label="One run" className="rounded-lg border border-line bg-panel p-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        {/* When it started, which is how a person names a run; the collector's
            identifier is kept as the title, for copying. */}
        <h2 className="flex-1 text-base font-medium" title={id}>
          {run.data?.state === 'ok' ? `Run of ${moment(run.data.data.run.startedAt)}` : 'Run'}
        </h2>
        {onOpen === undefined ? null : (
          <span className="flex gap-1" data-run-arrows="">
            <button
              type="button"
              disabled={older === undefined}
              onClick={() => older !== undefined && onOpen(older)}
              title="The run before this one"
              className={btnSecondary}
            >
              ← Older
            </button>
            <button
              type="button"
              disabled={newer === undefined}
              onClick={() => newer !== undefined && onOpen(newer)}
              title="The run after this one"
              className={btnSecondary}
            >
              Newer →
            </button>
          </span>
        )}
        {onClose === undefined ? null : (
          <button type="button" onClick={onClose} className={btnSecondary}>
            {closeLabel}
          </button>
        )}
      </div>
      <View query={run} what="this run">
        {(r) => <RunBody cluster={cluster} answer={r} logOpen={logOpen} onLog={onLog} />}
      </View>
    </section>
  );
}

export function RunBody({
  cluster,
  answer,
  logOpen = false,
  onLog,
}: {
  cluster: string;
  answer: RunAnswer;
  logOpen?: boolean;
  onLog?: (() => void) | undefined;
}): ReactNode {
  const { run } = answer;
  return (
    <div className="flex flex-col gap-3 text-sm" data-run-body={run.id}>
      <div className="flex flex-wrap items-center gap-2">
        <Pill tone={STATUS_TONE[run.status] ?? ''}>{run.status}</Pill>
        <span className="text-muted tabular-nums">
          {moment(run.startedAt)}
          {run.durationMs === undefined ? '' : ` · ${duration(run.durationMs)}`}
          {run.trigger === 'manual' ? ' · manual' : ''}
        </span>
        <span className="flex-1" />
        {onLog === undefined ? null : <LogToggle id={run.id} open={logOpen} onToggle={onLog} />}
      </div>
      <RunStats cluster={cluster} answer={answer} />
      <p className="text-xs text-muted">
        {/* The Job opens its page in the console while it is there. */}
        {answer.job === undefined ? (
          `${answer.namespace}/${run.pod}`
        ) : (
          <Link
            to={`/c/${encodeURIComponent(cluster)}/jobs/${encodeURIComponent(answer.namespace)}/${encodeURIComponent(answer.job)}`}
            className={`text-accent hover:underline ${focusRing}`}
          >
            Job {answer.namespace}/{answer.job}
          </Link>
        )}
        {run.node === undefined ? '' : ` on ${run.node}`}
      </p>
    </div>
  );
}

export const STATUS_TONE: Readonly<Record<string, string>> = { succeeded: 'ok', failed: 'bad', running: 'warn' };

/** The one way to a run's log: it unfolds in the page, and folds again. */
export function LogToggle({ id, open, onToggle }: { id: string; open: boolean; onToggle: () => void }): ReactNode {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={open}
      data-log-toggle={id}
      className={btnToggle(open)}
    >
      Log
    </button>
  );
}

/**
 * Exit code, CPU and memory, as three figures.
 *
 * CPU and memory are the collector's, sampled over the run. **A figure nobody
 * recorded is said to be missing, never drawn as 0**: a cluster that records no
 * samples, a run shorter than one sampling interval and a collector that could
 * not be asked each say so. A run still going whose pod is there reads its
 * current usage from metrics-server, through the console's own metrics panel,
 * read for the signed-in person.
 */
export function RunStats({ cluster, answer }: { cluster: string; answer: RunAnswer }): ReactNode {
  const { run, samples } = answer;
  const live = run.status === 'running' && answer.podExists === true;
  const recorded = figures(run, samples);
  return (
    <dl className="grid grid-cols-3 gap-3" data-run-stats="">
      <Stat label="Exit code">
        {run.exitCode === undefined ? (
          <span className="text-muted">{run.status === 'running' ? 'running' : 'not recorded'}</span>
        ) : (
          <span className={run.exitCode === 0 ? '' : 'text-bad'}>{run.exitCode}</span>
        )}
      </Stat>
      {recorded !== undefined ? (
        <>
          <Stat label="CPU">{recorded.cpu ?? <Missing />}</Stat>
          <Stat label="Memory">{recorded.memory ?? <Missing />}</Stat>
        </>
      ) : live ? (
        <LiveUsage cluster={cluster} namespace={answer.namespace} pod={answer.livePod ?? run.pod} />
      ) : (
        <>
          <Stat label="CPU">
            <Missing />
          </Stat>
          <Stat label="Memory">
            <Missing />
          </Stat>
          <p className="col-span-3 -mt-2 text-xs text-muted" data-why-missing="">
            {whyNoSamples(answer)}
          </p>
        </>
      )}
    </dl>
  );
}

function Missing({ word = 'not recorded' }: { word?: string }): ReactNode {
  return (
    <span className="text-muted" data-missing="">
      {word}
    </span>
  );
}

function Stat({ label, children }: { label: string; children: ReactNode }): ReactNode {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-xs uppercase tracking-wide text-muted">{label}</dt>
      <dd className="tabular-nums">{children}</dd>
    </div>
  );
}

/** "avg · peak" for each, from the samples' summary or the run's own record;
 *  undefined for a measurement nobody recorded, and for both when neither was. */
export function figures(run: RunRecord, samples: Samples | undefined): { cpu?: string; memory?: string } | undefined {
  const avgCpu = samples?.avgCpuMillicores ?? run.avgCpuMillicores;
  const maxCpu = samples?.maxCpuMillicores ?? run.maxCpuMillicores;
  const avgMem = samples?.avgMemoryBytes ?? run.avgMemoryBytes;
  const maxMem = samples?.maxMemoryBytes ?? run.maxMemoryBytes;
  if (avgCpu === undefined && maxCpu === undefined && avgMem === undefined && maxMem === undefined) return undefined;
  const pair = (avg: string | undefined, peak: string | undefined): string | undefined =>
    avg === undefined ? (peak === undefined ? undefined : `peak ${peak}`) : peak === undefined ? avg : `${avg} · peak ${peak}`;
  const cpu = pair(avgCpu === undefined ? undefined : `${avgCpu}m`, maxCpu === undefined ? undefined : `${maxCpu}m`);
  const memory = pair(avgMem === undefined ? undefined : bytes(avgMem), maxMem === undefined ? undefined : bytes(maxMem));
  return { ...(cpu === undefined ? {} : { cpu }), ...(memory === undefined ? {} : { memory }) };
}

/** Why a run has no CPU or memory figure, in one sentence. */
export function whyNoSamples(answer: RunAnswer): string {
  const { samples, run } = answer;
  if (samples === undefined) return answer.samplesNote ?? 'The samples could not be read.';
  if (!samples.metricsEnabled) return 'This collector records no CPU or memory on this cluster.';
  if (run.status === 'running') return 'Still running, and its pod is not one this console can read now.';
  return `The run ended inside one sampling interval (${samples.intervalSeconds}s).`;
}

/** metrics-server's current reading, for a pod still running. */
function LiveUsage({ cluster, namespace, pod }: { cluster: string; namespace: string; pod: string }): ReactNode {
  const panel = useQuery(metricsQuery(cluster, 'pods', namespace, pod, {}));
  const read = panel.data?.state === 'ok' ? panel.data.data : undefined;
  const say = (key: string): ReactNode => {
    if (panel.isPending) return <Missing word="reading…" />;
    const figure = read === undefined ? undefined : now(read, key);
    return figure === undefined ? <Missing word="not measured" /> : <span title="Now, from metrics-server">{figure} now</span>;
  };
  return (
    <>
      <Stat label="CPU">{say('cpu')}</Stat>
      <Stat label="Memory">{say('memory')}</Stat>
    </>
  );
}

/** A line's current figure: the server's own words, or its newest sample. */
export function now(panel: MetricsPanel, key: string): string | undefined {
  const line = panel.series.find((s) => s.key === key);
  if (line === undefined) return undefined;
  if (line.current !== undefined && line.current.value !== null && line.current.text !== undefined) return line.current.text;
  const values = line.data.values;
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v === null || v === undefined) continue;
    if (line.unit === 'mcores') return `${Math.round(v)}m`;
    if (line.unit === 'bytes') return bytes(Math.round(v));
    return String(v);
  }
  return undefined;
}
