import { Avatar, Style } from '@dicebear/core';
import { createElement, type CSSProperties, type ReactElement, type ReactNode } from 'react';

import type { ClusterColour } from '../api';
import { pairOf, type IconRecipe, type IconStyle } from './iconRecipe';

/**
 * The heavy half of the cluster icon: DiceBear's engine, the style
 * definitions - loaded by `loadStyle` when a drawing needs one - and the step
 * from SVG text to React elements. Imported
 * only through `useIconRenderer`, so it is a chunk of its own too.
 *
 * ## Why the SVG becomes React elements rather than markup or an image
 *
 * Two routes were closed before this one:
 *
 * - **`<img src="data:…">`** - the console's policy has no `img-src`, so images
 *   fall back to `default-src 'self'` and a `data:` URI is blocked
 *   (`internal/web/headers.go`). Drawing this way would need the policy
 *   loosened for a picture.
 * - **`dangerouslySetInnerHTML`** - banned outright by the lint config (brief
 *   §5), and the reason is the right one even for markup from a library.
 *
 * So DiceBear's string is parsed with `DOMParser` - inert, nothing runs - and
 * walked into React elements through an **allow-list** of SVG elements and
 * attributes: no `<script>`, no `<foreignObject>`, no `<style>`, no event
 * handler, and no reference that leaves the drawing (`href` and `url()` must
 * name a fragment inside it). Every `id` is prefixed per instance, because the
 * same recipe drawn on the tile and in the editor would otherwise put two
 * `#clip-…` in one document, and the second drawing would borrow the first's.
 */

/** Every definition is KubeTower's own, in one module of its own
 *  (`iconStyles.ts`), imported the first time a drawing needs a style. */
function definitionOf(name: IconStyle): Promise<unknown> {
  return import('./iconStyles').then((module) => {
    const definition = module.STYLE_DEFINITIONS[name];
    if (definition === undefined) throw new Error(`no definition for ${name}`);
    return definition;
  });
}

const styles = new Map<IconStyle, Style>();
const loading = new Map<IconStyle, Promise<void>>();

/*! Includes @dicebear/core (https://github.com/dicebear/dicebear), inlined into
 * this chunk - here, beside the first function that calls it, because a
 * legal comment above the imports is dropped with them - under its MIT License:
 *
 * MIT License
 *
 * Copyright (c) 2026 Florian Körner
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */
/** A style, loaded and validated once - validation is the expensive step. A
 *  failed load is forgotten, so the next drawing asks again. */
export function loadStyle(name: IconStyle): Promise<void> {
  let found = loading.get(name);
  if (found === undefined) {
    found = definitionOf(name).then((definition) => {
      styles.set(name, new Style(definition));
    });
    found.catch(() => loading.delete(name));
    loading.set(name, found);
  }
  return found;
}

export function isStyleLoaded(name: IconStyle): boolean {
  return styles.has(name);
}

function styleOf(name: IconStyle): Style {
  const style = styles.get(name);
  if (style === undefined) throw new Error(`the ${name} style is not loaded`);
  return style;
}

/** A swatch's paint as DiceBear wants it, six hex digits with no `#`, read from
 *  the theme's own `--color-plate-*` - or `undefined` where the theme cannot be
 *  read, which leaves the style's default.
 *
 *  The theme writes the swatches as `hsl(...)`, and the built stylesheet holds
 *  them as hex (Vite's CSS minifier converts them), so both are read; anything
 *  else is painted onto a one-pixel canvas and read back. */
function paintOf(key: ClusterColour): string | undefined {
  return themePaint(`--color-plate-${key}`);
}

/** Any theme color, by its custom property, as six hex digits. */
function themePaint(property: string): string | undefined {
  if (typeof document === 'undefined') return undefined;
  const value = getComputedStyle(document.documentElement).getPropertyValue(property).trim();
  return value === '' ? undefined : (hexOf(value) ?? hslOf(value) ?? paintedOf(value));
}

function hexOf(value: string): string | undefined {
  const short = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(value);
  if (short !== null) return `${short[1]}${short[1]}${short[2]}${short[2]}${short[3]}${short[3]}`.toLowerCase();
  return /^#([0-9a-f]{6})$/i.exec(value)?.[1]?.toLowerCase();
}

