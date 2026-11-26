import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { NoMetrics } from './NoMetrics';

const UNREACHED = 'service monitoring/prometheus:9090 found, but neither the API service proxy nor a port-forward reached it';

describe('NoMetrics', () => {
  it('draws one muted line with the reason on hover, for every failed state', () => {
    for (const state of ['unreachable', 'unreadable', 'forbidden', 'absent'] as const) {
      cleanup();
      render(<NoMetrics state={state} reason={{ code: 'cluster_unreachable', message: UNREACHED, detail: 'i/o timeout' }} />);
      const line = screen.getByRole('status');
      expect(line.textContent).toBe('No metrics available');
      expect(line.getAttribute('data-state')).toBe(state);
      expect(line.getAttribute('title')).toBe(`${UNREACHED}\ni/o timeout`);
      expect(line.className).toContain('text-muted');
      expect(line.className).not.toMatch(/text-(bad|warn)/);
      expect(line.className).not.toMatch(/\bborder\b/);
      expect(document.querySelector('.font-mono')).toBeNull();
    }
  });

  it('says a detail that repeats the message once', () => {
    render(<NoMetrics state="unreachable" reason={{ code: 'cluster_unreachable', message: UNREACHED, detail: UNREACHED }} />);
    expect(screen.getByRole('status').getAttribute('title')).toBe(UNREACHED);
  });

  it('offers a small retry only where the reason says it could differ', () => {
    const retry = vi.fn();
    render(<NoMetrics state="unreachable" reason={{ code: 'cluster_unreachable', message: 'm', retryable: true }} onRetry={retry} />);
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(retry).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: 'Try again' }).textContent).toBe('');

    cleanup();
    render(<NoMetrics state="forbidden" reason={{ code: 'verb_denied', message: 'm' }} onRetry={retry} />);
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();

    cleanup();
    render(<NoMetrics state="pending" reason={{ code: 'not_probed_yet', message: 'm', retryable: true }} onRetry={retry} />);
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });

  it('draws a neutral badge with a plain detail on hover, and no control', () => {
    render(<NoMetrics variant="badge" detail="no Prometheus matched" onRetry={() => undefined} />);
    const pill = document.querySelector('[data-no-metrics] [data-tone]');
    expect(pill?.textContent).toBe('No metrics available');
    expect(pill?.getAttribute('data-tone')).toBe('normal');
    expect(pill?.getAttribute('title')).toBe('no Prometheus matched');
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('puts its reason within a keyboard\'s and a touch screen\'s reach, with no retry to press', () => {
    render(<NoMetrics state="forbidden" reason={{ code: 'verb_denied', message: 'You may not get pods in shop' }} />);
    const why = screen.getByRole('button', { name: 'Why no metrics' });
    expect(document.querySelector('[data-detail-tip-text]')).toBeNull();
    why.focus();
    fireEvent.click(why);
    expect(why.getAttribute('aria-expanded')).toBe('true');
    expect(document.getElementById(why.getAttribute('aria-controls') ?? '')?.textContent).toBe('You may not get pods in shop');
    fireEvent.keyDown(why, { key: 'Escape' });
    expect(document.querySelector('[data-detail-tip-text]')).toBeNull();
    expect(document.activeElement).toBe(why);
  });
});
