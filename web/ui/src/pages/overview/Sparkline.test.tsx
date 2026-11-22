import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { Sparkline } from './Bits';
import type { MetricsPanel } from './data';

/**
 * The Overview must not draw "No metrics" under CPU used and
 * Memory used on a Prometheus that holds no per-container series at all, which
 * reads as an idle cluster. The window is the reason only when the line's code
 * says so; otherwise the line draws nothing.
 */
function line(reason?: { code: string; message: string }): MetricsPanel {
  return {
    series: [
      {
        key: 'cpu-used',
        label: 'CPU used',
        data: { startedAt: '2026-10-20T09:00:00Z', stepSeconds: 60, values: [], trustworthy: false },
        ...(reason === undefined ? {} : { reason }),
      },
    ],
  } as MetricsPanel;
}

afterEach(cleanup);

describe('Sparkline with nothing to draw', () => {
  it('blames the window only for an empty window', () => {
    render(<Sparkline panel={line({ code: 'empty_scope', message: 'no samples in this window' })} label="CPU used" />);
    expect(screen.getByText('No metrics')).toBeTruthy();
  });

  // A line this Prometheus cannot chart draws nothing at all - not the window
  // sentence, and not a filler sentence either.
  it('draws nothing for series this Prometheus does not hold', () => {
    const { container } = render(
      <Sparkline
        panel={line({ code: 'not_installed', message: "this cluster's Prometheus has no per-container series" })}
        label="CPU used"
      />,
    );
    expect(container.textContent).toBe('');
  });

  // A read that failed is not an absence: it stays, as two words, and the
  // reason is on hover.
  it('says a failed line was not read, with the reason on hover', () => {
    const { container } = render(
      <Sparkline
        panel={line({ code: 'cluster_unreachable', message: 'Post "http://prometheus:9090": i/o timeout' })}
        label="CPU used"
      />,
    );
    const said = container.querySelector('[data-state="incomplete"]');
    expect(said?.textContent).toBe('CPU used — Metrics unavailable');
    expect(said?.getAttribute('title')).toBe('Post "http://prometheus:9090": i/o timeout');
    expect(container.textContent).not.toContain('No metrics');
  });

  it('keeps the window sentence for a line that carries no reason', () => {
    render(<Sparkline panel={line()} label="CPU used" />);
    expect(screen.getByText('No metrics')).toBeTruthy();
  });
});