function hslOf(value: string): string | undefined {
  const hsl = /^hsl\(\s*([\d.]+)\s+([\d.]+)%\s+([\d.]+)%\s*\)$/i.exec(value);
  if (hsl === null) return undefined;
  const [h, s, l] = [Number(hsl[1]), Number(hsl[2]) / 100, Number(hsl[3]) / 100];
  const a = s * Math.min(l, 1 - l);
  const channel = (n: number): string => {
    const k = (n + h / 30) % 12;
    return byte(l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1)));
  };
  return `${channel(0)}${channel(8)}${channel(4)}`;
}

function paintedOf(value: string): string | undefined {
  const context = document.createElement('canvas').getContext('2d', { willReadFrequently: true });
  if (context === null) return undefined;
  context.fillStyle = value;
  context.fillRect(0, 0, 1, 1);
  const [r = 0, g = 0, b = 0] = context.getImageData(0, 0, 1, 1).data;
  return `${byte(r / 255)}${byte(g / 255)}${byte(b / 255)}`;
}

const byte = (unit: number): string =>
  Math.round(Math.max(0, Math.min(1, unit)) * 255)
    .toString(16)
    .padStart(2, '0');

/** The recipe as DiceBear's options. */
function optionsOf(recipe: IconRecipe): Record<string, unknown> {
  // No animation: a `<style>` would not pass the allow-list, and a rail of
  // moving tiles is not what a tile is for.
  const options: Record<string, unknown> = { seed: recipe.seed, animationProbability: 0 };
  if (recipe.background !== undefined) {
    const from = paintOf(recipe.background);
    const to = paintOf(pairOf(recipe.background));
    if (from !== undefined) {
      if (recipe.gradient !== undefined && to !== undefined) {
        options.backgroundColor = [from, to];
        options.backgroundColorFill = [recipe.gradient];
        options.backgroundColorOrder = 'fixed';
      } else {
        options.backgroundColor = [from];
      }
    }
  }
  if (recipe.ink !== undefined) {
    const ink = paintOf(recipe.ink);
    if (ink !== undefined) options.accentColor = [ink];
  }
  if (recipe.rotate !== undefined) options.rotate = recipe.rotate;
  if (recipe.scale !== undefined) options.scale = recipe.scale / 100;
  if (recipe.flip === true) options.flip = 'horizontal';
  return options;
}

/** The recipe drawn, as SVG text. Throws only on a definition DiceBear refuses. */
export function renderIconSvg(recipe: IconRecipe): string {
  return new Avatar(styleOf(recipe.style), optionsOf(recipe)).toString();
}

// --- SVG text to React elements, through an allow-list ----------------------

const ELEMENTS = new Set([
  'svg',
  'g',
  'defs',
  'path',
  'rect',
  'circle',
  'ellipse',
  'line',
  'polyline',
  'polygon',
  'use',
  'clipPath',
  'mask',
  'pattern',
  'linearGradient',
  'radialGradient',
  'stop',
  'filter',
  'feBlend',
  'feColorMatrix',
  'feComponentTransfer',
  'feComposite',
  'feFlood',
  'feGaussianBlur',
  'feMerge',
  'feMergeNode',
  'feOffset',
  'feTurbulence',
  'feDisplacementMap',
  'feFuncA',
  'feFuncR',
  'feFuncG',
  'feFuncB',
]);

/** `clip-path` to `clipPath`: React's spelling of every SVG presentation
 *  attribute DiceBear writes. */
const camel = (name: string): string =>
  /^(aria|data)-/.test(name) ? name : name.replace(/-([a-z])/g, (_, letter: string) => letter.toUpperCase());

/** A `url(#x)` inside the drawing, re-pointed at the prefixed id; any other
 *  `url(…)` is a reference out of the drawing and the attribute is dropped. */
