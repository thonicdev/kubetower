import { useId, useState, type ReactNode } from 'react';

import { btnPrimary, btnSecondary } from '../components/buttons';
import { Dialog } from '../components/Dialog';
import { focusRing, focusRingTight } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { ClusterIcon } from './ClusterIcon';
import { IconCustomise } from './IconCustomise';
import {
  ICON_CATEGORIES,
  ICON_STYLES,
  iconStyleLabel,
  MAX_SEED,
  randomSeed,
  type IconRecipe,
  type IconStyle,
} from './iconRecipe';

type Tab = 'all' | (typeof ICON_CATEGORIES)[number]['id'];

/**
 * The cluster's icon, chosen and dressed. Opened from the
 * right-click menu's "Customize
 * icon…".
 *
 * Left, which style: KubeTower's own, every one drawn with this cluster's seed
 * and colors so the choice is made by looking, in groups and filterable by
 * name; then the seed that varies them, Randomize, and Surprise
 * me (a random style and seed). Right, how to draw it: background, fill, ink,
 * rotate, scale and mirror, previewed at the rail's size and larger.
 *
 * Each style is a chunk of its own; opening the dialog fetches the ones it
 * shows. The console's `Dialog`, so the keyboard, Escape and focus are its.
 */
export function IconModal({
  context,
  label,
  initial,
  pending,
  error,
  onSave,
  onClose,
}: {
  context: string;
  label: string;
  initial: IconRecipe;
  pending: boolean;
  error: string | undefined;
  onSave: (recipe: IconRecipe) => void;
  onClose: () => void;
}): ReactNode {
  const [recipe, setRecipe] = useState<IconRecipe>(initial);
  const [tab, setTab] = useState<Tab>('all');
  const [filter, setFilter] = useState('');
  const filterId = useId();
  const seedId = useId();

  const inTab: readonly IconStyle[] =
    tab === 'all' ? ICON_STYLES : (ICON_CATEGORIES.find((one) => one.id === tab)?.styles ?? []);
  const words = filter.toLowerCase().split(/\s+/).filter((word) => word !== '');
  const shown = inTab.filter((style) => words.every((word) => iconStyleLabel(style).toLowerCase().includes(word)));
  const seed = recipe.seed === '' ? context.slice(0, MAX_SEED) : recipe.seed;

  const chips: { id: Tab; text: string }[] = [
    { id: 'all', text: `All styles (${String(ICON_STYLES.length)})` },
    ...ICON_CATEGORIES.map((one) => ({ id: one.id, text: `${one.label} (${String(one.styles.length)})` })),
  ];

  return (
    <Dialog title={`Icon for ${label}`} onClose={onClose} size="viewport" layer={KeyLayer.Dialog}>
      <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_calc(var(--spacing)*72)]">
        <section aria-label="Choose a style" className="flex min-w-0 flex-col gap-2">
          <div role="tablist" aria-label="Style groups" className="flex flex-wrap gap-1">
            {chips.map((chip) => (
              <button
                key={chip.id}
                type="button"
                role="tab"
                aria-selected={tab === chip.id}
                data-group={chip.id}
                onClick={() => setTab(chip.id)}
                className={`inline-flex min-h-6 items-center rounded-full border px-2.5 py-0.5 text-xs pointer-coarse:min-h-11 ${focusRing} ${
                  tab === chip.id ? 'border-accent bg-panel-2 text-fg' : 'border-line text-muted hover:text-fg'
                }`}
              >
                {chip.text}
              </button>
            ))}
          </div>
          <label htmlFor={filterId} className="sr-only">
            Filter styles
          </label>
          <input
            id={filterId}
            type="search"
            data-dialog-focus=""
            value={filter}
            placeholder="Filter styles: pod, server, database…"
            spellCheck={false}
            onChange={(event) => setFilter(event.target.value)}
            className={`w-full rounded-md border border-line bg-panel-2 px-2 py-1 text-sm text-fg ${focusRingTight}`}
          />
          <div className="max-h-[50vh] min-h-40 overflow-y-auto rounded-md border border-line p-2">
            {shown.length === 0 ? (
              <p role="status" className="p-2 text-xs text-muted">
                No style is called that.
              </p>
            ) : (
              <div
                role="radiogroup"
                aria-label={`Style for ${label}`}
                className="grid grid-cols-4 gap-2 sm:grid-cols-6 lg:grid-cols-8"
              >
                {shown.map((style) => (
                  <button
                    key={style}
                    type="button"
                    role="radio"
                    aria-checked={style === recipe.style}
                    aria-label={iconStyleLabel(style)}
                    title={iconStyleLabel(style)}
                    data-icon-style={style}
                    onClick={() => setRecipe({ ...recipe, style, seed })}
                    className={`flex flex-col items-center gap-1 rounded-lg p-1 text-2xs text-muted hover:bg-panel-2 ${focusRing} ${
                      style === recipe.style ? 'bg-panel-2 text-fg ring-2 ring-accent' : ''
                    }`}
                  >
                    <span className="relative flex size-12 overflow-hidden rounded-lg bg-panel-2">
                      <ClusterIcon recipe={{ ...recipe, style, seed }} fallback={null} />
                    </span>
                    <span className="w-full truncate text-center">{iconStyleLabel(style)}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
          <label htmlFor={seedId} className="text-xs text-muted">
            Seed - the same seed always draws the same icon
          </label>
          <div className="-mt-1 flex flex-wrap items-center gap-2">
            <input
              id={seedId}
              value={seed}
              maxLength={MAX_SEED}
              spellCheck={false}
              onChange={(event) => setRecipe({ ...recipe, seed: event.target.value })}
              className={`min-w-0 flex-1 rounded-sm border border-line bg-panel-2 px-1.5 py-1 font-mono text-xs text-fg ${focusRingTight}`}
            />
            <button
              type="button"
              onClick={() => setRecipe({ ...recipe, seed: randomSeed() })}
              title="A random seed, same style"
              className={btnSecondary}
            >
              Randomize
            </button>
            <button
              type="button"
              onClick={() => {
                const pick = crypto.getRandomValues(new Uint32Array(1))[0] ?? 0;
                setRecipe({ ...recipe, style: ICON_STYLES[pick % ICON_STYLES.length] ?? recipe.style, seed: randomSeed() });
              }}
              title="A random style and a random seed"
              className={btnSecondary}
            >
              Surprise me
            </button>
          </div>
        </section>

        <section aria-label="Customize the icon" className="flex flex-col gap-3">
          <div className="flex items-end gap-3">
            <span data-preview="rail" className="relative flex size-9 shrink-0 overflow-hidden rounded-lg bg-panel-2">
              <ClusterIcon recipe={recipe} fallback={null} />
            </span>
            <span data-preview="large" className="relative flex size-24 shrink-0 overflow-hidden rounded-2xl bg-panel-2">
              <ClusterIcon recipe={recipe} fallback={null} />
            </span>
            <p data-chosen-style="" className="min-w-0 text-2xs break-words text-muted">
              {iconStyleLabel(recipe.style)}
            </p>
          </div>
          <IconCustomise recipe={recipe} onChange={setRecipe} label={label} />

          {error === undefined ? null : (
            <p role="alert" className="text-xs text-bad">
              {error}
            </p>
          )}
          {/* The act first and the way back after it, as in every dialog's foot. */}
          <div className="mt-auto flex items-center gap-2">
            <button
              type="button"
              // Not `disabled`: that would drop the keyboard out of the dialog.
              aria-disabled={pending}
              title={pending ? 'Saving…' : undefined}
              onClick={() => {
                if (!pending) onSave(recipe);
              }}
              className={`${btnPrimary} aria-disabled:cursor-not-allowed aria-disabled:opacity-50`}
            >
              Save
            </button>
            <button
              type="button"
              onClick={onClose}
              className={btnSecondary}
            >
              Cancel
            </button>
          </div>
        </section>
      </div>
    </Dialog>
  );
}
