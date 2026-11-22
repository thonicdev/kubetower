import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

// The package entry, and nothing deeper: this file asks what a consumer in
// another repository receives, which is exactly what `index.ts` lists and not
// what `components/` happens to contain.
import {
  bodyCell,
  headCell,
  HEAD_ROW,
  Pill,
  ROW,
  TABLE,
  Tile,
  TileGrid,
  trail,
  type Crumb,
  type PageFacts,
  type PillProps,
  type TileProps,
} from './index';

afterEach(cleanup);

describe('@kubetower/ui', () => {
  // An extra that grades a status draws it with the console's chip, or it
  // invents a second one. The entry is what it can reach, so the entry is what
  // is checked - importing `components/Pill` here would pass with the export
  // removed.
  it('exports the graded-status chip', () => {
    // Type-level half: the props an extra writes compile against the export.
    const props: PillProps = { tone: 'bad', title: 'Degraded', children: 'Degraded' };

    render(<Pill {...props} />);

    const chip = screen.getByText('Degraded');
    expect(chip.dataset['pill']).toBe('bad');
    expect(chip.getAttribute('title')).toBe('Degraded');
  });

  // An extra's page whose title is a path links its segments the way the
  // core's pages do.
  it('exports the breadcrumb trail', () => {
    const list: Crumb = { label: 'Applications', to: '/c/prod/x/argocd' };
    const facts: PageFacts = trail(list, { label: 'api' });
    expect(facts).toEqual({ title: 'Applications / api', trail: [list, { label: 'api' }] });
  });

  // The tile an extra's page draws its things with (Argo CD's Applications,
  // Redis's deployments): reachable from the entry, props and all.
  it('exports the tile and its grid', () => {
    const props: TileProps = { title: 'guestbook', tone: 'ok', detail: 'default · argocd' };

    render(
      <TileGrid label="Applications">
        <Tile {...props} />
      </TileGrid>,
    );

    const item = screen.getByRole('listitem');
    expect(item.dataset['tone']).toBe('ok');
    expect(screen.getByText('default · argocd')).toBeTruthy();
  });

  // An extra's table is drawn with the console's look, reached from the entry.
  it("exports the tables' look", () => {
    expect(TABLE).toContain('kt-grid');
    expect(HEAD_ROW).toContain('uppercase');
    expect(ROW).toContain('border-b');
    expect(headCell()).toContain('px-2');
    expect(bodyCell(true)).toContain('h-7');
  });
});
