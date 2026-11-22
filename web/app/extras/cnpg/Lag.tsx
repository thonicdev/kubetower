import type { UseQueryResult } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { HEAD_ROW, Pending, Pill, ROW, TABLE, bodyCell, btnSecondary, headCell } from '@kubetower/ui';

import { CnpgProblem, type Lag, type ReplicaLag } from './data';
import { seconds } from './parts';
import { Problem } from './states';

/**
 * The replication lag of each standby, read when the button is pressed: one
 * fixed psql query run in the primary (`extras/cnpg/lag.go`). Never on load,
 * never on an interval. Each way it can give nothing is its own pill - a
 * refusal, a missing psql, a timeout, a cut or unreadable answer - and none is
 * drawn as 0 s; a lag Postgres did not report is a dash.
 */
export function LagBar({ lag, started, onRead }: { lag: UseQueryResult<Lag>; started: boolean; onRead: () => void }): ReactNode {
  return (
    <div className="flex flex-col gap-2" data-lag={started ? (lag.isError ? 'failed' : lag.data !== undefined ? 'read' : 'reading') : 'off'}>
      <div className="flex items-center gap-2">
        <button
          type="button"
          className={btnSecondary}
          onClick={onRead}
          disabled={lag.isFetching}
          data-lag-read=""
          title="Run one fixed read-only psql query in the primary's postgres container, as you (create pods/exec)"
        >
          {lag.isFetching ? 'Reading…' : started ? 'Read again' : 'Read lag'}
        </button>
        {lag.data !== undefined && lag.data.replicas.length === 0 ? (
          <Pill neutral="muted" title="The primary streams to no standby">
            no standby streaming
          </Pill>
        ) : null}
      </div>
      {lag.data === undefined ? (
        lag.isError ? (
          <LagProblem error={lag.error} />
        ) : lag.isFetching ? (
          <Pending what="the lag" />
        ) : null
      ) : lag.data.replicas.length === 0 ? null : (
        <table className={`${TABLE} w-fit tabular-nums`} data-lag-table="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Standby</th>
              <th className={headCell()}>State</th>
              <th className={`${headCell()} text-right`} title="Primary flushed WAL until the standby wrote it">
                Write
              </th>
              <th className={`${headCell()} text-right`} title="Until the standby flushed it to disk">
                Flush
              </th>
              <th className={`${headCell()} text-right`} title="Until the standby replayed it">
                Replay
              </th>
            </tr>
          </thead>
          <tbody>
            {lag.data.replicas.map((r) => (
              <Row key={r.name} r={r} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function Figure({ v }: { v: number | undefined }): ReactNode {
  return v === undefined ? (
    <span className="text-muted" title="Not measured: Postgres reported no lag, nothing was replayed lately">
      —
    </span>
  ) : (
    <span>{seconds(v)}</span>
  );
}

function Row({ r }: { r: ReplicaLag }): ReactNode {
  return (
    <tr className={ROW} data-lag-replica={r.name}>
      <td className={`${bodyCell()} font-medium`}>{r.name}</td>
      <td className={bodyCell()}>
        <Pill tone={r.state === 'streaming' ? 'ok' : 'warn'} title={r.sync === '' ? undefined : r.sync}>
          {r.sync === '' || r.sync === 'async' ? r.state : `${r.state} · ${r.sync}`}
        </Pill>
      </td>
      <td className={`${bodyCell()} text-right`}>
        <Figure v={r.write} />
      </td>
      <td className={`${bodyCell()} text-right`}>
        <Figure v={r.flush} />
      </td>
      <td className={`${bodyCell()} text-right`}>
        <Figure v={r.replay} />
      </td>
    </tr>
  );
}

/** What each failure of the read is called: one pill, the sentence on hover. */
const WORD: Readonly<Record<string, { label: string; tone?: 'warn' | 'bad' }>> = {
  exec_forbidden: { label: 'Refused: no exec on the primary', tone: 'warn' },
  exec_unreviewed: { label: 'Not reviewed: try again', tone: 'warn' },
  no_psql: { label: 'No psql in the image', tone: 'warn' },
  exec_timed_out: { label: 'Timed out', tone: 'bad' },
  exec_busy: { label: 'Busy: try again', tone: 'warn' },
  lag_truncated: { label: 'Answer cut', tone: 'bad' },
  lag_unreadable: { label: 'Answer unreadable', tone: 'bad' },
  lag_query_failed: { label: 'psql failed', tone: 'bad' },
  exec_failed: { label: 'Could not run', tone: 'bad' },
  no_primary: { label: 'No primary', tone: 'warn' },
};

function LagProblem({ error }: { error: unknown }): ReactNode {
  const word = error instanceof CnpgProblem ? WORD[error.code] : undefined;
  if (word === undefined || !(error instanceof CnpgProblem)) return <Problem error={error} />;
  return (
    <div className="flex items-center gap-2" data-lag-problem={error.code} role="alert">
      <Pill tone={word.tone} title={error.detail === undefined ? error.message : `${error.message} ${error.detail}`}>
        {word.label}
      </Pill>
      <span className="sr-only">{error.message}</span>
    </div>
  );
}
