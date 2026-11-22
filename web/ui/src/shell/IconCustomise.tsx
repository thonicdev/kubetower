import { useId, type ReactNode } from 'react';

import type { ClusterColour } from '../api';
import { btnToggle } from '../components/buttons';
import { focusRingTight } from '../focus';
import type { IconGradient, IconRecipe } from './iconRecipe';
import { SwatchGrid } from './Swatches';

/**
 * The Customize dialog's controls: dressing the style that
 * was chosen. They edit the recipe and nothing else.
 */

/** Background, fill, ink, rotate, scale and mirror. */
export function IconCustomise({
  recipe,
  onChange,
  label,
}: {
  recipe: IconRecipe;
  onChange: (recipe: IconRecipe) => void;
  label: string;
}): ReactNode {
  const id = useId();
  const set = (change: Partial<IconRecipe>): void => onChange({ ...recipe, ...change });
  /** `exactOptionalPropertyTypes` forbids `undefined` on an optional member,
   *  so "back to the style's own" is a member taken out. */
  const without = (name: 'background' | 'gradient' | 'ink'): void => {
    const next: IconRecipe = { ...recipe };
    delete next[name];
    if (name === 'background') delete next.gradient;
    onChange(next);
  };

  return (
    <div className="flex flex-col gap-2">
      <ColourRow
        title="Background"
        value={recipe.background}
        label={`Background for ${label}`}
        onPick={(background) => set({ background })}
        onDefault={() => without('background')}
      />
      <div role="radiogroup" aria-label={`Background fill for ${label}`} className="flex items-center gap-1">
        {(['solid', 'linear', 'radial'] as const).map((fill) => {
          const chosen = fill === (recipe.gradient ?? 'solid');
          return (
            <button
              key={fill}
              type="button"
              role="radio"
              aria-checked={chosen}
              // A gradient runs between a swatch and its other half, so it
              // needs a swatch to run from.
              disabled={recipe.background === undefined}
              title={recipe.background === undefined ? 'Pick a background color first' : undefined}
              onClick={() => {
                if (fill === 'solid') without('gradient');
                else set({ gradient: fill satisfies IconGradient });
              }}
              className={`${btnToggle(chosen)} capitalize`}
            >
              {fill}
            </button>
          );
        })}
      </div>

      <ColourRow
        title="Ink"
        value={recipe.ink}
        label={`Ink for ${label}`}
        onPick={(colour) => set({ ink: colour })}
        onDefault={() => without('ink')}
      />

      <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1 text-xs text-muted">
        <label htmlFor={`${id}-rotate`}>Rotate</label>
        <input
          id={`${id}-rotate`}
          type="range"
          min={0}
          max={345}
          step={15}
          value={recipe.rotate ?? 0}
          onChange={(event) => set({ rotate: Number(event.target.value) })}
          className={`accent-accent ${focusRingTight}`}
        />
        <span className="w-9 text-right font-mono">{recipe.rotate ?? 0}°</span>
        <label htmlFor={`${id}-scale`}>Scale</label>
        <input
          id={`${id}-scale`}
          type="range"
          min={50}
          max={150}
          step={5}
          value={recipe.scale ?? 100}
          onChange={(event) => set({ scale: Number(event.target.value) })}
          className={`accent-accent ${focusRingTight}`}
        />
        <span className="w-9 text-right font-mono">{recipe.scale ?? 100}%</span>
      </div>
      <label className="flex items-center gap-2 text-xs text-muted">
        <input
          type="checkbox"
          checked={recipe.flip === true}
          onChange={(event) => set({ flip: event.target.checked })}
          className={`accent-accent ${focusRingTight}`}
        />
        Mirror
      </label>
    </div>
  );
}

/** A color choice: the sixteen swatches, or the style's own palette. */
function ColourRow({
  title,
  value,
  label,
  onPick,
  onDefault,
}: {
  title: string;
  value: ClusterColour | undefined;
  label: string;
  onPick: (key: ClusterColour) => void;
  onDefault: () => void;
}): ReactNode {
  return (
    <div>
      <div className="flex items-center justify-between text-xs text-muted">
        <span>{title}</span>
        <button
          type="button"
          aria-pressed={value === undefined}
          onClick={onDefault}
          title="The style's own colors"
          className={btnToggle(value === undefined)}
        >
          Style&apos;s own
        </button>
      </div>
      <div className="mt-1">
        <SwatchGrid value={value} onPick={onPick} small label={label} />
      </div>
    </div>
  );
}
