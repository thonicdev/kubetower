import { useId, type ReactNode } from 'react';

import type { ContainerProbe, ContainerStatus, PodContainer, ResourceAmount } from '../../api';
import { FoldedMessage, Message } from '../../components/FoldedMessage';
import { Icon } from '../../components/Icon';
import { formatInstant, shortDuration } from '../../components/instant';
import { useNow } from '../../components/useNow';
import { focusRing } from '../../focus';
import { Pill } from '../../components/Pill';
import { useEnvFold } from './envFolds';

/**
 * The pieces a container tile is built from: the
 * status pill, the lines under it, a remembered collapsible section, the probe
 * chips and the resource chips. Data only: every word drawn
 * is a value, a title or a short state.
 */

/** `Waiting: ImagePullBackOff`, `Terminated: OOMKilled`, or just `Running`. The
 *  reason is the word a user searches for and it goes in the pill; the
 *  message is the detail and it goes beside it, never instead of it. */
function stateOf(status: ContainerStatus): string {
  const word = status.state.charAt(0).toUpperCase() + status.state.slice(1);
  return status.reason === undefined || status.reason === '' ? word : `${word}: ${status.reason}`;
}

type Termination = NonNullable<ContainerStatus['lastTermination']>;

/**
 * How this container's **previous** run ended: the exit code and what it means,
 * the kubelet's reason, when it ended and how long it ran.
 *
 * A container that was OOM-killed and restarted reads *running* on the line
 * above, and nothing there says why it restarted. The kubelet keeps the answer
 * in `lastState.terminated` - the one previous run, however old - which
 * `kubectl describe` calls Last State. Directly under the state
 * line, so the restart count it explains is the line above it.
 *
 * **One previous run and nothing older**, because that is all the kubelet keeps:
 * a pod with no `lastTermination` draws nothing, never "never", since an evicted
 * or recreated pod has none whatever happened to it. `OOMKilled` is drawn in the
 * bad tone, because it is the one people come looking for.
 */
function LastTermination({ last }: { last: Termination }): ReactNode {
  const now = useNow();
  const meaning = exitMeaning(last.exitCode);
  const oom = last.reason === 'OOMKilled';
  const ended = last.finishedAt === undefined ? undefined : new Date(last.finishedAt);
  const started = last.startedAt === undefined ? undefined : new Date(last.startedAt);
  const ran =
    ended === undefined || started === undefined || Number.isNaN(ended.getTime()) || Number.isNaN(started.getTime())
      ? undefined
      : shortDuration(ended.getTime() - started.getTime());

  return (
    <div data-last-termination className="flex flex-wrap items-baseline gap-2 text-xs">
      <span className="text-2xs text-muted uppercase">last termination</span>
      <span data-last-exit-code={String(last.exitCode)} className={last.exitCode === 0 ? 'text-muted' : 'text-bad'}>
        exit code {String(last.exitCode)}
        {meaning === undefined ? null : <span className="text-muted"> ({meaning})</span>}
      </span>
      {last.reason === undefined || last.reason === '' ? null : (
        <span
          data-last-reason={last.reason}
          className={oom ? 'rounded-sm border border-bad px-1 font-semibold text-bad' : 'text-muted'}
        >
          {last.reason}
        </span>
      )}
      {ended === undefined || Number.isNaN(ended.getTime()) ? null : (
        <span data-last-ended className="text-muted" title={formatInstant(ended, { year: true, seconds: true })}>
          ended {shortDuration(now - ended.getTime())} ago
        </span>
      )}
      {ran === undefined ? null : <span className="text-muted">after running {ran}</span>}
      {last.signal === undefined || last.signal === 0 ? null : (
        <span data-last-signal={String(last.signal)} className="text-muted">
          signal {String(last.signal)}
        </span>
      )}
      {/* The kubelet's termination message: the container's
          `terminationMessagePath`, or with `FallbackToLogsOnError` the tail of
          its log, up to 4 KB. Often one line of JSON or a stack trace, so it is
          neither short nor breakable at a space (a 176-character JSON line
          would make the panel wider than the drawer). Folded
          like the current state's, so the panel's height does not depend on
          what the application wrote. */}
      {last.message === undefined || last.message === '' ? null : (
        <FoldedMessage
          of="last"
          message={last.message}
          label={foldLabel(last.exitCode)}
        />
      )}
    </div>
  );
}

