import { describe, expect, it } from 'vitest';

import { DEFAULT_ICON_STYLE, ICON_CATEGORIES, ICON_STYLES, iconStyleLabel, pairOf, QUICK_COLOURS, parseIcon, randomIcon, randomSeed, serialiseIcon, type IconRecipe } from './iconRecipe';
import { PALETTE } from './clusterState';

describe('the icon recipe', () => {
  it('round-trips, leaving defaults out so equal recipes are equal strings', () => {
    const recipe: IconRecipe = {
      v: 1,
      style: 'kt-pod',
      seed: 'prod',
      background: 'blue',
      gradient: 'radial',
      ink: 'amber-pale',
      rotate: 45,
      scale: 120,
      flip: true,
    };
    expect(parseIcon(serialiseIcon(recipe))).toEqual(recipe);
    expect(serialiseIcon({ v: 1, style: 'kt-squares-solo', seed: 'x', rotate: 0, scale: 100, flip: false })).toBe(
      '{"v":1,"style":"kt-squares-solo","seed":"x"}',
    );
  });

  it('drops what a recipe cannot use: a gradient with no background', () => {
    expect(serialiseIcon({ v: 1, style: 'kt-pod', seed: 'x', gradient: 'linear' })).toBe(
      '{"v":1,"style":"kt-pod","seed":"x"}',
    );
  });

  it('reads anything it does not understand as no icon, so the tile keeps its color', () => {
    for (const raw of [
      undefined,
      '',
      'not json',
      '[]',
      'null',
      '{"v":2,"style":"kt-pod","seed":"x"}',
      '{"v":1,"style":"shape-grid","seed":"x"}',
      '{"v":1,"style":"rings","seed":"x"}',
      '{"v":1,"style":"avataaars","seed":"x"}',
      '{"v":1,"style":"kt-pod"}',
      `{"v":1,"style":"kt-pod","seed":"${'x'.repeat(65)}"}`,
    ]) {
      expect(parseIcon(raw)).toBeUndefined();
    }
  });

  it('keeps a readable recipe and drops only its unreadable members', () => {
    expect(
      parseIcon('{"v":1,"style":"kt-rings-solo","seed":"x","background":"chartreuse","rotate":720,"scale":"big","ink":"teal"}'),
    ).toEqual({ v: 1, style: 'kt-rings-solo', seed: 'x', ink: 'teal' });
  });

  it('offers two hundred and twenty-one styles, all its own, and no characters', () => {
    expect(ICON_CATEGORIES.map((group) => [group.id, group.styles.length])).toEqual([
      ['clusters', 20],
      ['kubernetes', 12],
      ['servers', 10],
      ['network', 9],
      ['data', 9],
      ['platform', 10],
      ['security', 10],
      ['operations', 10],
      ['delivery', 10],
      ['resources', 10],
      ['actions', 10],
      ['insight', 10],
      ['console', 10],
      ['abstract', 40],
      ['patterns', 41],
    ]);
    expect(new Set(ICON_STYLES).size).toBe(221);
    // Home-made only: nothing from an icon library.
    expect(ICON_STYLES.every((style) => style.startsWith('kt-'))).toBe(true);
    for (const library of ['shape-grid', 'shapes', 'identicon', 'rings', 'glass', 'stripes', 'planets']) {
      expect(ICON_STYLES).not.toContain(library);
    }
    expect(DEFAULT_ICON_STYLE).toBe('kt-hexagons-quad');
    expect(iconStyleLabel('kt-ci-cluster')).toBe('CI cluster');
    expect(iconStyleLabel('kt-circles-stack')).toBe('Circles stack');
    expect(iconStyleLabel('kt-api')).toBe('API');
    expect(iconStyleLabel('kt-id-card')).toBe('ID card');
    expect(ICON_STYLES).toContain('kt-pod');
    expect(ICON_STYLES).toContain('kt-server');
    for (const character of ['pixelbot', 'voxel-bot', 'lorelei', 'open-peeps', 'thumbs', 'initials']) {
      expect(ICON_STYLES).not.toContain(character);
    }
    expect(iconStyleLabel('kt-replica-set')).toBe('Replica set');
    expect(iconStyleLabel('kt-cpu')).toBe('CPU');
    expect(iconStyleLabel('kt-hexagons-quad')).toBe('Hexagons quad');
    expect(QUICK_COLOURS).toHaveLength(8);
    expect(new Set(QUICK_COLOURS).size).toBe(8);
    for (const key of QUICK_COLOURS) expect(PALETTE).toContain(key);
  });

  it('reads a recipe in any offered style, and nothing else', () => {
    for (const style of ICON_STYLES) expect(parseIcon(serialiseIcon({ v: 1, style, seed: 'x' }))?.style).toBe(style);
    expect(parseIcon('{"v":1,"style":"lib","seed":"","lib":"lucide","name":"server","vb":"0 0 24 24","body":"<g/>"}')).toBeUndefined();
  });

  it('pairs each swatch with the other half of its hue', () => {
    expect(pairOf('teal')).toBe('teal-pale');
    expect(pairOf('teal-pale')).toBe('teal');
  });

  it('draws random seeds of eight characters', () => {
    expect(randomSeed()).toMatch(/^[0-9a-z]{8}$/);
  });

  it('draws a random recipe - style, color and seed - from the dice it is given', () => {
    // Always the last face: the last style, the last swatch, and "z" eight times.
    const top = (): number => 0.999;
    const recipe = randomIcon(undefined, top);
    expect(recipe).toEqual({ v: 1, style: ICON_STYLES.at(-1), seed: 'zzzzzzzz', background: PALETTE.at(-1) });
    // And it is a recipe like a hand-chosen one: it survives the round trip.
    expect(parseIcon(serialiseIcon(recipe))).toEqual(recipe);
    // The first face of each.
    expect(randomIcon(undefined, () => 0)).toEqual({ v: 1, style: ICON_STYLES[0], seed: '00000000', background: PALETTE[0] });
  });

  it('keeps a color the operator chose as the random icon background', () => {
    expect(randomIcon('teal', () => 0.999).background).toBe('teal');
  });

  it('draws a different recipe each time from the real dice', () => {
    const drawn = new Set(Array.from({ length: 4 }, () => serialiseIcon(randomIcon(undefined))));
    expect(drawn.size).toBe(4);
  });
});
