import { useQuery } from '@tanstack/react-query';
import { useMemo, type ReactNode } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router';
import { CloseButton, CopyButton, LogsButton, Pill, btnSecondary, focusRing, useDeclarePageFacts } from '@kubetower/ui';

import { FROM_CONSOLE, cronJobPagePath, logPagePath, openedFromConsole, runQuery, type RunAnswer, type RunLog } from './data';
import { dayOfInstant } from './calendar';
import { bytes, duration, moment } from './format';
import { tokenize, type Level, type TokenKind } from './logTokens';
import { STATUS_TONE } from './RunView';
import { View } from './View';

/**
 * One run's log, full-page, at `/c/:cluster/x/kubecron/runs/:id/log`,
 * with as little text around it as can be.
 *
 * A code viewer and nothing else: line numbers, timestamps, levels, `key=`
 * keys and JSON colored from the theme's own syntax tokens (the manifest
 * view's), an error or warning line tinted whole. One header line names the
 * run. The copy is the collector's; while the pod is still there, the
 * console's live log is one button away in the dock.
 *
 * **Full-page is asked for, never the default**: a run's log opens in the page, beside or under
 * the runs (`InlineLog`), and only that panel's "Full page" leads here. The ×
 * at the top right returns to the view it was opened from, as it was.
 */
export function LogPage(): ReactNode {
  const { cluster = '', id = '' } = useParams<{ cluster: string; id: string }>();
  useDeclarePageFacts({ title: 'KubeCron / log' });
  const run = useQuery(runQuery(cluster, id));
  return (
    <div className="flex h-full min-h-0 flex-col" data-kubecron-log={id}>
      <View query={run} what="this run's log">
        {(r) => <Log cluster={cluster} answer={r} />}
      </View>
    </div>
  );
}

function Log({ cluster, answer }: { cluster: string; answer: RunAnswer }): ReactNode {
  const { run, log } = answer;
  const day = dayOfInstant(run.startedAt);
  // **Back is back**: the log is
  // opened from the page's heatmap, a CronJob's page or a tab, and the arrow
  // returns there, to the day and run that were picked. Opened any other way -
  // a pasted link, or after signing in - there is nowhere in the console to
  // return to, and no arrow: only the link to the CronJob.
  const navigate = useNavigate();
  const location = useLocation();
  const cameFrom = openedFromConsole(location.state);
  const cronJobRunsOfDay = `${cronJobPagePath(cluster, answer.namespace, answer.cronJob)}${day === undefined ? '' : `?day=${day}`}`;
  return (
    <>
      <header className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2 text-sm">
        <Link
          to={cronJobRunsOfDay}
          className={`font-mono text-accent hover:underline ${focusRing}`}
          title="This CronJob's runs that day"
        >
          {answer.namespace}/{answer.cronJob}
        </Link>
        <Pill tone={STATUS_TONE[run.status] ?? ''}>{run.status}</Pill>
        <span className="text-xs text-muted tabular-nums">
          {moment(run.startedAt)}
          {run.durationMs === undefined ? '' : ` · ${duration(run.durationMs)}`}
          {run.exitCode === undefined ? '' : ` · exit ${run.exitCode}`}
        </span>
        <span className="flex-1" />
        {answer.podExists === true ? (
          <LogsButton cluster={cluster} kind="pods" namespace={answer.namespace} name={answer.livePod ?? run.pod} />
        ) : null}
        {log !== undefined && log.lines.length > 0 ? (
          <CopyButton value={log.lines.map((l) => l.text).join('\n')} label="Copy" />
        ) : null}
        {/* Always a ×: back to where the log was opened when the console
            opened it, to its CronJob's runs of that day otherwise (a pasted
            link, a reload after signing in). */}
        {cameFrom ? (
          <CloseButton onClick={() => void navigate(-1)} label="Close the full page" />
        ) : (
          <CloseButton to={cronJobRunsOfDay} label="Close the full page" />
        )}
      </header>
      <Body answer={answer} />
    </>
  );
}

/**
 * A run's log inside the page it was asked from: beside the runs on a wide window, under them on
 * a narrow one or in a tab, with the code viewer's colors, scrolling inside
 * its own box. "Full page" at the top right opens `LogPage`, whose × comes
 * back here; Close folds it.
 */
