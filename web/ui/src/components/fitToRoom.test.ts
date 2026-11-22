import { describe, expect, it } from 'vitest';

import { fitToRoom, type Fitting } from './DataGrid';

const sum = (widths: readonly number[]): number => widths.reduce((total, each) => total + each, 0);

/** Name, Namespace, Status (held), Restarts, Node, Age - a pods list in short. */
function pods(): Fitting[] {
  return [
    { width: 300, yields: 1, floor: 154, head: 122 },
    { width: 140, yields: 1, floor: 112, head: 112 },
    { width: 166, yields: undefined, floor: 166, head: 88, holds: true, need: () => 96 },
    { width: 104, yields: undefined, floor: 104, head: 104 },
    { width: 120, yields: 2, floor: 104, head: 72 },
    { width: 64, yields: undefined, floor: 64, head: 64 },
  ];
}

describe('fitToRoom', () => {
  it('leaves a list that fits as it is', () => {
    const fit = fitToRoom(pods(), 1000);
    expect(fit).toEqual({ widths: [300, 140, 166, 104, 120, 64], squeezed: false });
  });

  it('takes the difference from the name and the namespace first, and touches nothing else', () => {
    const fit = fitToRoom(pods(), 800);
    expect(fit.squeezed).toBe(false);
    expect(sum(fit.widths)).toBeLessThanOrEqual(800);
    expect(fit.widths.slice(2)).toEqual([166, 104, 120, 64]);
    expect(fit.widths[0]).toBeLessThan(300);
    expect(fit.widths[1]).toBeLessThan(140);
  });

  it('then other free text, then every other column down to its heading, and keeps the status', () => {
    const second = fitToRoom(pods(), 710);
    expect(second.widths.slice(0, 2)).toEqual([154, 112]);
    expect(second.widths[4]).toBeLessThan(120);
    expect(second.widths[3]).toBe(104);
    expect(second.widths[2]).toBe(166);
    expect(sum(second.widths)).toBeLessThanOrEqual(710);

    const fourth = fitToRoom(pods(), 640);
    expect(fourth.squeezed).toBe(false);
    expect(fourth.widths[2], 'the held status gave way before the others').toBe(166);
    expect(sum(fourth.widths)).toBeLessThanOrEqual(640);
  });

  it('then gives back the status reserve, down to what its values need', () => {
    const fit = fitToRoom(pods(), 600);
    expect(fit.squeezed).toBe(false);
    expect(fit.widths[2]).toBeLessThan(166);
    expect(fit.widths[2]).toBeGreaterThanOrEqual(96);
    expect(sum(fit.widths)).toBeLessThanOrEqual(600);
  });

  it('past every heading, narrows every column but the held one in proportion, to the room exactly', () => {
    const fit = fitToRoom(pods(), 500);
    expect(fit.squeezed).toBe(true);
    expect(fit.widths[2]).toBe(96);
    expect(sum(fit.widths)).toBeCloseTo(500, 6);
  });

  it('narrows the held column too where the rest would be left with almost nothing', () => {
    const fit = fitToRoom(pods(), 250);
    expect(fit.squeezed).toBe(true);
    expect(fit.widths[2]).toBeLessThan(96);
    expect(sum(fit.widths)).toBeCloseTo(250, 6);
  });

  it('never drops a column', () => {
    for (const room of [100, 400, 700, 900, 2000]) {
      const fit = fitToRoom(pods(), room);
      expect(fit.widths).toHaveLength(6);
      for (const width of fit.widths) expect(width).toBeGreaterThan(0);
    }
  });
});
