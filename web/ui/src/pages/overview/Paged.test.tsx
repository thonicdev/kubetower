import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { Paged } from './Paged';

afterEach(cleanup);

const items = (count: number): number[] => Array.from({ length: count }, (_, index) => index);

function Rows({ list }: { list: number[] }) {
  return (
    <Paged items={list} what="Rows">
      {(shown) => <p data-testid="shown">{shown.join(',')}</p>}
    </Paged>
  );
}

const shown = (container: HTMLElement): string[] =>
  (container.querySelector('[data-testid="shown"]')?.textContent ?? '').split(',').filter((part) => part !== '');

const click = (container: HTMLElement, name: string): void => {
  const found = Array.from(container.querySelectorAll('button')).find((button) => button.textContent === name);
  if (found === undefined) throw new Error(`no ${name} button`);
  fireEvent.click(found);
};

describe('Paged', () => {
  it('clamps to the last page that still exists when a refetch shortens the list', () => {
    const { container, rerender } = render(<Rows list={items(245)} />);
    click(container, 'Next');
    click(container, 'Next');
    expect(shown(container)).toHaveLength(45);
    expect(container.textContent).toContain('201–245 of 245');

    rerender(<Rows list={items(125)} />);

    expect(shown(container)).toHaveLength(25);
    expect(container.textContent).toContain('101–125 of 125');
  });

  it('falls back to the first page, with no controls, when the list fits one page', () => {
    const { container, rerender } = render(<Rows list={items(245)} />);
    click(container, 'Next');

    rerender(<Rows list={items(60)} />);

    expect(shown(container)).toHaveLength(60);
    expect(container.querySelector('nav')).toBeNull();
  });
});

describe('Paged, over a list the server cut', () => {
  // The server sends at most 400 rows of a longer list. Without the total the
  // last page would read "301–400 of 400", which is the end of the list.
  function Cut({ list, total }: { list: number[]; total: number | undefined }) {
    return (
      <Paged items={list} total={total} what="Rows">
        {(rows) => <p data-testid="shown">{rows.join(',')}</p>}
      </Paged>
    );
  }

  it('says on the last page that the list was cut, and by how much', () => {
    const { container } = render(<Cut list={items(400)} total={450} />);
    for (let page = 0; page < 3; page += 1) click(container, 'Next');

    expect(shown(container)).toHaveLength(100);
    expect(container.textContent).toContain('301–400 of 450, the first 400 shown');
  });

  it('says so on the first page too, before anyone reaches the end', () => {
    const { container } = render(<Cut list={items(400)} total={450} />);

    expect(container.textContent).toContain('1–100 of 450, the first 400 shown');
  });

  it('says nothing of a cut when the total is the rows carried, or unknown', () => {
    const { container, rerender } = render(<Cut list={items(245)} total={245} />);
    expect(container.textContent).toContain('1–100 of 245');
    expect(container.textContent).not.toContain('shown');

    rerender(<Cut list={items(245)} total={undefined} />);
    expect(container.textContent).toContain('1–100 of 245');
    expect(container.textContent).not.toContain('shown');
  });
});