export function InlineLog({
  cluster,
  id,
  onClose,
  onFullPage,
}: {
  cluster: string;
  id: string;
  onClose: () => void;
  /** Called as the log goes full page, for a view that must leave itself a way back. */
  onFullPage?: (() => void) | undefined;
}): ReactNode {
  const run = useQuery(runQuery(cluster, id));
  return (
    <section aria-label="Log" data-inline-log={id} className="flex max-h-160 min-h-0 flex-col overflow-hidden rounded-lg border border-line bg-panel">
      <View query={run} what="this run's log">
        {(r) => (
          <>
            <header className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-1.5 text-xs">
              <span className="font-medium">Log</span>
              <Pill tone={STATUS_TONE[r.run.status] ?? ''}>{r.run.status}</Pill>
              <span className="text-muted tabular-nums">
                {r.namespace}/{r.cronJob} · {moment(r.run.startedAt)}
              </span>
              <span className="flex-1" />
              {r.podExists === true ? <LogsButton cluster={cluster} kind="pods" namespace={r.namespace} name={r.livePod ?? r.run.pod} /> : null}
              {r.log !== undefined && r.log.lines.length > 0 ? <CopyButton value={r.log.lines.map((l) => l.text).join('\n')} label="Copy" /> : null}
              <Link
                to={logPagePath(cluster, id)}
                state={FROM_CONSOLE}
                // A full page opened in another browser tab leaves this view
                // where it is: no way back to keep.
                onClick={(e) => {
                  if (!(e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0)) onFullPage?.();
                }}
                data-log-full=""
                title="Open the log full-page"
                className={btnSecondary}
              >
                ⤢ Full page
              </Link>
              <CloseButton onClick={onClose} label="Close the log" size="inline" />
            </header>
            <Body answer={r} />
          </>
        )}
      </View>
    </section>
  );
}

function Body({ answer }: { answer: RunAnswer }): ReactNode {
  const { log } = answer;
  if (log === undefined) {
    // A refusal is the person's rights, not a failure: drawn quietly, and
    // apart from a collector that could not answer.
    const state = answer.logState ?? 'failed';
    return (
      <p className={`p-4 text-sm ${state === 'refused' ? 'text-muted' : 'text-warn'}`} data-log-state={state}>
        {answer.logNote ?? 'The log could not be read.'}
      </p>
    );
  }
  if (log.lines.length === 0) {
    return (
      <p className="p-4 text-sm text-muted" data-log-state={log.expired ? 'expired' : 'empty'}>
        {log.expired ? `Aged out of the collector's log retention (${bytes(log.sizeBytes)} printed).` : 'No output.'}
      </p>
    );
  }
  return <CodeView log={log} />;
}

const TONE: Readonly<Record<TokenKind, string>> = {
  plain: '',
  time: 'text-muted',
  error: 'font-semibold text-bad',
  warn: 'font-semibold text-warn',
  info: 'text-accent',
  debug: 'text-muted',
  key: 'text-y-key',
  string: 'text-y-str',
  number: 'text-y-num',
  literal: 'text-y-bool',
  punct: 'text-muted',
};

const ROW: Readonly<Record<Level, string>> = {
  error: 'bg-bad/10',
  warn: 'bg-warn/10',
  info: '',
  debug: '',
};

export function CodeView({ log }: { log: RunLog }): ReactNode {
  const lines = useMemo(() => log.lines.map((l) => ({ at: l.at, ...tokenize(l.text) })), [log.lines]);
  const width = String(lines.length).length;
  return (
    <div className="min-h-0 flex-1 overflow-auto bg-panel-2">
      {log.truncated ? (
        <p className="px-4 pt-2 text-xs text-muted" data-log-truncated="">
          Last {lines.length} lines.
        </p>
      ) : null}
      <pre className="py-2 font-mono text-xs leading-5">
        <code>
          {lines.map((line, i) => (
            <div key={i} data-line={i + 1} data-level={line.level ?? ''} className={`flex pr-4 ${line.level === undefined ? '' : ROW[line.level]}`}>
              {/* The number is not part of the text: unselectable and
                  unread, so a copy pastes the log and nothing else. The
                  collector's own time for the line is its title. */}
              <span
                aria-hidden
                title={line.at}
                className="mr-3 shrink-0 select-none border-r border-line pr-2 pl-4 text-right text-muted/60 tabular-nums"
                style={{ width: `${width + 3}ch` }}
              >
                {i + 1}
              </span>
              <span className="min-w-0 whitespace-pre-wrap wrap-anywhere">
                {line.tokens.map((t, j) => (
                  <span key={j} className={TONE[t.kind]}>
                    {t.text}
                  </span>
                ))}
              </span>
            </div>
          ))}
        </code>
      </pre>
      {log.running ? <p className="px-4 pb-2 text-xs text-muted">Still running.</p> : null}
    </div>
  );
}
