import type { ReactNode } from 'react';

import type { Reason, ReasonOnlyState } from '../api';
import { btnGhost } from '../components/buttons';
import { DetailTip } from '../components/DetailTip';
import { Icon } from '../components/Icon';
import { Pill } from '../components/Pill';

/**
 * A metrics read with no answer to draw: one muted line, *No metrics
 * available*.
 *
 * A metrics backend that is missing, or found and not reached (no service
 * proxy, a port-forward that failed), is not a cluster failure, and a boxed
 * red *Could not be asked* with the whole sentence twice took more room than
 * the charts it stood for (#127). The server's sentence and detail ride in the
 * line's `title`, and the state stays in `data-state` for anything that
 * branches on it.
 *
 * Only for metrics. A read of the cluster itself, and an action that failed,
 * keep `StateNotice` and `Failure`.
 */
export const NO_METRICS = 'No metrics available';

export interface NoMetricsProps {
  /** The read's state, where there was one. Omitted by a surface that has no
   *  envelope to hand, such as a setting naming the metrics source. */
  state?: ReasonOnlyState | undefined;
  reason?: Reason | undefined;
  /** The hover line where there is no `Reason`, such as a discovery's own
   *  detail. Ignored when `reason` carries a sentence. */
  detail?: string | undefined;
  /** Offered only where the reason says another attempt could differ. */
  onRetry?: (() => void) | undefined;
  /** `line` in place of a panel's answer; `badge` beside a label, as a neutral
   *  pill with no control. */
  variant?: 'line' | 'badge';
}

export function NoMetrics({ state, reason, detail, onRetry, variant = 'line' }: NoMetricsProps): ReactNode {
  const title = why(reason) ?? (detail === '' ? undefined : detail);
  if (variant === 'badge') {
    return (
      <span data-state={state} data-no-metrics="">
        <Pill neutral="muted" title={title}>
          {NO_METRICS}
        </Pill>
      </span>
    );
  }
  // Never on `pending`, as `StateNotice`: asking again cannot bring the
  // server's own round sooner.
  const retry = state !== 'pending' && reason?.retryable === true ? onRetry : undefined;
  return (
    <p
      role="status"
      data-state={state}
      data-no-metrics=""
      className="flex items-center gap-1 text-xs text-muted"
      title={title}
    >
      {NO_METRICS}
      {/* The reason within a keyboard's and a touch screen's reach, not the
          hover alone. */}
      {title === undefined ? null : <DetailTip text={title} label="Why no metrics" />}
      {retry === undefined ? null : (
        <button type="button" onClick={retry} aria-label="Try again" title="Try again" className={`${btnGhost} px-1`}>
          <Icon name="refresh" />
        </button>
      )}
    </p>
  );
}

/** The message, and the detail where it says more than the message. */
export function why(reason: Reason | undefined): string | undefined {
  const parts = [reason?.message, reason?.detail].filter(
    (part, index, all): part is string => part !== undefined && part !== '' && all.indexOf(part) === index,
  );
  return parts.length === 0 ? undefined : parts.join('\n');
}
