import { describe, expect, it } from 'vitest';

import { LOG_CAP, LOG_WINDOW, LogBudget, LogBuffer, OVERLAP, compileFilter } from './logBuffer';

/**
 * `prepend`, which is the other end of `push` and the only other operation that
 * changes the array's front.
 *
 * It gets its own file because the failure mode is silent. `lines`, `pretty` and
 * `styled` are index-parallel and `shown` holds indices into them, so a prepend
 * that forgets to shift `shown` does not throw - it draws the wrong lines, which
 * reads as a container having printed something it did not. Nothing above this
 * layer can tell the difference.
 *
 * The Go side's parity spec is `internal/logs/earlier_test.go`, whose eleven
 * functions pin the *window arithmetic*. Nothing here re-tests that: the server
 * decides which lines a page holds, and this decides what happens to them when
 * they arrive.
 */
describe('prepend', () => {
  function filled(...texts: string[]): LogBuffer {
    const buffer = new LogBuffer();
    for (const text of texts) buffer.push({ text });
    return buffer;
  }

  it('puts the block in front, oldest first', () => {
    const buffer = filled('three', 'four');
    const added = buffer.prepend([{ text: 'one' }, { text: 'two' }]);

    expect(added).toBe(2);
    expect(buffer.lines.map((line) => line.text)).toEqual(['one', 'two', 'three', 'four']);
  });

  it('shifts every index already drawn', () => {
    // With no filter `shown` is 0…n-1 in full, so a prepend that forgot to shift
    // would leave it pointing at the block that just arrived - the pane would
    // draw the earlier lines twice and lose the later ones, with nothing
    // anywhere reporting a fault.
    const buffer = filled('three', 'four');
    buffer.view(null, false);
    buffer.prepend([{ text: 'one' }, { text: 'two' }]);

    expect([...buffer.view(null, false)]).toEqual([0, 1, 2, 3]);
    expect(buffer.shownText(2)).toBe('three');
  });

  it('filters the arriving block the way it filters anything else', () => {
    const buffer = filled('keep me', 'drop this');
    const filter = compileFilter('keep', false, false);
    buffer.view(filter.pattern, false);

    buffer.prepend([{ text: 'keep this too' }, { text: 'not this' }]);

    // Indices 0 and 2 are the two that match, in buffer order.
    expect([...buffer.view(filter.pattern, false)]).toEqual([0, 2]);
    expect(buffer.shownText(0)).toBe('keep this too');
    expect(buffer.shownText(2)).toBe('keep me');
  });

  it('trims a duplicate overlap rather than showing it twice', () => {
    // `before` is what the client said it held, and a line rotated away between
    // the two reads makes the server's block overlap what is already here. A
    // repeated line reads as the container having printed twice, which is a
    // claim about the workload.
    const buffer = filled('b', 'c');
    const added = buffer.prepend([{ text: 'a' }, { text: 'b' }, { text: 'c' }]);

    expect(added).toBe(1);
    expect(buffer.lines.map((line) => line.text)).toEqual(['a', 'b', 'c']);
  });

  it('does not look past the overlap it declares', () => {
    // The trim is bounded so that a log which genuinely repeats itself for
    // thousands of lines is not collapsed into one. Past OVERLAP the block is
    // taken as adjacent, which is what the server said it was.
    const repeated = Array.from({ length: OVERLAP + 10 }, () => ({ text: 'same' }));
    const buffer = new LogBuffer();
    for (const line of repeated) buffer.push(line);

    const added = buffer.prepend(repeated);
    expect(added).toBe(repeated.length - OVERLAP);
  });

  it('adds nothing for an empty page', () => {
    const buffer = filled('one');
    expect(buffer.prepend([])).toBe(0);
    expect(buffer.length).toBe(1);
  });

  it('adds nothing when the whole page is already held', () => {
    // Not the same as an empty page, and the caller has to be able to tell them
    // apart: this one means the scroll position must not be compensated, because
    // nothing arrived to compensate for.
    const buffer = filled('a', 'b');
    expect(buffer.prepend([{ text: 'a' }, { text: 'b' }])).toBe(0);
    expect(buffer.length).toBe(2);
  });
});

/**
 * The cap. The server's bounds are per stream and a panel is not a stream: a
 * stream that reconnects and appends its whole tail again each time is bounded
 * by nothing else.
 */
