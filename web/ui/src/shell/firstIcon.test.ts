import { describe, expect, it } from 'vitest';

import { PALETTE } from './clusterState';
import { iconsToDraw, settleIcon } from './firstIcon';
import { ICON_STYLES, parseIcon, RESET_ICON } from './iconRecipe';

/** Dice that roll the given faces in turn, over and over. */
function dice(...faces: number[]): () => number {
  let at = 0;
  return () => faces[at++ % faces.length] ?? 0;
}

describe('a cluster seen with no icon is given one at random', () => {
  it('draws a style, a color and a seed for a new cluster, as a recipe the tile reads', () => {
    const [draw] = iconsToDraw([{ name: 'prod' }], {}, () => 0);
    expect(draw?.cluster).toBe('prod');
    expect(parseIcon(draw?.icon)).toEqual({ v: 1, style: ICON_STYLES[0], seed: '00000000', background: PALETTE[0] });
  });

  it('gives two new clusters different icons', () => {
    // Style, color, then eight seed characters: ten faces per cluster.
    const draws = iconsToDraw([{ name: 'prod' }, { name: 'staging' }], undefined, dice(...Array<number>(10).fill(0), ...Array<number>(10).fill(0.5)));
    expect(draws.map((one) => one.cluster)).toEqual(['prod', 'staging']);
    const [a, b] = draws.map((one) => parseIcon(one.icon));
    expect(a).not.toEqual(b);
    expect(a?.style).not.toBe(b?.style);
    expect(a?.background).not.toBe(b?.background);
    expect(a?.seed).not.toBe(b?.seed);
  });

  it('leaves a stored recipe alone, even one this build cannot read', () => {
    const plates = {
      prod: { icon: '{"v":1,"style":"kt-rings-solo","seed":"x"}' },
      staging: { icon: '{"v":9,"style":"later"}' },
      edge: { name: 'Edge' },
    };
    expect(iconsToDraw([{ name: 'prod' }, { name: 'staging' }, { name: 'edge' }], plates, () => 0).map((one) => one.cluster)).toEqual(['edge']);
  });

  it('keeps a color the operator chose as the background', () => {
    const [draw] = iconsToDraw([{ name: 'prod' }], { prod: { colour: 'teal' } }, () => 0.999);
    expect(parseIcon(draw?.icon)?.background).toBe('teal');
  });

  it('draws nothing again for a cluster already drawn for or reset on this page', () => {
    settleIcon('prod');
    expect(iconsToDraw([{ name: 'prod' }, { name: 'staging' }], {}, () => 0).map((one) => one.cluster)).toEqual(['staging']);
  });

  it('draws nothing for a cluster whose reset marker is stored, even after a reload', () => {
    // A reload empties the per-page set; the stored marker is what holds.
    expect(iconsToDraw([{ name: 'edge' }], { edge: { icon: RESET_ICON } }, () => 0)).toEqual([]);
  });

  it('reads the reset marker as no icon, so the default is drawn', () => {
    expect(parseIcon(RESET_ICON)).toBeUndefined();
  });

  it('draws again once the marker is cleared', () => {
    expect(iconsToDraw([{ name: 'edge' }], { edge: { icon: '' } }, () => 0).map((one) => one.cluster)).toEqual(['edge']);
  });
});