/**
 * What the control over a run's message says, decided **by the exit code** for
 * the current state and the last termination alike:
 * a run that exited 0 left a message, any other left an error. The current
 * state's `exitCode` is omitted when it is 0, so the caller passes `?? 0`. The
 * reason is not read: `Completed` and exit 0 agree for the kubelet's own words,
 * and the code is the one that is always there.
 */
function foldLabel(exitCode: number): string {
  return exitCode === 0 ? 'show message' : 'show error';
}

/**
 * What an exit code usually means, where it is one of the few worth naming.
 * Above 128 a process was ended by a signal, 128 plus its number; 137 is the
 * one the OOM killer sends, so "often OOM" - the `reason` beside it says
 * whether it was.
 */
function exitMeaning(code: number): string | undefined {
  switch (code) {
    case 0:
      return 'clean exit';
    case 1:
      return 'application error';
    case 137:
      return 'SIGKILL, often OOM';
    case 143:
      return 'SIGTERM';
    default:
      return code > 128 && code < 160 ? `signal ${String(code - 128)}` : undefined;
  }
}

/**
 * State, readiness and restarts in one pill. An unreported container says only
 * that, with the server's reason as the title: its `ready` means nothing.
 */
export function StatusPill({ status }: { status: ContainerStatus | undefined }): ReactNode {
  if (status === undefined) return null;
  const unreported = status.state === 'unreported';
  // Zero is omitted from the payload on a reported container; unknown on an
  // unreported one.
  const restarts = unreported ? undefined : (status.restarts ?? 0);
  return (
    <Pill tone={gradeOf(status)} neutral="muted" title={unreported ? status.problem : undefined}>
      <span data-status={status.state}>{stateOf(status)}</span>
      {unreported ? null : (
        <>
          <span className="font-normal"> · </span>
          <span data-ready={status.ready ? 'true' : 'false'} className="font-normal">
            {status.ready ? 'ready' : 'not ready'}
          </span>
        </>
      )}
      {restarts === undefined ? null : (
        <>
          <span className="font-normal"> · </span>
          <span data-restarts={String(restarts)} className={`font-normal ${restarts > 0 ? 'text-warn' : ''}`}>
            {restarts === 1 ? '1 restart' : `${String(restarts)} restarts`}
          </span>
        </>
      )}
    </Pill>
  );
}

/** The pill's grade: `toneOf`'s reading of a state, as `Pill` takes it. */
function gradeOf(status: ContainerStatus): string | undefined {
  if (status.state === 'unreported') return undefined;
  if (status.state === 'terminated') return status.reason === 'Completed' ? undefined : 'bad';
  if (status.state === 'running' && status.ready) return 'ok';
  return 'warn';
}

/** What the pill cannot hold: the exit code, the state's message, the last run. */
export function StatusLines({ status }: { status: ContainerStatus | undefined }): ReactNode {
  if (status === undefined) return null;
  const exit = status.state === 'terminated' && status.exitCode !== undefined;
  const message = status.message !== undefined && status.message !== '';
  if (!exit && !message && status.lastTermination === undefined) return null;
  return (
    <div className="mt-1 flex flex-col gap-1 text-xs">
      {exit || message ? (
        <div className="flex flex-wrap items-baseline gap-2">
          {exit ? (
            <span data-exit-code={String(status.exitCode)} className="text-bad">
              exit code {String(status.exitCode)}
            </span>
          ) : null}
          {!message ? null : status.state === 'terminated' ? (
            <FoldedMessage of="state" message={status.message ?? ''} label={foldLabel(status.exitCode ?? 0)} />
          ) : (
            <Message
              of="state"
              message={status.message ?? ''}
              label="show message"
              inline={(text) => (
                <span data-status-message className="w-full text-muted">
                  {text}
                </span>
              )}
            />
          )}
        </div>
      ) : null}
      {status.lastTermination === undefined ? null : <LastTermination last={status.lastTermination} />}
    </div>
  );
}