describe('the cap', () => {
  it('is a hundred thousand lines unless told otherwise', () => {
    expect(new LogBuffer().cap).toBe(LOG_CAP);
    expect(LOG_CAP).toBe(100_000);
  });

  it('is never exceeded, whatever arrives', () => {
    const buffer = new LogBuffer(50);
    let most = 0;
    for (let i = 0; i < 1_000; i++) {
      buffer.push({ text: `line ${i}` });
      most = Math.max(most, buffer.length);
    }
    expect(most).toBe(50);
    // The newest lines are the ones kept, and what went is counted.
    expect(buffer.lines[buffer.length - 1]?.text).toBe('line 999');
    expect(buffer.dropped).toBe(1_000 - buffer.length);
    expect(buffer.dropped + buffer.length).toBe(1_000);
  });

  it('moves every drawn index with the lines it lets go', () => {
    // `shown` holds indices into `lines`. A trim that forgot to move them would
    // draw the wrong lines, which reads as the container having printed them.
    const buffer = new LogBuffer(20);
    const filter = compileFilter('even', false, false);
    buffer.view(filter.pattern, false);
    for (let i = 0; i < 100; i++) buffer.push({ text: i % 2 === 0 ? `even ${i}` : `odd ${i}` });

    const shown = buffer.view(filter.pattern, false);
    expect(shown.length).toBeGreaterThan(0);
    for (const index of shown) expect(buffer.shownText(index)).toMatch(/^even /);
    expect(buffer.shownText(shown[shown.length - 1] ?? -1)).toBe('even 98');
    // And a fresh filter over what is held agrees with the one kept up to date.
    const again = [...buffer.view(null, false)].filter((index) => /even/.test(buffer.shownText(index)));
    expect([...buffer.view(filter.pattern, false)]).toEqual(again);
  });

  it('takes from a page backwards only what fits, and counts the rest', () => {
    const buffer = new LogBuffer(10);
    for (let i = 0; i < 8; i++) buffer.push({ text: `late ${i}` });

    const added = buffer.prepend(Array.from({ length: 5 }, (_, i) => ({ text: `early ${i}` })));
    expect(added).toBe(2);
    expect(buffer.length).toBe(10);
    // The two nearest the head, so what is held stays contiguous.
    expect(buffer.lines[0]?.text).toBe('early 3');
    expect(buffer.lines[1]?.text).toBe('early 4');
    expect(buffer.dropped).toBe(3);
  });

  it('starts counting again on a reset', () => {
    const buffer = new LogBuffer(10);
    for (let i = 0; i < 30; i++) buffer.push({ text: `${i}` });
    expect(buffer.dropped).toBeGreaterThan(0);
    buffer.reset();
    expect(buffer.dropped).toBe(0);
    expect(buffer.length).toBe(0);
  });
});

/**
 * Where a pane holding a selection stops drawing. The
 * window's end is named by a line and not by an index, because the index moves
 * under a trim or a page backwards while the reader is still selecting.
 */
describe('shownThrough', () => {
  it('counts what is drawn up to the line, with or without a filter', () => {
    const buffer = new LogBuffer();
    for (let i = 0; i < 10; i++) buffer.push({ text: i % 2 === 0 ? `even ${i}` : `odd ${i}` });
    const sixth = buffer.lines[5];
    if (sixth === undefined) throw new Error('no sixth line');

    buffer.view(null, false);
    expect(buffer.shownThrough(sixth)).toBe(6);
    // `odd 5` is filtered out; the three evens before it are what is drawn.
    buffer.view(compileFilter('even', false, false).pattern, false);
    expect(buffer.shownThrough(sixth)).toBe(3);
  });

  it('follows the line through a page backwards', () => {
    const buffer = new LogBuffer();
    for (let i = 0; i < 4; i++) buffer.push({ text: `late ${i}` });
    const held = buffer.lines[3];
    if (held === undefined) throw new Error('no fourth line');
    buffer.view(null, false);
    buffer.prepend([{ text: 'early 0' }, { text: 'early 1' }]);
    buffer.push({ text: 'newer' });

    expect(buffer.shownThrough(held)).toBe(6);
  });

  it('follows the line through a trim, and says when the line itself went', () => {
    const buffer = new LogBuffer(20);
    for (let i = 0; i < 20; i++) buffer.push({ text: `${i}` });
    const kept = buffer.lines[15];
    const gone = buffer.lines[0];
    if (kept === undefined || gone === undefined) throw new Error('short buffer');
    buffer.view(null, false);
    for (let i = 20; i < 25; i++) buffer.push({ text: `${i}` });

    expect(buffer.dropped).toBeGreaterThan(0);
    expect(buffer.shownThrough(kept)).toBe(16 - buffer.dropped);
    expect(buffer.shownThrough(gone)).toBeNull();
  });
});

/**
 * A reconnect's replayed tail. Its head repeats the end of the buffer, and only
 * what follows is new.
 */
describe('appendReplay', () => {
  function filled(...texts: string[]): LogBuffer {
    const buffer = new LogBuffer();
    for (const text of texts) buffer.push({ text });
    return buffer;
  }
  const block = (...texts: string[]) => texts.map((text) => ({ text }));

  it('appends only what follows the lines already held', () => {
    const buffer = filled('a', 'b', 'c');
    expect(buffer.appendReplay(block('b', 'c', 'd', 'e'))).toBe(2);
    expect(buffer.lines.map((line) => line.text)).toEqual(['a', 'b', 'c', 'd', 'e']);
  });

  it('takes the longest overlap on a log that repeats itself', () => {
    // A short match is the likelier coincidence. Taking `x` alone as the overlap
    // would append `x x y` - two lines the container did not print again.
    const buffer = filled('x', 'x', 'x');
    expect(buffer.appendReplay(block('x', 'x', 'x', 'y'))).toBe(3);
    expect(buffer.lines.map((line) => line.text)).toEqual(['x', 'x', 'x', 'y']);
  });

  it('says when the block does not continue the buffer, and appends nothing', () => {
    const buffer = filled('old');
    expect(buffer.appendReplay(block('new'))).toBeNull();
    expect(buffer.lines.map((line) => line.text)).toEqual(['old']);
  });

  it('appends everything to an empty buffer', () => {
    const buffer = new LogBuffer();
    expect(buffer.appendReplay(block('a', 'b'))).toBe(0);
    expect(buffer.length).toBe(2);
  });
});

