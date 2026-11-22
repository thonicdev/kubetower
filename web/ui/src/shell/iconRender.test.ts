import { isValidElement, type ReactElement } from 'react';
import { beforeAll, describe, expect, it } from 'vitest';

import { ICON_STYLES } from './iconRecipe';
import { iconElement, isStyleLoaded, loadStyle, renderIconSvg, svgElement } from './iconRender';

type Props = Record<string, unknown> & { children?: unknown };

/** Every element of a tree, depth first. */
function everything(node: unknown): ReactElement<Props>[] {
  if (Array.isArray(node)) return node.flatMap(everything);
  if (!isValidElement<Props>(node)) return [];
  return [node, ...everything(node.props.children)];
}

describe('drawing an icon', () => {
  beforeAll(async () => {
    await Promise.all(ICON_STYLES.map((style) => loadStyle(style)));
  }, 30_000);

  it('offers KubeTower\u2019s own styles and nothing else', async () => {
    const infra = await import('./iconStyles');
    expect([...ICON_STYLES].sort()).toEqual([...infra.STYLE_IDS].sort());
    for (const id of infra.STYLE_IDS) {
      const meta = (infra.STYLE_DEFINITIONS[id] as { meta: { creator: { name: string } } }).meta;
      expect(meta.creator.name, id).toBe('KubeTower');
    }
  });

  // Every style is drawn twice and its markup parsed once, in jsdom: 4.9 s
  // measured at a load of 34, past Vitest's default 5 s on a busier runner.
  // The budget is the one the styles' loading above already has.
  it('draws every offered style, deterministically, with nothing the allow-list drops', () => {
    for (const style of ICON_STYLES) {
      expect(isStyleLoaded(style)).toBe(true);
      const once = renderIconSvg({ v: 1, style, seed: 'prod' });
      expect(once.startsWith('<svg'), style).toBe(true);
      expect(renderIconSvg({ v: 1, style, seed: 'prod' })).toBe(once);
      // No text, no stylesheet, no image: each would be dropped on the way in,
      // and the icon drawn without it.
      expect(once, style).not.toMatch(/<(text|style|image|foreignObject)\b/);
      expect(iconElement({ v: 1, style, seed: 'prod' }, 'kti1'), style).not.toBeNull();
    }
  }, 30_000);

  it('prefixes every id and every reference to one, so two drawings never share an id', () => {
    const drawn = iconElement({ v: 1, style: 'kt-hexagons-quad', seed: 'prod' }, 'ktiA');
    const all = everything(drawn);
    const ids = all.map((each) => each.props.id).filter((id) => id !== undefined);
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) expect(String(id)).toMatch(/^ktiA-/);
    for (const each of all) {
      const href = each.props.href;
      if (href !== undefined) expect(String(href)).toMatch(/^#ktiA-/);
      const clip = each.props.clipPath;
      if (clip !== undefined) expect(String(clip)).toMatch(/^url\(#ktiA-/);
    }
  });

  it('passes nothing that can run, load or style the page', () => {
    const hostile = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" onload="alert(1)">
      <script>alert(1)</script>
      <style>body { display: none }</style>
      <foreignObject><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject>
      <g id="a" class="dbsh-s1" onclick="alert(1)"><rect width="1" height="1" fill="url(https://example.invalid/x)"/></g>
      <use href="https://example.invalid/sprite.svg#x"/>
      <use xlink:href="javascript:alert(1)"/>
      <use href="#a"/>
      <rect style="fill: url(#a); background: url(https://example.invalid/y); mix-blend-mode: screen"/>
    </svg>`;
    const all = everything(svgElement(hostile, 'p'));
    const tags = all.map((each) => each.type);
    expect(tags).not.toContain('script');
    expect(tags).not.toContain('style');
    expect(tags).not.toContain('foreignObject');
    for (const each of all) {
      for (const [name, value] of Object.entries(each.props)) {
        expect(name.toLowerCase().startsWith('on')).toBe(false);
        expect(name).not.toBe('className');
        if (typeof value === 'string') expect(value).not.toMatch(/example\.invalid|javascript:/);
      }
    }
    const uses = all.filter((each) => each.type === 'use').map((each) => each.props.href);
    expect(uses).toEqual([undefined, undefined, '#p-a']);
    const styled = all.find((each) => each.props.style !== undefined)?.props.style;
    expect(styled).toEqual({ fill: 'url(#p-a)', mixBlendMode: 'screen' });
  });

  it('paints a background swatch from the theme, as hsl or as the hex the built stylesheet holds', () => {
    const root = document.documentElement.style;
    try {
      // hsl(225 55% 40%) is the theme's blue; the minified stylesheet says 2e4a9e.
      root.setProperty('--color-plate-blue', 'hsl(225 55% 40%)');
      expect(renderIconSvg({ v: 1, style: 'kt-pod', seed: 'x', background: 'blue' })).toContain('2e4a9e');
      root.setProperty('--color-plate-blue', `#${'2e4a9e'}`);
      expect(renderIconSvg({ v: 1, style: 'kt-pod', seed: 'x', background: 'blue' })).toContain('2e4a9e');
      root.setProperty('--color-plate-blue', `#${'abc'}`);
      expect(renderIconSvg({ v: 1, style: 'kt-pod', seed: 'x', background: 'blue' })).toContain('aabbcc');
    } finally {
      root.removeProperty('--color-plate-blue');
    }
  });

  it('answers null for markup that is not one SVG', () => {
    expect(svgElement('<html><body/></html>', 'p')).toBeNull();
    expect(svgElement('<svg', 'p')).toBeNull();
  });
});