/**
 * A titled section that folds, remembered across the object's tabs as the
 * environment's lists are (`envFolds.ts`). Closed, the rows are not rendered at
 * all, so a revealed Secret value leaves the page with them, and `summary` -
 * data, never a sentence - says what is inside. `faults` stay on the header
 * while closed.
 */
export function Section({
  heading,
  summary,
  faults = [],
  memory,
  defaultOpen,
  children,
}: {
  heading: string;
  summary?: ReactNode;
  faults?: readonly string[];
  memory: string;
  defaultOpen: boolean;
  children: ReactNode;
}): ReactNode {
  // The memory holds the exceptions to the default, so a section open by
  // default needs nothing stored to be open.
  const [flipped, remember] = useEnvFold(`${memory}:${defaultOpen ? 'closed' : 'open'}`);
  const open = defaultOpen ? !flipped : flipped;
  const body = useId();
  return (
    <section data-tile-section={heading.toLowerCase()} className="mt-2">
      <div className="flex min-w-0 items-center gap-2">
        <button
          type="button"
          data-fold-control
          aria-expanded={open}
          aria-controls={body}
          onClick={() => {
            remember(!flipped);
          }}
          className={`inline-flex shrink-0 cursor-pointer items-center gap-1 rounded-sm text-2xs text-muted uppercase hover:text-fg ${focusRing}`}
        >
          <Icon name="chevron" className={`size-3 transition-transform ${open ? 'rotate-90' : ''}`} />
          <span>{heading}</span>
        </button>
        {open ? null : (
          <span data-fold-summary className="min-w-0 truncate text-xs text-muted">
            {summary}
          </span>
        )}
        {open || faults.length === 0 ? null : (
          <span data-fold-faults className="shrink-0 text-2xs text-warn">
            {faults.join(' · ')}
          </span>
        )}
      </div>
      <div id={body} data-fold-body className="mt-1">
        {open ? children : null}
      </div>
    </section>
  );
}

/** A probe's timings, short - `after 15s`, `every 10s`, `1s timeout`, `×3` - each with its meaning as a title. */
function timings(probe: ContainerProbe): { text: string; title: string }[] {
  const out: { text: string; title: string }[] = [];
  if (probe.initialDelaySeconds) out.push({ text: `after ${String(probe.initialDelaySeconds)}s`, title: 'initial delay' });
  if (probe.periodSeconds) out.push({ text: `every ${String(probe.periodSeconds)}s`, title: 'period' });
  if (probe.timeoutSeconds) out.push({ text: `${String(probe.timeoutSeconds)}s timeout`, title: 'timeout' });
  if (probe.failureThreshold) out.push({ text: `×${String(probe.failureThreshold)}`, title: 'failures before it acts' });
  return out;
}

/** One probe as a chip: its kind, then its check and timings, `none`, or `unreadable`. */
export function ProbeChip({ probe }: { probe: ContainerProbe }): ReactNode {
  const tone =
    probe.state === 'declared'
      ? 'border-line'
      : probe.state === 'unreadable'
        ? 'border-warn/40 text-warn'
        : 'border-dashed border-line text-muted';
  return (
    <span
      data-probe={probe.kind}
      data-probe-state={probe.state}
      className={`inline-flex min-w-0 flex-wrap items-baseline gap-x-1.5 rounded-sm border px-1.5 py-px text-xs ${tone}`}
    >
      <span className="text-2xs text-muted uppercase">{probe.kind}</span>
      {probe.state === 'declared' ? (
        <>
          <span className="font-mono wrap-anywhere">
            {probe.handler}
            {probe.target === undefined || probe.target === '' ? null : ` ${probe.target}`}
          </span>
          {timings(probe).map((part) => (
            <span key={part.title} title={part.title} data-probe-timing className="text-muted">
              {part.text}
            </span>
          ))}
        </>
      ) : probe.state === 'unreadable' ? (
        <span data-probe-problem className="wrap-anywhere">
          {probe.problem === undefined || probe.problem === '' ? 'unreadable' : probe.problem}
        </span>
      ) : (
        <span data-probe-absent>none</span>
      )}
    </span>
  );
}

