import { useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';

import { metricsQuery, useMetricsPanel, type MetricsPanel } from '../../api';
import { Dialog } from '../../components/Dialog';
import { Icon, type IconName } from '../../components/Icon';
import { btnSecondary, btnToggle } from '../../components/buttons';
import { focusRingTight } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import { useIntent } from '../../router/prefetch';
import { NoMetrics } from '../../state/NoMetrics';
import { ValueView } from '../../state/Result';
import { StateNotice } from '../../state/StateNotice';
import { formatInstant } from '../../components/instant';

/**
 * The measurements for one object: an icon row, a span row, and one chart.
 *
 * It is a separate read rather than part of the drawer: two
 * Prometheus range queries over a week take a second or more, and through a
 * port-forwarded tunnel the tunnel is opened first. Folded into the object, every
 * pod anybody clicked would pay that before showing a name.
 *
 * ## A measurement that cannot be read is a disabled control carrying why
 *
 * Never a hidden one, and never a chart drawing a flat line at zero: a
 * flat line for a container pushing 40MB/s is *a confident wrong answer*. So
 * `available: false` draws the icon grayed with its `reason` on the control, and
 * the user sees a question rather than an answer.
 *
 * ## Which measurement is drawn is a client choice; which span is asked for is not
 *
 * The operation takes `range` and `points` and **no metric**: the panel comes
 * back carrying every series it could read, so picking `memory` redraws what is
 * already here. A span is the opposite - it changes what is asked for, so the
 * row of spans refetches, and the window rides in the query key because the old
 * window's samples under the new one's caption is exactly the drift this panel
 * exists to avoid.
 *
 * ## A span change keeps the last chart, and says so
 *
 * Changing the range takes a second or so, which is Prometheus's; what made it
 * *look* broken would be the panel dropping to its waiting state for that
 * second. So `useMetricsPanel` keeps the previous answer
 * as a placeholder while the next one loads, the pressed button already shows
 * the span that was asked for, and the row says *settling* beside it - the chart
 * on screen is the last true answer, marked as not yet the new one. Resting on
 * a span button reads that span ahead through `router/prefetch.ts`'s intent
 * delay, so the click that follows usually finds it already here.
 *
 * The server already scales the step with the span - `internal/overview.Windows`
 * lands every window between 60 and 168 samples, and a test there pins it - so
 * a 7-day query costs Prometheus the same as a 1-hour one and `points` is not
 * sent from here.
 *
 * ## Click to zoom
 *
 * The chart is a button and opens the same lines in `components/Dialog` at most
 * of the viewport, with the axes labeled and a readout per sample. The dialog
 * owns Escape, the veil and giving the keyboard back; `Panel` owns the `open`
 * flag, and the dialog carries the span buttons and the measurement switch
 * too, sharing the panel's span. The large chart is not a second payload from
 * a second route: it is the numbers already on
 * screen, drawn larger - `getMetricsPanel`'s own description says the zoom "is
 * the same data over a different window, so it is a parameter here and not an
 * endpoint", and the span buttons are that parameter.
 *
 * ## The client draws and never rescales
 *
 * `Series` arrives as `{startedAt, stepSeconds, values}` - a start, a step and
 * the readings. The x positions are therefore arithmetic on the step and not a
 * second opinion about time: a second copy of the scale puts a reading a few pixels off, and the
 * only symptom is a number that looks slightly wrong.
 *
 * ## A chart is the lines under one icon, and an absent line is its own absence
 *
 * Lines are grouped by `chart` - the icon-row key the server stamped on each -
 * and the network is two of them, receive and transmit on one pair of axes,
 * because the question is which way the traffic is going and two charts with
 * independent scales answer it wrongly.
 *
 * There is no `find(key === wanted) ?? series[0]` fallback: a chosen
 * measurement with no line under it would draw the first series, which is CPU,
 * under the Network icon with nothing on screen to say so - a chart of the
 * wrong thing that looks right. A measurement with no line draws *its*
 * absence, with the server's sentence where it sent one, and never another
 * measurement.
 *
 * ## Nothing is drawn under a measurement nobody can read
 *
 * No "Nothing here" notice is drawn under every line that has no samples (once
 * per line, three times on a pod nothing measured). The reason belongs on the
 * control instead:
 *
 * - **Neither Prometheus nor metrics-server**: CPU and memory arrive refused,
 *   like the network, with the server's reason on hover, and nothing is drawn
 *   beneath the row. The opening measurement is the first one that can be read.
 * - **metrics-server alone**: CPU and memory stay offered and show the figure of
 *   now, and the span buttons are grayed with *no Prometheus detected* on hover,
 *   because no span of history can be chosen. That is decided from the CPU and
 *   memory lines' reason code - `not_installed` there means there is no
 *   Prometheus to ask (`lineReason` in `internal/web/apireads.go`) - and never
 *   from a window that came back empty, where a wider span is the one control
 *   that might find samples.
 */
export function Metrics({
  cluster,
  kind,
  namespace,
  name,
  silent = false,
}: {
  cluster: string;
  kind: string;
  namespace: string | undefined;
  name: string;
  /** Draw nothing at all unless there is a line to draw: no notice while
   *  waiting, none for a missing backend, an empty window or a failed read, and
   *  no sentence about a span that could not be read. For a view whose absence
   *  is by design the whole answer (a claim's storage chart). */
  silent?: boolean;
}): ReactNode {
  /* The span is state here and the metric is not, because the span is what the
     request carries. `undefined` means "whatever the server picks", which is the
     first render and is not the same as any particular span. */
  const [range, setRange] = useState<string | undefined>(undefined);
  const client = useQueryClient();
  const query = useMetricsPanel(
    cluster,
    kind,
    namespace,
    name,
    range === undefined ? {} : { range },
    true,
  );

  /* A span read that failed, or answered a
     refusal, dropped the panel to `ValueView`'s notice - and the panel is
     where the picked measurement and the open dialog live, so both went with
     it. So the span drawn before each pick is remembered (`drawn`), only when
     it was an answer that could be drawn, and a pick that fails draws that
     span's answer, still in the cache, with one line saying the pick could not
     be read: the panel is never unmounted on the way. Picking the failed span
     again asks again.

     **And the answer is kept with the span**: the cache
     lets go of a read nothing observes after its `gcTime`, so a pick that
     failed after the drawn span's entry had been collected found nothing to
     draw and unmounted the panel all the same. The cache's entry is still
     preferred, being the fresher where both exist.

     **The kept answer is the object's it was read for, and no other.**
     The drawer reuses this instance when another
     object is opened, so an answer kept for pod A, drawn on pod B's failed
     read, was A's chart under B's name. It is kept with the object's identity,
     and an answer for another object is never drawn: that read falls through
     to `ValueView`'s notice. */
  const object = `${cluster}/${kind}/${namespace ?? ''}/${name}`;
  const answer = query.data;
  const ok = answer?.state === 'ok' ? answer.data : undefined;
  const [remembered, setDrawn] = useState<
    { object: string; range: string | undefined; panel: MetricsPanel } | undefined
  >(undefined);
  const drawn = remembered?.object === object ? remembered : undefined;
  const refusal =
    answer !== undefined && answer.state !== 'ok'
      ? (answer.reason?.message ?? `the server answered ${answer.state}`)
      : answer === undefined && query.isError
        ? query.error.message
        : undefined;
  const kept =
    refusal === undefined
      ? undefined
      : client.getQueryData(
          metricsQuery(cluster, kind, namespace, name, drawn?.range === undefined ? {} : { range: drawn.range })
            .queryKey,
        );
  const fallback = refusal === undefined ? undefined : kept?.state === 'ok' ? kept.data : drawn?.panel;

  const panel = ok ?? fallback;
  if (silent && (panel === undefined || !drawable(panel))) return null;
  if (panel === undefined) {
    // Nothing drawn yet. A first answer that is a refusal is one quiet line
    // (#127): a metrics backend missing or not reached is not a cluster
    // failure. Waiting, `pending` and a request that failed outright stay
    // `ValueView`'s to say, as everywhere else.
    // The person's own `get` refused is not an absence of metrics (#156):
    // the console's refusal notice, as on any page they may not read.
    if (answer !== undefined && answer.state === 'forbidden' && answer.reason?.scope?.verb !== undefined) {
      return <StateNotice state={answer.state} reason={answer.reason} compact />;
    }
    if (answer !== undefined && answer.state !== 'ok' && answer.state !== 'pending') {
      return <NoMetrics state={answer.state} reason={answer.reason} onRetry={() => void query.refetch()} />;
    }
    return (
      <ValueView query={query} what="these metrics">
        {() => null}
      </ValueView>
    );
  }
  return (
    <Panel
      panel={panel}
      // The buttons say what is drawn: on a failed pick, the span kept.
      range={ok === undefined ? drawn?.range : range}
      onRange={(next) => {
        if (next === range) {
          if (refusal !== undefined) void query.refetch();
          return;
        }
        if (ok !== undefined && !query.isPlaceholderData) setDrawn({ object, range, panel: ok });
        setRange(next);
      }}
      settling={query.isPlaceholderData}
      failure={silent ? undefined : ok === undefined && refusal !== undefined ? `${range ?? 'This span'} could not be read: ${refusal}` : undefined}
      prefetch={(window) =>
        void client.prefetchQuery(metricsQuery(cluster, kind, namespace, name, { range: window }))
      }
    />
  );
}

/** Whether any line of the answer carries a sample. */
function drawable(panel: MetricsPanel): boolean {
  return (panel.series ?? []).some((line) => line.data.values.some((one) => one !== null));
}

function Panel({
  panel,
  range,
  onRange,
  settling,
  failure,
  prefetch,
}: {
  panel: MetricsPanel;
  range: string | undefined;
  onRange: (key: string) => void;
  /** True while the chart on screen is the previous span's, kept as a
   *  placeholder for the one that was asked for. */
  settling: boolean;
  /** Why the span last asked for could not be read, where it could not; the
   *  chart on screen is then the last one that could. */
  failure: string | undefined;
  /** Read one span ahead, without choosing it. */
  prefetch: (window: string) => void;
}): ReactNode {
  const series = panel.series ?? [];
  const choices = panel.metrics ?? [];
  const windows = panel.windows ?? [];
  const noHistory = historyRefused(series);

  /* Which series is drawn. The server's `selected` is the opening position and
     the operator's click wins after that - so `undefined` here means "the one
     the server chose", and a panel refetched at a wider span does not snap back
     to CPU under the operator's hand. A pick the new answer refuses gives way
     to the server's choice, and a panel where nothing can be read draws
     nothing: an absent measurement is drawn as a grayed control, not as a
     notice under it. */
  const [picked, setPicked] = useState<string | undefined>(undefined);
  const wanted = openingChoice(choices, series, picked);
  const choice = choices.find((one) => one.key === wanted);
  const label = choice?.label ?? wanted ?? '';
  // Every line under the chosen icon, and nothing else. No `?? series[0]`: see
  // the header. An empty list here is drawn as the absence of `wanted`.
  const lines = series.filter((one) => chartOf(one) === wanted);

  /* Whether the enlarged chart is open. Here and not in `Chart`: the dialog carries the same span and measurement controls as the
     panel, so a click inside it refetches - and a `Chart` that is replaced by a
     muted line when a window comes back empty would take its dialog with it.
     The panel outlives every answer, so the dialog does too. */
  const [open, setOpen] = useState(false);

  // One set of controls, drawn twice: in the panel and in the dialog over it.
  // The span is the panel's own state, so a span picked in the dialog is the
  // span the small chart behind it shows once the dialog closes. Each copy
  // names its groups for where it is, so a screen reader is not offered two
  // identical "Measurement" groups while the dialog is open.
  const controls = (where: 'panel' | 'enlarged'): ReactNode => (
    <Controls
      where={where}
      choices={choices}
      wanted={wanted}
      onPick={setPicked}
      windows={windows}
      range={range}
      onRange={onRange}
      prefetch={prefetch}
      settling={settling}
      noHistory={noHistory}
      failure={failure}
    />
  );

  return (
    <div className="flex flex-col gap-3">
      {/* `inert` while the enlarged chart is open: the veil covers
          this half, and nothing under it may take focus or be read out as if
          it were there. The dialog is outside it, below. */}
      <div inert={open} data-metrics-behind className="flex flex-col gap-3">
      <header className="flex flex-wrap items-center gap-3">{controls('panel')}</header>

      {wanted === undefined ? null : lines.length === 0 ? (
        // A measurement the row offers and the answer carries no line for: out
        // of contract, said in one muted line rather than papered over with
        // whatever line came first - and not as a "Nothing here" notice.
        <p role="status" className="text-xs text-muted">
          No {label} line came back in this answer, so nothing is drawn rather than another measurement.
        </p>
      ) : (
        <Chart
          label={label}
          lines={lines}
          quiet={noHistory !== undefined}
          onOpen={() => setOpen(true)}
          offer={periodOffer(lines, windows, range, onRange, prefetch)}
        />
      )}

      {panel.caption === undefined || panel.caption === '' ? null : (
        <p className="text-xs text-muted">{panel.caption}</p>
      )}
      </div>

      {open ? (
        /* `Overlay`, which is the row `keyboard/layers.ts` writes this chart
           into by name: the full-size chart yields to
           nothing. Without it the chart takes the `Dialog` default and shares a
           layer with the manifest editor, which is only harmless while nothing
           opens it from inside one. */
        <Dialog title={label === '' ? 'Metrics' : label} onClose={() => setOpen(false)} size="viewport" layer={KeyLayer.Overlay}>
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-3">{controls('enlarged')}</div>
            <Enlarged label={label} lines={lines} />
          </div>
        </Dialog>
      ) : null}
    </div>
  );
}

/**
 * The measurement switch and the span buttons, with the *settling* marker.
 *
 * A component because it is drawn twice: in the panel, and in the enlarged
 * chart over it. The dialog's veil covers the
 * panel's buttons, so without a second copy changing the span would mean
 * closing the dialog first. Both copies drive the panel's own state, which is what makes the span
 * shared - the small chart behind the dialog follows it - and the prefetch on
 * intent and the *settling* marker are the same in both, because they are the
 * same component.
 */
function Controls({
  where,
  choices,
  wanted,
  onPick,
  windows,
  range,
  onRange,
  prefetch,
  settling,
  noHistory,
  failure,
}: {
  /** Which copy this is, for the groups' accessible names. */
  where: 'panel' | 'enlarged';
  choices: NonNullable<MetricsPanel['metrics']>;
  wanted: string | undefined;
  onPick: (key: string) => void;
  windows: NonNullable<MetricsPanel['windows']>;
  range: string | undefined;
  onRange: (key: string) => void;
  prefetch: (window: string) => void;
  settling: boolean;
  noHistory: string | undefined;
  failure: string | undefined;
}): ReactNode {
  const suffix = where === 'enlarged' ? ', enlarged chart' : '';
  return (
    <>
      {choices.length === 0 ? null : (
        <div role="group" aria-label={`Measurement${suffix}`} className="flex gap-1">
          {choices.map((choice) => {
            const glyph = GLYPH[choice.key];
            const on = choice.key === wanted;
            return (
              <button
                key={choice.key}
                type="button"
                // Disabled and carrying why, rather than absent. Four icons of
                // which two are gray is a question; three icons is a console
                // that has quietly decided for you.
                disabled={choice.available !== true}
                aria-pressed={on}
                title={choice.available === true ? choice.label : refusal(choice)}
                onClick={() => onPick(choice.key)}
                className={`${btnToggle(on)} disabled:border-line disabled:text-muted`}
              >
                {glyph === undefined ? null : <Icon name={glyph} />}
                {choice.label}
              </button>
            );
          })}
        </div>
      )}

      {windows.length === 0 ? null : (
        <div role="group" aria-label={`Window${suffix}`} className="flex items-center gap-1">
          {windows.map((window) => (
            <WindowButton
              key={window.key}
              label={window.label}
              // The pressed button is the span that was *asked for*, from the
              // first render on: while the answer settles, the chart is the
              // old span's and the button is the new one's, and the marker
              // beside the row is what reconciles the two.
              on={range === undefined ? window.selected === true : window.key === range}
              onPick={() => onRange(window.key)}
              onIntent={() => prefetch(window.key)}
              refused={noHistory}
            />
          ))}
          {/* Not a spinner over the chart: the answer on screen is still the
              last true answer, and blanking it would be a worse lie than
              saying that a newer one is on its way. The same marker, and the
              same sentence, as `FilterBox`. */}
          <span aria-live="polite" className="text-xs text-muted">
            {settling ? 'settling…' : ''}
          </span>
        </div>
      )}

      {failure === undefined ? null : (
        // Beside the buttons, in both copies, because it is about the span:
        // the chart on screen is the last span that could be read.
        <p role="status" data-span-failed className="w-full text-xs text-warn wrap-anywhere">
          {failure}
        </p>
      )}
    </>
  );
}

/**
 * One span button. A component rather than a loop body because the intent
 * handlers are a hook, and the hook is what makes resting on `24h` read that
 * window ahead without choosing it.
 */
function WindowButton({
  label,
  on,
  onPick,
  onIntent,
  refused,
}: {
  label: string;
  on: boolean;
  onPick: () => void;
  onIntent: () => void;
  /** Why no span can be chosen, when none can: the server's sentence for a
   *  cluster with no Prometheus. The button is grayed and carries it. */
  refused?: string | undefined;
}): ReactNode {
  const intent = useIntent(onIntent);
  return (
    <button
      type="button"
      aria-pressed={on}
      disabled={refused !== undefined}
      title={refused === undefined ? undefined : `No Prometheus detected: ${refused}`}
      onClick={onPick}
      {...(refused === undefined ? intent : {})}
      className={`${btnToggle(on)} disabled:border-line disabled:text-muted`}
    >
      {label}
    </button>
  );
}

type Line = MetricsPanel['series'][number];

/**
 * The measurement drawn: the operator's pick while the answer still offers it,
 * then the server's `selected`, then the first the row offers. `undefined` when
 * the row offers nothing, and then nothing is drawn beneath it. A panel from a
 * server that sends no row at all draws its first line.
 */
function openingChoice(
  choices: NonNullable<MetricsPanel['metrics']>,
  series: readonly Line[],
  picked: string | undefined,
): string | undefined {
  if (choices.length === 0) return chartOf(series[0]);
  const usable = choices.filter((one) => one.available);
  if (picked !== undefined && usable.some((one) => one.key === picked)) return picked;
  return usable.find((one) => one.selected === true)?.key ?? usable[0]?.key;
}

/**
 * Why no span of history can be chosen, or `undefined` where one can.
 *
 * True only when nothing in the panel was drawn **and** the CPU and memory
 * lines both say `not_installed` - which on those two lines means there is no
 * Prometheus to ask, because they have no counter probe to be missing from
 * (`lineReason`, `internal/web/apireads.go`). An empty window says
 * `empty_scope` and a Prometheus that failed says `cluster_unreachable`, and
 * neither grays the spans: there, a wider window is worth asking for.
 */
function historyRefused(series: readonly Line[]): string | undefined {
  if (series.some((line) => line.data.values.some((one) => one !== null))) return undefined;
  const usage = series.filter((line) => chartOf(line) === 'cpu' || chartOf(line) === 'memory');
  if (usage.length === 0 || !usage.every((line) => line.reason?.code === 'not_installed')) return undefined;
  return usage[0]?.reason?.message;
}

/** The icon-row key a line is drawn under. Absent means the line is its own
 *  chart, which the contract says is every chart but the network. */
function chartOf(line: Line | undefined): string | undefined {
  if (line === undefined) return undefined;
  return line.chart ?? line.key;
}

/**
 * The lines of one chart on one 0-to-peak scale.
 *
 * **The viewBox is the sample space and the element is sized by CSS**, so the
 * chart scales with the drawer without the client recomputing a single
 * coordinate. `preserveAspectRatio="none"` is what makes that work and is also
 * why the stroke is drawn with `vector-effect`: a non-uniform scale would
 * otherwise stretch the line's thickness with it.
 *
 * A missing reading is a gap, not a zero - `null` in `values` breaks the line
 * rather than dropping it to the floor, for the same reason the icon row refuses
 * to draw an unavailable measurement.
 *
 * **One scale across every line.** The peak is the peak of all of them, so a
 * kilobyte of egress is drawn a thousandth as tall as a megabyte of ingress
 * and not as tall as it - which is the whole reason the two share a chart.
 *
 * **A line with nothing in it gets a legend row carrying its reason**, not
 * silence. An omitted line is indistinguishable from a line at zero, and for
 * network traffic those are opposite claims - `internal/overview/lines.go`
 * says so in the same words, and this follows that rule.
 *
 * **Click to zoom.** The chart is a button; pressing it opens the same lines in
 * a `Dialog` at most of the viewport, with the axes labeled and a readout per
 * sample. The large chart is the *same* `<svg>`
 * - same sample space, same scale, same points - sized by CSS; the only things
 * added are the axis labels beside it and the hover targets inside it, so
 * nothing about the picture is recomputed between the two sizes.
 */
function Chart({
  label,
  lines,
  quiet,
  onOpen,
  offer,
}: {
  label: string;
  lines: readonly Line[];
  /** The reason there is no series is already on the span buttons, so a line's
   *  reading of now does not say it again. */
  quiet: boolean;
  /** Open the enlarged chart, which `Panel` owns. */
  onOpen: () => void;
  /** The period the empty lines' sentence names, as a button, where it names
   *  one the row offers. */
  offer?: PeriodOffer | undefined;
}): ReactNode {
  const measured = measure(lines);

  if (measured === undefined) {
    // Nothing to draw on any line: one muted sentence per line, the server's
    // own. Two lines refused for two different reasons - a counter that is not
    // exported and a window that is empty - are two facts. Not a "Nothing
    // here" notice each: the heading added nothing the
    // sentence does not say, and drew it three times on one pod.
    //
    // **A line with a reading of now draws that reading**.
    // Usage and history are two questions, and a cluster with metrics-server
    // and no Prometheus answers the first.
    //
    // **A finished pod's run is one line of figures**: what each network
    // line counted over the whole run, side by side - `Network in 3 MiB ·
    // Network out 340 KiB` - and nothing else. The server's sentence about the
    // window is not repeated under each: the figure is the answer to the
    // question an empty chart of a finished pod asks. A line with no total
    // keeps its sentence, which is then the only thing said about it. The
    // period the sentences name is a button only where the server kept one,
    // which it does only where that period draws something.
    const counted = lines.filter((line) => readingOf(line.runTotal) !== undefined);
    return (
      <div className="flex flex-col gap-2">
        {counted.length === 0 ? null : <RunTotals lines={counted} />}
        {lines.map((line) =>
          readingOf(line.runTotal) !== undefined ? null : readingOf(line.current) === undefined ? (
            <p key={line.key} role="status" className="text-xs text-muted" title={whyTitle(line.reason)}>
              {line.label}: {shortState(line.reason)}
            </p>
          ) : (
            <Now key={line.key} line={line} quiet={quiet} />
          ),
        )}
        {offer === undefined ? null : <PeriodButton offer={offer} />}
      </div>
    );
  }

  return (
    <figure className="flex flex-col gap-1">
      <Legend lines={lines} measured={measured} />

      <button
        type="button"
        aria-label={`Open ${label} full size`}
        title="Click to open full size"
        onClick={onOpen}
        className={`block w-full cursor-zoom-in rounded-md ${focusRingTight}`}
      >
        <Lines label={label} lines={lines} measured={measured} className="h-24 w-full" />
      </button>

      <p className="text-xs text-muted">
        {measured.longest} samples, one every {humanise(measured.stepSeconds)}, from{' '}
        {when(measured.startedAt)}
        {measured.unit === undefined || measured.unit === '' ? '' : ` · in ${measured.unit}`}
      </p>

    </figure>
  );
}

/**
 * What the lines share: one scale, one sample space, one axis. `undefined`
 * when no line has a single reading - the caller draws the reasons instead.
 */
interface Measured {
  /** The finite readings of each line, by index. */
  readings: readonly (readonly number[])[];
  /** The peak across every line; the top of the one scale. */
  peak: number;
  /** The divisor. The peak, floored at one so an idle container is a line on
   *  the axis and not NaN. */
  scale: number;
  /** The most samples any line has; the width of the sample space. */
  longest: number;
  stepSeconds: number;
  startedAt: string;
  unit: string | undefined;
}

function measure(lines: readonly Line[]): Measured | undefined {
  const readings = lines.map((line) =>
    line.data.values.filter((one): one is number => one !== null && Number.isFinite(one)),
  );
  const drawable = lines.filter((_, index) => (readings[index]?.length ?? 0) > 0);
  if (drawable.length === 0) return undefined;

  const peak = Math.max(...readings.flat());
  // A zero peak is a real answer - an idle container - and dividing by it is
  // not. The floor keeps the line on the axis instead of producing NaN for every
  // point, which renders as nothing at all with no error anywhere.
  const scale = peak > 0 ? peak : 1;
  const longest = Math.max(...lines.map((line) => line.data.values.length));
  // The axis is the first drawable line's: every line of one chart was asked
  // for at the same step over the same window, so the caption is one sentence.
  const axis = drawable[0]?.data;
  return {
    readings,
    peak,
    scale,
    longest,
    stepSeconds: axis?.stepSeconds ?? 0,
    startedAt: axis?.startedAt ?? '',
    unit: drawable[0]?.unit,
  };
}

/** One row per line: swatch, name, and either its last and peak or the reason
 *  it has nothing. */
function Legend({ lines, measured }: { lines: readonly Line[]; measured: Measured }): ReactNode {
  return (
    <figcaption className="flex flex-col gap-0.5 text-xs">
      {lines.map((line, index) => {
        const tone = toneOf(index);
        const own = measured.readings[index] ?? [];
        const last = [...line.data.values].reverse().find((one): one is number => one !== null);
        return (
          <span key={line.key} className="flex items-baseline justify-between gap-3">
            <span className="flex items-center gap-1.5">
              {lines.length > 1 ? (
                // The swatch, so the legend can be read back onto the lines.
                <span aria-hidden="true" className={`inline-block h-0.5 w-3 ${tone.swatch}`} />
              ) : null}
              {line.label}
              {line.data.trustworthy === false && own.length > 0 ? (
                // The contract's own flag. A reading the source itself will
                // not vouch for is drawn, and said so - hiding it would be the
                // silent half of the same defect.
                <span className="text-warn"> · the source does not vouch for this</span>
              ) : null}
            </span>
            {own.length === 0 ? (
              // This line's own absence, in the server's words. Never omitted:
              // an omitted line is indistinguishable from a line at zero.
              <span role="status" className="text-right text-muted" title={whyTitle(line.reason)}>
                {shortState(line.reason)}
              </span>
            ) : (
              <span className="text-muted tabular-nums">
                {last === undefined ? '—' : format(last, line.unit)} now · peak{' '}
                {format(Math.max(...own), line.unit)}
              </span>
            )}
          </span>
        );
      })}
    </figcaption>
  );
}

/**
 * The picture: every drawable line as a polyline in the sample space. `readouts`
 * adds one invisible column per sample carrying a `<title>` - the browser's own
 * tooltip, which is the simplest thing that makes every point hoverable and
 * needs no positioning arithmetic of its own (no stylesheet, and no
 * second copy of the scale).
 */
function Lines({
  label,
  lines,
  measured,
  className,
  readouts = false,
}: {
  label: string;
  lines: readonly Line[];
  measured: Measured;
  className: string;
  readouts?: boolean;
}): ReactNode {
  const width = Math.max(measured.longest - 1, 1);
  const spanSeconds = Math.round((measured.longest - 1) * measured.stepSeconds);
  const columns = readouts ? Array.from({ length: measured.longest }, (_, index) => index) : [];
  return (
    <svg
      role="img"
      aria-label={`${label} over the last ${humanise(spanSeconds)}, from ${when(measured.startedAt)}`}
      viewBox={`0 0 ${width} 1`}
      preserveAspectRatio="none"
      className={`rounded-md border border-line bg-panel ${className}`}
    >
      {lines.map((line, index) => {
        if ((measured.readings[index]?.length ?? 0) === 0) return null;
        const tone = toneOf(index);
        return (
          <polyline
            key={line.key}
            points={pointsOf(line.data.values, measured.scale)}
            className={`fill-none ${tone.stroke}`}
            vectorEffect="non-scaling-stroke"
            strokeWidth={1.5}
            strokeLinejoin="round"
            strokeDasharray={tone.dash}
          />
        );
      })}
      {columns.map((index) => (
        // A column half a sample wide either side of the point, the full height
        // of the chart: resting anywhere in it reads that moment. Drawn after
        // the lines so it is what the pointer meets.
        <rect
          key={index}
          x={index - 0.5}
          y={0}
          width={1}
          height={1}
          className="fill-transparent hover:fill-accent/20"
        >
          <title>{readout(lines, measured, index)}</title>
        </rect>
      ))}
    </svg>
  );
}

/** What every line read at one moment, as one sentence for the tooltip. */
function readout(lines: readonly Line[], measured: Measured, index: number): string {
  const at = new Date(measured.startedAt).getTime() + index * measured.stepSeconds * 1000;
  const moment = Number.isNaN(at) ? `sample ${String(index + 1)}` : when(new Date(at).toISOString());
  const parts = lines.map((line) => {
    const value = line.data.values[index];
    return `${line.label}: ${value === null || value === undefined ? '—' : format(value, line.unit)}`;
  });
  return `${moment} · ${parts.join(' · ')}`;
}

/**
 * What the dialog draws under its controls: the full-size chart where this
 * window has samples, and one muted line where it has none. The dialog stays
 * open over a span that came back empty - closing it would take the span
 * buttons away with it - and says so rather than drawing an empty frame.
 */
function Enlarged({ label, lines }: { label: string; lines: readonly Line[] }): ReactNode {
  const measured = measure(lines);
  if (measured === undefined) {
    return (
      <p role="status" className="text-xs text-muted">
        {NO_METRICS}
      </p>
    );
  }
  return <Zoom label={label} lines={lines} measured={measured} />;
}

/**
 * The full-size view: the same lines, the axes labeled, a readout per sample.
 *
 * The Y labels sit beside the `<svg>` as HTML rather than inside it, because
 * the picture is drawn with `preserveAspectRatio="none"` and text inside it
 * would stretch with the samples. Peak, half and zero are the three values the
 * scale is made of, so they are the three the axis says.
 */
function Zoom({ label, lines, measured }: { label: string; lines: readonly Line[]; measured: Measured }): ReactNode {
  const spanSeconds = Math.round((measured.longest - 1) * measured.stepSeconds);
  const endedAt = new Date(new Date(measured.startedAt).getTime() + spanSeconds * 1000);
  // Peak, half and zero are three points on one scale, so they are printed in
  // one multiple - the peak's. See render.
  const axis = multipleOf(measured.unit, measured.peak);
  return (
    <div className="flex flex-col gap-2">
      <Legend lines={lines} measured={measured} />
      <div className="flex gap-2">
        <div className="flex flex-col justify-between text-right text-xs text-muted tabular-nums">
          <span>{render(measured.peak, axis)}</span>
          <span>{render(measured.peak / 2, axis)}</span>
          <span>{render(0, axis)}</span>
        </div>
        <Lines label={label} lines={lines} measured={measured} className="h-[60vh] min-w-0 flex-1" readouts />
      </div>
      <div className="flex justify-between text-xs text-muted">
        <span>{when(measured.startedAt)}</span>
        <span>
          {measured.longest} samples, one every {humanise(measured.stepSeconds)}
        </span>
        <span>{Number.isNaN(endedAt.getTime()) ? '' : when(endedAt.toISOString())}</span>
      </div>
    </div>
  );
}

/** The polyline for one run of samples on a shared 0-to-peak scale. Index is the
 *  x, so the position is arithmetic on the step and never a second opinion
 *  about time. */
function pointsOf(values: readonly (number | null)[], scale: number): string {
  return values
    .map((one, index) => (one === null ? null : `${index},${(1 - one / scale).toFixed(4)}`))
    .filter((one): one is string => one !== null)
    .join(' ');
}

/**
 * The figure a line's `current` reading carries, or `undefined` where it carries
 * none that can be believed. The same predicate the overview's readings go
 * through: a reading the server would not vouch for, or a non-finite one, is not
 * drawn as a number.
 */
function readingOf(reading: Line['current']): number | undefined {
  if (reading === undefined || !reading.trustworthy) return undefined;
  const value = reading.value;
  return value !== null && Number.isFinite(value) ? value : undefined;
}

/**
 * One measurement as it stands now, on a line with no series to draw: the
 * figure, the word *now*, and the one sentence saying why there is no line.
 *
 * Not a chart and not drawn like one. A single reading is not a flat line, and
 * a sparkline of one point would imply a history the cluster said it does not
 * keep.
 */
function Now({ line, quiet }: { line: Line; quiet: boolean }): ReactNode {
  const value = readingOf(line.current);
  return (
    <div data-reading="now" className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className="flex flex-wrap items-baseline gap-2">
        <span className="text-muted">{line.label}</span>
        <span className="text-lg font-semibold tabular-nums">
          {line.current?.text ?? (value === undefined ? '—' : format(value, line.unit))}
        </span>
        <span className="text-xs text-muted">now</span>
      </p>
      {quiet || line.reason?.message === undefined || line.reason.message === '' ? null : (
        <p className="mt-1 text-xs text-muted" title={whyTitle(line.reason)}>
          {shortState(line.reason)}
        </p>
      )}
    </div>
  );
}

/**
 * What a finished pod's network lines counted over the whole run, on a chart
 * with nothing to draw in the period on screen: one compact line, each label
 * and its figure, joined by a dot. Not a box per line and not the server's
 * sentence under each: two boxes repeating one sentence about a window said
 * less than the two figures do.
 */
function RunTotals({ lines }: { lines: readonly Line[] }): ReactNode {
  return (
    <p data-reading="run" title="Over the pod's whole run" className="text-sm">
      {lines.map((line, index) => (
        <span key={line.key} className="whitespace-nowrap">
          {index === 0 ? null : <span className="text-muted"> · </span>}
          <span className="text-muted">{line.label}</span>{' '}
          <span className="font-semibold tabular-nums">{line.runTotal?.text ?? '—'}</span>
        </span>
      ))}
    </p>
  );
}

interface PeriodOffer {
  label: string;
  onPick: () => void;
  onIntent: () => void;
}

/**
 * The period an empty chart's lines name, where the row offers it and it is
 * not already the one asked for. One per chart: the network's two lines name
 * the same run, and two buttons saying `24h` would be one question asked twice.
 */
function periodOffer(
  lines: readonly Line[],
  windows: NonNullable<MetricsPanel['windows']>,
  range: string | undefined,
  onRange: (key: string) => void,
  prefetch: (window: string) => void,
): PeriodOffer | undefined {
  if (lines.some((line) => line.data.values.some((one) => one !== null))) return undefined;
  const asked = range ?? windows.find((one) => one.selected === true)?.key;
  const window = windows.find((one) => one.key !== asked && lines.some((line) => line.period === one.key));
  if (window === undefined) return undefined;
  return { label: window.label, onPick: () => onRange(window.key), onIntent: () => prefetch(window.key) };
}

/** The button that selects the period holding the run, exactly as pressing it
 *  in the period row would - and reads it ahead on intent, as that row does. */
function PeriodButton({ offer }: { offer: PeriodOffer }): ReactNode {
  const intent = useIntent(offer.onIntent);
  return (
    <button
      type="button"
      data-period-offer
      onClick={offer.onPick}
      {...intent}
      className={`self-start ${btnSecondary}`}
    >
      Show the {offer.label} period
    </button>
  );
}

/** What a chart with nothing to draw says on screen: two short states and no
 *  sentence (R5 section 5 keeps them apart). The server's reason, where it
 *  sent one, is the element's `title` and never on screen. */
const NO_METRICS = 'No metrics';
const METRICS_UNAVAILABLE = 'Metrics unavailable';

/** The reason codes that mean "nothing to show" rather than "the read failed". */
const EMPTY_CODES: ReadonlySet<string> = new Set(['empty_scope', 'not_installed']);

function shortState(reason: { code?: string | undefined } | undefined): string {
  return reason?.code === undefined || EMPTY_CODES.has(reason.code) ? NO_METRICS : METRICS_UNAVAILABLE;
}

function whyTitle(reason: { message?: string | undefined } | undefined): string | undefined {
  return reason?.message === undefined || reason.message === '' ? undefined : reason.message;
}

interface Tone {
  stroke: string;
  swatch: string;
  dash: string | undefined;
}

/**
 * The tones the lines of one chart are drawn in, in order. Full class strings,
 * looked up - never built by interpolation, because Tailwind finds utilities by
 * scanning source text and `stroke-${tone}` exists in the DOM and in no
 * stylesheet.
 *
 * Not chosen for meaning: these are two lines of the same measurement, and
 * neither ingress nor egress is "the bad one". The dash is what makes the pair
 * survive grayscale and color blindness; the first is solid, which is what a
 * single-line chart gets. Ported from `internal/overview/lines.go`.
 */
const TONES: readonly Tone[] = [
  { stroke: 'stroke-accent', swatch: 'bg-accent', dash: undefined },
  { stroke: 'stroke-warn', swatch: 'bg-warn', dash: '5 3' },
  { stroke: 'stroke-ok', swatch: 'bg-ok', dash: '2 2' },
  { stroke: 'stroke-bad', swatch: 'bg-bad', dash: '6 2 1 2' },
];

function toneOf(index: number): Tone {
  return TONES[Math.min(index, TONES.length - 1)] ?? { stroke: 'stroke-accent', swatch: 'bg-accent', dash: undefined };
}

/** The icons the sprite carries for a measurement. A key with no glyph draws its
 *  label alone rather than nothing: a measurement the server added and this map
 *  has not is still reachable. */
const GLYPH: Record<string, IconName | undefined> = {
  cpu: 'cpu',
  memory: 'memory',
  network: 'network',
  disk: 'disk',
  volume: 'disk',
};

function refusal(choice: { label: string; reason?: { message?: string } | undefined }): string {
  const why = choice.reason?.message;
  return why === undefined || why === '' ? `${choice.label} cannot be read` : `${choice.label}: ${why}`;
}

/**
 * Three significant figures, in the largest multiple of the server's unit that
 * keeps the figure under four digits.
 *
 * **The panel still never invents a unit**, and that rule is the reason this
 * function exists rather than a `toFixed` at each call site: a number the server
 * labeled `bytes/s` and this file called `MB` would be a conversion nobody
 * asked for and nobody can check. That rule is about *changing the quantity*,
 * not about *scaling*, which is a different act. Changing the
 * quantity — printing a rate as a level, or bytes as bits — invents a unit.
 * Moving inside one family does not: 402210 bytes/s and 392.8 KiB/s are the
 * same statement, and only one of them can be read at a glance: a reading of
 * `402210 bytes/s` makes the reader count digits to learn the figure is about
 * 400 kB/s.
 *
 * A family this file has never heard of is left exactly as it arrives — the
 * number as three significant figures and the server's own label after it —
 * because scaling a unit whose steps are unknown *is* inventing one.
 *
 * The caption under the chart still says `in bytes/s`, and now earns its place:
 * it names what was measured while the figures say how much of it.
 */
function format(value: number, unit?: string | undefined): string {
  return render(value, multipleOf(unit, value));
}

/** What to divide a figure by, and what to write after it. */
interface Multiple {
  divisor: number;
  suffix: string;
  /** Whether a space separates the figure from the suffix: `8.8 KiB` and
   *  `3.2 cores` take one, `402m` does not. Both spellings are the console's
   *  own, from `textutil.BytesCompact` and `overview.formatCores`. */
  spaced: boolean;
}

/**
 * The byte family, **binary**, because that is the decision and it is already
 * made everywhere else in this console.
 *
 * A network rate is conventionally decimal — `kB/s` — and choosing it here
 * would have been defensible on its own. It is not defensible next to the rest:
 * `textutil.BytesCompact` is what prints the memory column's `14.2 GiB`, what
 * `internal/overview.format` prints a `bytes/s` capacity with, and what labels
 * the Go-rendered sparklines. Two conventions for one unit, with nothing saying
 * which is which, is how `14.2 GiB` and `402.2 kB/s` end up beside a third
 * spelling — so this file follows the one already in the tree rather than
 * adding to them.
 */
const BYTE_MULTIPLES = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const;

/**
 * The multiple for one unit at one magnitude. Every family the panel draws is
 * here, and `unitOf` in `internal/web/apireads.go` is the list it has to match:
 * `mcores`, `bytes`, `bytes/s`, and the empty label a plain count arrives with.
 */
function multipleOf(unit: string | undefined, magnitude: number): Multiple {
  const size = Math.abs(magnitude);
  if (unit === 'bytes' || unit === 'bytes/s') {
    const rate = unit === 'bytes/s' ? '/s' : '';
    let divisor = 1;
    let step = 0;
    while (step < BYTE_MULTIPLES.length - 1 && size / divisor >= 1024) {
      divisor *= 1024;
      step += 1;
    }
    return { divisor, suffix: `${BYTE_MULTIPLES[step] ?? 'B'}${rate}`, spaced: true };
  }
  if (unit === 'mcores') {
    // `overview.formatCores`: a request is written in millicores under a core
    // and in cores above one, which is how node capacity is discussed.
    return size >= 1000 ? { divisor: 1000, suffix: 'cores', spaced: true } : { divisor: 1, suffix: 'm', spaced: false };
  }
  return { divisor: 1, suffix: unit ?? '', spaced: true };
}

/**
 * One figure in a multiple that was already chosen, which is what an axis
 * needs: its three labels are peak, half-peak and zero on **one** scale, so
 * they take one multiple — the peak's — or the axis reads
 * `8.8 KiB/s · 4.4 KiB/s · 0 B/s` and its last step is a different size from
 * its first. A legend and a tooltip go through `format` instead and take each
 * figure's own multiple, so a line idling at 40 B/s under a 400 kB/s peak says
 * `40 B/s` and not `0.0 KiB/s`.
 */
function render(value: number, multiple: Multiple): string {
  const scaled = value / multiple.divisor;
  const size = Math.abs(scaled);
  const figure = trimZeros(size >= 100 ? scaled.toFixed(0) : size >= 1 ? scaled.toFixed(1) : scaled.toFixed(3));
  if (multiple.suffix === '') return figure;
  return multiple.spaced ? `${figure} ${multiple.suffix}` : `${figure}${multiple.suffix}`;
}

/** `8.0 KiB` and `8 KiB` say the same thing and only one reads like a figure a
 *  human chose - `overview.trimZero`, which drops the same tail. Only ever
 *  after a decimal point, so 1000 stays 1000. */
function trimZeros(text: string): string {
  return text.includes('.') ? text.replace(/\.?0+$/, '') : text;
}

function humanise(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 86_400) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86_400)}d`;
}

function when(at: string): string {
  return formatInstant(at);
}
