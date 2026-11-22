import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { Gauge } from '../pages/overview/Reading';
import { fraction, Measure } from './Reading';

/**
 * The console's one anatomy for a reading, tested where it is decided: where
 * there is a proportion, the bar and its percentage
 * on one line and the quantity on hover as `used / total`; where there is none,
 * the reading itself and no shape. Each surface that draws one has its own test
 * beside it (`ResourceList.test.tsx`, `Panels.test.tsx`, `ClusterList.test.tsx`);
 * this one holds the parts every surface inherits.
 */

afterEach(cleanup);

/** What is on screen, screen-reader text aside. */
function drawn(element: Element | null): string {
  const copy = element?.cloneNode(true) as HTMLElement | undefined;
  copy?.querySelectorAll('.sr-only').forEach((node) => {
    node.remove();
  });
  return copy?.textContent ?? '';
}

describe('Measure', () => {
  it('draws the bar and its rounded percentage beside it, and the quantity only on hover', () => {
    const { container } = render(
      <Measure value="1163m" title="1.2 / 2 cores" percent={58.2} tone="ok" label="CPU" />,
    );

    const line = container.querySelector('[data-measure]');
    expect(drawn(line)).toBe('58%');
    expect(line?.getAttribute('title')).toBe('1.2 / 2 cores');
    expect(line?.querySelector('.sr-only')?.textContent).toBe('1.2 / 2 cores');

    // Bar first, percentage after it, one line.
    const track = line?.querySelector('[data-gauged]');
    expect(track?.getAttribute('aria-label')).toBe('CPU: 58 percent');
    expect(line?.firstElementChild?.contains(track ?? null)).toBe(true);
    const share = line?.querySelector('[data-share]');
    expect(share?.previousElementSibling).toBe(line?.firstElementChild);
    // Said once: the track's name carries the figure, so the digits are hidden.
    expect(share?.getAttribute('aria-hidden')).toBe('true');
  });

  it('keeps the quantity on hover where no whole was sent', () => {
    const { container } = render(<Measure value="368m" percent={6} label="CPU" />);
    expect(container.querySelector('[data-measure]')?.getAttribute('title')).toBe('368m');
  });

  it('draws no shape and no percentage where nothing was measured', () => {
    const { container } = render(<Measure value="—" label="CPU" />);
    expect(container.querySelector('[data-gauged]')).toBeNull();
    expect(container.querySelector('[data-share]')).toBeNull();
    expect(container.textContent).toBe('—');
  });

  it('leaves the bar off and keeps the percentage where another shape draws it', () => {
    const { container } = render(
      <Measure value="9.4 cores" title="9.4 / 8 cores" percent={117.5} track={false} label="requested" />,
    );
    expect(container.querySelector('[data-gauged]')).toBeNull();
    // The real figure past the whole, never clamped: the ring says the same.
    expect(container.querySelector('[data-share]')?.textContent).toBe('118%');
    expect(container.querySelector('[data-share]')?.getAttribute('aria-hidden')).toBeNull();
  });
});

describe('fraction', () => {
  it('joins two server texts with a slash and formats nothing', () => {
    expect(fraction('3.2 GiB', '16.0 GiB')).toBe('3.2 GiB / 16.0 GiB');
  });

  it('is no hover at all rather than half of one', () => {
    expect(fraction('3.2 GiB', undefined)).toBeUndefined();
    expect(fraction('', '16.0 GiB')).toBeUndefined();
  });
});

describe("the overview's Gauge", () => {
  it('draws its percentage beside the bar and used / total on hover', () => {
    const { container } = render(
      <Gauge
        used={{ value: 2, trustworthy: true, text: '2.0 cores' }}
        capacity={{ value: 8, trustworthy: true, text: '8.0 cores' }}
        label="CPU"
      />,
    );
    const line = container.querySelector('[data-measure]');
    expect(drawn(line)).toBe('25%');
    expect(line?.getAttribute('title')).toBe('2.0 cores / 8.0 cores');
    expect(line?.querySelector('[data-gauged]')).not.toBeNull();
  });

  it('keeps the quantity on hover where the server sent no text', () => {
    // `text` is optional in the contract: the value as sent stands in for it,
    // so a measured reading is never left with no quantity anywhere.
    const { container } = render(
      <Gauge used={{ value: 2, trustworthy: true }} capacity={{ value: 8, trustworthy: true }} label="CPU" />,
    );
    const line = container.querySelector('[data-measure]');
    expect(line?.getAttribute('title')).toBe('2 / 8');
    expect(line?.querySelector('.sr-only')?.textContent).toBe('2 / 8');
  });

  it('draws the dash and no bar where the usage was not measured', () => {
    const { container } = render(
      <Gauge
        used={{ value: null, trustworthy: false, text: 'no metrics source' }}
        capacity={{ value: 8, trustworthy: true, text: '8.0 cores' }}
        label="CPU"
      />,
    );
    expect(container.querySelector('[data-gauged]')).toBeNull();
    expect(container.querySelector('[data-share]')).toBeNull();
    expect(container.textContent).toContain('—');
  });
});