/**
 * A closed Command section's line: the executable and how many arguments
 * follow it, `image entrypoint` where the spec sets no command. The argv itself
 * is never joined into one string, here or anywhere: that loses where each
 * argument ends.
 */
export function commandSummary(container: PodContainer): string {
  const command = container.command ?? [];
  const args = container.args ?? [];
  const head = command[0] ?? 'image entrypoint';
  const rest = command.length === 0 ? args.length : command.length - 1 + args.length;
  return rest === 0 ? head : `${head} · ${String(rest)} ${rest === 1 ? 'arg' : 'args'}`;
}

/**
 * A closed Probes section's line: which probes are declared and which are not,
 * grouped - `declared: liveness, readiness · none: startup`. Declared, never a
 * mark that could read as passing: whether a probe passes is the pill's
 * `ready`, and a declared readiness probe on a container that is not ready is
 * the ordinary failing case.
 */
export function probesSummary(probes: readonly ContainerProbe[]): string {
  const groups: { word: string; kinds: string[] }[] = [
    { word: 'declared', kinds: [] },
    { word: 'unreadable', kinds: [] },
    { word: 'none', kinds: [] },
  ];
  for (const probe of probes) {
    const at = probe.state === 'declared' ? 0 : probe.state === 'unreadable' ? 1 : 2;
    groups[at]?.kinds.push(probe.kind);
  }
  return groups
    .filter((group) => group.kinds.length > 0)
    .map((group) => `${group.word}: ${group.kinds.join(', ')}`)
    .join(' · ');
}

interface Pair {
  name: string;
  request: string | undefined;
  limit: string | undefined;
}

/** Requests and limits paired by resource, in the server's order. */
export function pairsOf(requests: readonly ResourceAmount[], limits: readonly ResourceAmount[]): Pair[] {
  const out: Pair[] = [];
  for (const amount of [...requests, ...limits]) {
    if (out.some((pair) => pair.name === amount.name)) continue;
    out.push({
      name: amount.name,
      request: requests.find((one) => one.name === amount.name)?.value,
      limit: limits.find((one) => one.name === amount.name)?.value,
    });
  }
  return out;
}

/** `cpu 100m · memory 256Mi→512Mi`, or `BestEffort`, for a folded summary. */
export function resourcesSummary(requests: readonly ResourceAmount[], limits: readonly ResourceAmount[]): string {
  const pairs = pairsOf(requests, limits);
  if (pairs.length === 0) return 'BestEffort';
  return pairs
    .map((pair) => `${pair.name} ${[pair.request ?? '–', pair.limit ?? '–'].join(' / ')}`)
    .join(' · ');
}

/** One chip per resource, `cpu req 100m lim 500m`; nothing declared is the BestEffort chip. */
export function ResourceChips({
  requests,
  limits,
}: {
  requests: readonly ResourceAmount[];
  limits: readonly ResourceAmount[];
}): ReactNode {
  const pairs = pairsOf(requests, limits);
  if (pairs.length === 0) {
    return (
      <span data-qos="besteffort" className="rounded-sm border border-dashed border-line px-1.5 py-px text-xs text-muted">
        BestEffort
      </span>
    );
  }
  return (
    <>
      {pairs.map((pair) => (
          <span
            key={pair.name}
            data-resource={pair.name}
            className="inline-flex max-w-full min-w-0 flex-wrap items-center gap-x-1.5 rounded-sm border border-line px-1.5 py-px text-xs"
          >
            <Icon
              name={pair.name === 'cpu' ? 'cpu' : pair.name === 'memory' ? 'memory' : 'disk'}
              className="size-3 text-muted"
            />
            <span className="text-2xs text-muted">{pair.name}</span>
            {pair.request === undefined ? null : (
              <span data-amounts="requests" className="font-mono">
                <span className="font-sans text-2xs text-muted">req </span>
                {pair.request}
              </span>
            )}
            {pair.limit === undefined ? null : (
              <span data-amounts="limits" className="font-mono">
                <span className="font-sans text-2xs text-muted">lim </span>
                {pair.limit}
              </span>
            )}
          </span>
      ))}
    </>
  );
}
