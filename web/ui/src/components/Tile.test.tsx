import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Pill } from './Pill';
import { FilterTile, Tile, TileGrid } from './Tile';

afterEach(cleanup);

describe('Tile', () => {
  it('draws the title, the badges and one secondary line, with the tone as data', () => {
    render(
      <Tile
        title="guestbook"
        tone="bad"
        badges={<Pill tone="bad">Degraded</Pill>}
        detail="default · argocd"
        data-app="guestbook"
      />,
    );

    const root = document.querySelector('[data-app="guestbook"]');
    expect(root?.tagName).toBe('DIV');
    expect(root?.getAttribute('data-tone')).toBe('bad');
    expect(root?.className).toContain('border-l-bad');
    expect(screen.getByText('guestbook').getAttribute('title')).toBe('guestbook');
    expect(screen.getByText('Degraded').dataset['pill']).toBe('bad');
    expect(screen.getByText('default · argocd').hasAttribute('data-tile-detail')).toBe(true);
    // No open control where neither `to` nor `onOpen` is given.
    expect(document.querySelector('[data-tile-open]')).toBeNull();
  });

  it('is a link to its route when given `to`, named by `openLabel`', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<Tile title="guestbook" to="/apps/guestbook" openLabel="Open Application guestbook" />} />
          <Route path="/apps/guestbook" element={<p>the application</p>} />
        </Routes>
      </MemoryRouter>,
    );

    const link = screen.getByRole('link', { name: 'Open Application guestbook' });
    expect(link.getAttribute('href')).toBe('/apps/guestbook');
    fireEvent.click(link);
    expect(screen.getByText('the application')).toBeTruthy();
  });

  it('is a button when given `onOpen`, and its action slot does not open it', () => {
    const open = vi.fn();
    const act = vi.fn();
    render(
      <Tile
        title="cache"
        onOpen={open}
        action={
          <button type="button" onClick={act}>
            Shell
          </button>
        }
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Shell' }));
    expect(act).toHaveBeenCalledOnce();
    expect(open).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'cache' }));
    expect(open).toHaveBeenCalledOnce();

    // The action is never inside the open control: a button in a button is
    // invalid HTML and unreachable by keyboard.
    const opener = screen.getByRole('button', { name: 'cache' });
    expect(opener.contains(screen.getByRole('button', { name: 'Shell' }))).toBe(false);
  });

  // On a 390px screen the third chip would be clipped: the second line wraps,
  // and the detail truncates within the tile's width on a line of its own.
  it('lets its chips and detail wrap rather than overflow', () => {
    render(
      <Tile
        title="redis"
        badges={
          <>
            <Pill>Valkey</Pill>
            <Pill>standalone</Pill>
            <Pill tone="ok">ready</Pill>
          </>
        }
        detail="1 primary · 2 replicas"
      />,
    );
    const second = document.querySelector('[data-tile-second]');
    expect(second?.className.split(' ')).toContain('flex-wrap');
    expect(screen.getByText('Valkey').parentElement?.className.split(' ')).toContain('flex-wrap');
    expect(screen.getByText('1 primary · 2 replicas').className.split(' ')).toEqual(
      expect.arrayContaining(['max-w-full', 'truncate']),
    );
  });

  // Under the stretched open control a chip's hover sentence could not be
  // reached, so the second line sits above it when the tile
  // opens, and hands a click on it to the open control. The real layout is
  // checked in measure/overview.spec.ts ("a tile's chips answer the pointer").
  it('sits its second line above the open control, and opens the tile from it', () => {
    const open = vi.fn();
    const own = vi.fn();
    render(
      <Tile
        title="cilium"
        onOpen={open}
        badges={<Pill tone="warn" title="ComparisonError: one resource drifted">OutOfSync</Pill>}
        detail={
          <>
            default ·{' '}
            <button type="button" onClick={own}>
              kube-system
            </button>
          </>
        }
      />,
    );
    const second = document.querySelector('[data-tile-second]');
    expect(second?.className.split(' ')).toEqual(expect.arrayContaining(['relative', 'z-10']));

    fireEvent.click(screen.getByText('OutOfSync'));
    expect(open).toHaveBeenCalledOnce();

    // A control of its own in the line is pressed, not the tile.
    fireEvent.click(screen.getByRole('button', { name: 'kube-system' }));
    expect(own).toHaveBeenCalledOnce();
    expect(open).toHaveBeenCalledOnce();
  });

  it('leaves the second line flat where the tile does not open', () => {
    render(<Tile title="plain" detail="nothing to open" />);
    const second = document.querySelector('[data-tile-second]');
    expect(second?.className.split(' ')).not.toContain('z-10');
  });

  it('draws children under a rule, outside the open control', () => {
    render(
      <Tile title="argocd" onOpen={() => undefined}>
        <label>
          <input type="checkbox" /> Column
        </label>
      </Tile>,
    );
    const box = screen.getByRole('checkbox');
    expect(screen.getByRole('button', { name: 'argocd' }).contains(box)).toBe(false);
  });
});

describe('TileGrid', () => {
  it('is a list whose tiles are its items', () => {
    render(
      <TileGrid label="Applications">
        <Tile title="one" />
        <Tile title="two" />
      </TileGrid>,
    );
    const list = screen.getByRole('list', { name: 'Applications' });
    expect(list.getAttribute('data-tile-grid')).toBe('regular');
    expect(screen.getAllByRole('listitem').map((item) => item.textContent)).toEqual(['one', 'two']);
  });
});

describe('FilterTile', () => {
  it('is a toggle with aria-pressed when it filters', () => {
    const pick = vi.fn();
    render(
      <TileGrid density="compact">
        <FilterTile count={3} label="Degraded" tone="bad" pressed onClick={pick} hint="Show the degraded ones" />
        <FilterTile count={0} label="Missing" />
      </TileGrid>,
    );

    const toggle = screen.getByRole('button', { name: '3 Degraded' });
    expect(toggle.getAttribute('aria-pressed')).toBe('true');
    expect(toggle.getAttribute('title')).toBe('Show the degraded ones');
    fireEvent.click(toggle);
    expect(pick).toHaveBeenCalledOnce();

    // Without `onClick` it is a reading, not a control.
    expect(screen.getAllByRole('button')).toHaveLength(1);
    expect(screen.getAllByRole('listitem')).toHaveLength(2);
  });

  // Filters must not stretch into bars on a wide screen, a lone one across the
  // whole row. Content-sized, 7 to 12rem, never
  // grown - asserted on the classes, since jsdom lays nothing out.
  it('is content-sized and never grows to fill the row', () => {
    render(
      <TileGrid density="compact">
        <FilterTile count={1} label="Out of sync" onClick={() => undefined} />
        <FilterTile count={2} label="Missing" />
      </TileGrid>,
    );
    for (const item of screen.getAllByRole('listitem')) {
      const classes = item.className.split(' ');
      expect(classes).toEqual(expect.arrayContaining(['min-w-28', 'max-w-48', 'shrink-0', 'grow-0']));
      expect(classes).not.toContain('flex-1');
      expect(classes.some((one) => one.startsWith('basis-'))).toBe(false);
    }
  });
});