function rewriteUrls(value: string, ids: ReadonlyMap<string, string>): string | undefined {
  let outside = false;
  const out = value.replace(/url\(\s*([^)]*?)\s*\)/g, (_, target: string) => {
    const id = target.startsWith('#') ? ids.get(target.slice(1)) : undefined;
    if (id === undefined) outside = true;
    return `url(#${id ?? ''})`;
  });
  return outside ? undefined : out;
}

function styleObject(value: string, ids: ReadonlyMap<string, string>): CSSProperties | undefined {
  const out: Record<string, string> = {};
  for (const declaration of value.split(';')) {
    const at = declaration.indexOf(':');
    if (at < 0) continue;
    const property = declaration.slice(0, at).trim().toLowerCase();
    const raw = declaration.slice(at + 1).trim();
    if (!/^[a-z-]+$/.test(property) || /expression|@import|\\/i.test(raw)) continue;
    const rewritten = rewriteUrls(raw, ids);
    if (rewritten !== undefined) out[camel(property)] = rewritten;
  }
  return Object.keys(out).length === 0 ? undefined : (out as CSSProperties);
}

function propsOf(element: Element, ids: ReadonlyMap<string, string>, key: string): Record<string, unknown> {
  const props: Record<string, unknown> = { key };
  for (const { name, value } of [...element.attributes]) {
    const lower = name.toLowerCase();
    if (lower.startsWith('on') || lower === 'class' || lower.startsWith('xmlns')) continue;
    if (lower === 'href' || lower === 'xlink:href') {
      const id = value.startsWith('#') ? ids.get(value.slice(1)) : undefined;
      if (id !== undefined) props.href = `#${id}`;
      continue;
    }
    if (name.includes(':')) continue;
    if (lower === 'id') {
      props.id = ids.get(value);
      continue;
    }
    if (lower === 'style') {
      const style = styleObject(value, ids);
      if (style !== undefined) props.style = style;
      continue;
    }
    const rewritten = rewriteUrls(value, ids);
    if (rewritten !== undefined) props[camel(name)] = rewritten;
  }
  return props;
}

function walk(element: Element, ids: ReadonlyMap<string, string>, key: string): ReactElement | null {
  if (!ELEMENTS.has(element.tagName)) return null;
  const children: ReactNode[] = [];
  [...element.children].forEach((child, at) => {
    const drawn = walk(child, ids, String(at));
    if (drawn !== null) children.push(drawn);
  });
  return createElement(element.tagName, propsOf(element, ids, key), ...children);
}

/**
 * The recipe drawn as React elements, filling its box. `prefix` makes its ids
 * unique on the page; `null` when the SVG cannot be read, which the caller
 * draws as the color.
 */
export function iconElement(recipe: IconRecipe, prefix: string): ReactElement | null {
  if (!styles.has(recipe.style)) return null;
  let svg: string;
  try {
    svg = renderIconSvg(recipe);
  } catch {
    return null;
  }
  return svgElement(svg, prefix);
}

/** SVG text as React elements through the allow-list above, ids prefixed with
 *  `prefix`; `null` when it is not one readable `<svg>`. Exported for its tests,
 *  which hand it markup DiceBear would never write. */
export function svgElement(svg: string, prefix: string): ReactElement | null {
  const document = new DOMParser().parseFromString(svg, 'image/svg+xml');
  const root = document.documentElement;
  if (root.tagName !== 'svg' || document.getElementsByTagName('parsererror').length > 0) return null;

  const ids = new Map<string, string>();
  for (const each of root.querySelectorAll('[id]')) {
    const id = each.getAttribute('id');
    if (id !== null) ids.set(id, `${prefix}-${id.replace(/[^\w-]/g, '')}`);
  }
  const drawn = walk(root, ids, 'svg');
  if (drawn === null) return null;
  // The box is the tile's: the drawing fills it, says nothing to a screen
  // reader (the tile's name does), and is never a tab stop.
  const props = drawn.props as Record<string, unknown>;
  return createElement(
    'svg',
    {
      ...props,
      key: 'svg',
      width: '100%',
      height: '100%',
      'aria-hidden': true,
      focusable: 'false',
      preserveAspectRatio: 'xMidYMid slice',
    },
    props.children as ReactNode,
  );
}