/**
 * `LOG_CAP` is per pane and the dock holds sixteen, so alone it would allow
 * sixteen times a hundred thousand lines plus their decoded and indented
 * copies. Two bounds narrow it: one budget of lines shared by every pane, and
 * decoded copies only for what is drawn.
 */
describe('the dock-wide budget', () => {
  it('holds sixteen panes within one budget, a pane that has ended included', () => {
    const budget = new LogBudget(1_600);
    const first = new LogBuffer();
    budget.join(first);
    // Alone under the budget it may hold all of it, and then its stream ends:
    // it receives nothing more, so only the budget can make it let go.
    for (let i = 0; i < 1_600; i++) first.push({ text: `first ${String(i)}` });
    expect(first.length).toBe(1_600);

    const others = Array.from({ length: 15 }, () => new LogBuffer());
    for (const pane of others) {
      budget.join(pane);
      for (let i = 0; i < 1_000; i++) pane.push({ text: `line ${String(i)}` });
    }
    const held = [first, ...others].reduce((sum, pane) => sum + pane.length, 0);
    expect(held).toBeLessThanOrEqual(1_600);
    expect(first.length).toBeLessThanOrEqual(100);
    // Letting go is said, as for the per-pane cap: the pane counts what went.
    expect(first.dropped).toBeGreaterThan(0);
  });

  it('keeps a pane own cap when the budget is larger', () => {
    const budget = new LogBudget(1_000);
    const pane = new LogBuffer(50);
    budget.join(pane);
    for (let i = 0; i < 200; i++) pane.push({ text: String(i) });
    expect(pane.cap).toBe(50);
    expect(pane.length).toBeLessThanOrEqual(50);
  });

  it('tells a pane it trimmed, and only that one', () => {
    const budget = new LogBudget(100);
    const full = new LogBuffer();
    const told: string[] = [];
    budget.join(full, () => told.push('full'));
    for (let i = 0; i < 80; i++) full.push({ text: String(i) });
    budget.join(new LogBuffer(), () => told.push('new'));
    expect(full.length).toBe(50);
    expect(told).toEqual(['full']);
  });

  it('gives a closed pane share back to the ones that stay', () => {
    const budget = new LogBudget(100);
    const a = new LogBuffer();
    const b = new LogBuffer();
    budget.join(a);
    budget.join(b);
    expect(a.cap).toBe(50);
    budget.leave(b);
    expect(a.cap).toBe(100);
  });
});

describe('what is decoded', () => {
  it('decodes nothing for a filter, and no more than the drawn window for a draw', () => {
    const buffer = new LogBuffer();
    for (let i = 0; i < 5_000; i++) buffer.push({ text: `\u001b[31merror ${String(i)}\u001b[0m` });
    const shown = buffer.view(/error/g, false);
    expect(shown).toHaveLength(5_000);
    // The filter tests the decoded text, and keeps none of it.
    expect(buffer.decodedLines).toBe(0);

    buffer.keepDecoded(LOG_WINDOW);
    for (const index of shown) expect(buffer.shownText(index)).toMatch(/^error \d+$/);
    expect(buffer.decodedLines).toBeLessThanOrEqual(LOG_WINDOW);
    // And what it kept is still the right answer for the right line.
    expect(buffer.decoded(4_999).text).toBe('error 4999');
    expect(buffer.decoded(0).text).toBe('error 0');
  });

  it('lets go of the line drawn longest ago, not the one first drawn', () => {
    const buffer = new LogBuffer();
    for (let i = 0; i < 3; i++) buffer.push({ text: `line ${String(i)}` });
    buffer.keepDecoded(2);
    const first = buffer.decoded(0);
    buffer.decoded(1);
    // Line 0 is drawn again, so it is the newest use; line 2 then pushes out 1.
    expect(buffer.decoded(0)).toBe(first);
    buffer.decoded(2);
    expect(buffer.decodedLines).toBe(2);
    expect(buffer.decoded(0)).toBe(first);
  });

  it('keeps indented copies within the same bound', () => {
    const buffer = new LogBuffer();
    for (let i = 0; i < 3_000; i++) buffer.push({ text: `{"n":${String(i)}}` });
    const shown = buffer.view(/n/g, true);
    expect(buffer.decodedLines).toBe(0);
    buffer.keepDecoded(100);
    for (const index of shown) buffer.shownText(index);
    expect(buffer.decodedLines).toBeLessThanOrEqual(100);
    expect(buffer.shownText(2_999)).toContain('"n": 2999');
  });
});
