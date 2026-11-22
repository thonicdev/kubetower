import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';

import type { ClusterColour } from '../api';
import { MenuScrim } from '../components/MenuScrim';
import { focusRing } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { colourLabel, PALETTE, plateClass } from './clusterState';

/**
 * The sixteen nameplate colors as a grid to point at: eight deep hues over
 * their eight pale versions, the order the server's
 * palette is in.
 *
 * A `radiogroup`, because exactly one is chosen. The arrow keys move the choice
 * along the grid - left and right within a row, up and down between the deep
 * and the pale half - and only the chosen swatch is in the tab order, which is
 * how a radio group behaves everywhere else. Each swatch is named by its color
 * (`colourLabel`), so a screen reader hears "Pale teal" rather than a key.
 *
 * Every class comes from `plateClass()`: the color is a palette key all the way
 * down, and no value from the wire reaches a class or a style.
 */
export function SwatchGrid({
  value,
  onPick,
  small = false,
  label = 'Color',
}: {
  /** The swatch drawn as chosen: the person's, or the automatic one.
   *  `undefined` only from a server older than the contract's required
   *  `colour`: nothing is drawn as chosen, and the first swatch takes the tab. */
  value: ClusterColour | undefined;
  onPick: (key: ClusterColour) => void;
  /** The 16px swatches of an Arrange row, against the editor's 24px. */
  small?: boolean;
  label?: string;
}): ReactNode {
  const grid = useRef<HTMLDivElement>(null);

  function move(event: KeyboardEvent<HTMLButtonElement>, at: number): void {
    const step: Readonly<Record<string, number>> = {
      ArrowLeft: -1,
      ArrowRight: 1,
      ArrowUp: -8,
      ArrowDown: 8,
    };
    const by = step[event.key];
    if (by === undefined) return;
    event.preventDefault();
    const next = PALETTE[(at + by + PALETTE.length) % PALETTE.length];
    if (next === undefined) return;
    onPick(next);
    grid.current?.querySelector<HTMLButtonElement>(`[data-swatch="${next}"]`)?.focus();
  }

  return (
    <div
      ref={grid}
      role="radiogroup"
      aria-label={label}
      className={`grid shrink-0 grid-cols-8 ${small ? 'gap-2' : 'gap-1.5'}`}
    >
      {PALETTE.map((key, at) => (
        <button
          key={key}
          type="button"
          role="radio"
          aria-checked={key === value}
          aria-label={colourLabel(key)}
          title={colourLabel(key)}
          data-swatch={key}
          tabIndex={key === (value ?? PALETTE[0]) ? 0 : -1}
          onClick={() => onPick(key)}
          onKeyDown={(event) => move(event, at)}
          // A small swatch is drawn at 16px and takes 24px of pointer: the box
          // reaches 4px into the 8px gap on each side, so neighbors never overlap.
          className={`${small ? 'relative size-4 before:absolute before:-inset-1' : 'size-6'} rounded-md ${plateClass(key)} ${focusRing} ${
            key === value ? 'ring-2 ring-fg ring-offset-2 ring-offset-panel' : ''
          }`}
        />
      ))}
    </div>
  );
}

/**
 * The narrow form of the grid: one button in the chosen color that opens the
 * sixteen, for Arrange below the width where the inline grid fits.
 *
 * The grid opens under the button, over everything, and closes on a pick, on a
 * click elsewhere and on Escape - claimed **above** the Arrange dialog's own
 * layer, so Escape closes the grid and leaves the dialog open.
 */
export function ColourButton({
  value,
  onPick,
  label,
}: {
  value: ClusterColour | undefined;
  onPick: (key: ClusterColour) => void;
  /** Whose color this is, for the button's name: "Color for prod". */
  label: string;
}): ReactNode {
  const [open, setOpen] = useState(false);
  const panel = useId();
  const button = useRef<HTMLButtonElement>(null);
  const close = (): void => {
    setOpen(false);
    button.current?.focus();
  };
  useEscape(KeyLayer.Dialog + 1, close, open);

  return (
    <span className="relative inline-flex">
      <button
        ref={button}
        type="button"
        aria-label={`${label}: ${colourLabel(value)}`}
        title={`${label}: ${colourLabel(value)}`}
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? panel : undefined}
        data-colour-button={value}
        onClick={() => setOpen((was) => !was)}
        className={`size-6 rounded-md ${plateClass(value)} ${focusRing}`}
      />
      {open ? (
        <>
          <MenuScrim onDismiss={close} />
          <div
            id={panel}
            className="absolute top-full left-0 z-20 mt-1 rounded-lg border border-line bg-panel p-2 shadow-2xl"
          >
            <SwatchGrid
              value={value}
              label={label}
              onPick={(key) => {
                onPick(key);
                close();
              }}
            />
          </div>
        </>
      ) : null}
    </span>
  );
}
