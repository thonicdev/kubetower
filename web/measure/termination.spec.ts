import { expect, test } from '@playwright/test';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* 15 — a termination message stays in the panel                             */
/* -------------------------------------------------------------------------- */

/**
 * The pod panel at the drawer's width, two restarted containers whose previous
 * run left a message nothing can break at a space: one line of JSON, 176
 * characters, and a tail near the kubelet's 4 KB cap holding an 800-character
 * token. Unwrapped, the first would make the panel far wider than itself and
 * the second one line over a thousand pixels tall.
 *
 * And a container *currently* `terminated: Error` whose message is a fifteen-line
 * Trivy tail, pasted whole under the exit code. Drawn beside the same container
 * with no message, so what the message costs the card is measured, not
 * guessed: closed, one line.
 *
 * Closed, each message is one control and one line, cut. Opened - by a click,
 * and the current one by Enter on the keyboard - the whole of it with its
 * newlines and none of its escapes, inside a box at most `max-h-48` (192px)
 * that scrolls vertically. In both states, nothing in the panel or the page
 * scrolls sideways.
 */
for (const width of [1024, 1280]) {
  test(`a termination message is folded, scrolls nothing sideways and stays bounded at ${String(width)}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await show(page, 'termination-message');
    await page.waitForSelector('[data-folded-message]');

    const read = (): Promise<{
      panel: { scroll: number; client: number };
      page: { scroll: number; client: number };
      cards: { loud: number; silent: number };
      messages: { of: string; first: number; height: number; line: number; open: boolean; text: string; body: { height: number; scroll: number; client: number; scrollHeight: number; whiteSpace: string } }[];
    }> =>
      page.evaluate(() => {
        const panel = document.querySelector('[data-pod-panel]');
        if (panel === null) throw new Error('the pod panel is not drawn');
        // Below the header: the header wraps by the length of the name
        // and the two cards' names differ, so the whole
        // card would measure the name as well as the message.
        const card = (name: string): number => {
          const tile = document.querySelector(`[data-container="${name}"]`);
          const header = tile?.querySelector('header');
          if (tile == null || header == null) return Number.NaN;
          return tile.getBoundingClientRect().bottom - header.getBoundingClientRect().bottom;
        };
        return {
          panel: { scroll: panel.scrollWidth, client: panel.clientWidth },
          page: { scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth },
          cards: { loud: card('grafana-sc-datasources'), silent: card('grafana-sc-silent') },
          messages: [...document.querySelectorAll<HTMLDetailsElement>('details[data-folded-message]')].map((details) => {
            const summary = details.querySelector('summary');
            const line = details.querySelector('[data-folded-message-line]');
            const body = details.querySelector('[data-folded-message-body]');
            if (summary === null || line === null || !(body instanceof HTMLElement)) throw new Error('a message without its parts');
            const box = body.getBoundingClientRect();
            return {
              of: details.dataset['messageOf'] ?? '',
              first: line.textContent.length,
              height: summary.getBoundingClientRect().height,
              line: Number.parseFloat(getComputedStyle(line).lineHeight),
              open: details.open,
              text: body.textContent,
              body: {
                height: box.height,
                scroll: body.scrollWidth,
                client: body.clientWidth,
                scrollHeight: body.scrollHeight,
                whiteSpace: getComputedStyle(body).whiteSpace,
              },
            };
          }),
        };
      });

    const closed = await read();
    expect(closed.messages.map((message) => message.of), 'two last terminations and one current state').toEqual(['last', 'last', 'state']);
    expect(closed.messages[0]?.first, 'the JSON line is 176 characters').toBe(176);
    for (const [at, message] of closed.messages.entries()) {
      expect(message.open, `message ${String(at)} is closed until asked`).toBe(false);
      expect(message.height, `message ${String(at)} closed is ${px(message.height)} tall, one line is ${px(message.line)}`).toBeLessThan(2 * message.line);
    }
    // The terminated card: fifteen lines of tail cost it the one line of the control and a row gap.
    const cost = closed.cards.loud - closed.cards.silent;
    const state = closed.messages[2];
    expect(cost, `the fifteen-line message adds ${px(cost)} to its card closed`).toBeLessThanOrEqual((state?.height ?? 0) + 8.5);
    expect(closed.panel.scroll, `closed: the panel is ${px(closed.panel.scroll)} in ${px(closed.panel.client)}`).toBeLessThanOrEqual(closed.panel.client);
    expect(closed.page.scroll, `closed: the page is ${px(closed.page.scroll)} in ${px(closed.page.client)}`).toBeLessThanOrEqual(closed.page.client);

    // The two last terminations by a click; the current one from the keyboard.
    for (const summary of await page.locator('[data-message-of="last"] > summary').all()) await summary.click();
    const control = page.locator('[data-container="grafana-sc-datasources"] [data-message-of="state"] > summary');
    await control.focus();
    await page.keyboard.press('Enter');

    const opened = await read();
    for (const [at, message] of opened.messages.entries()) {
      const where = `message ${String(at)} opened`;
      expect(message.open, `${where} is open`).toBe(true);
      expect(message.body.whiteSpace, `${where}: its newlines are kept`).toBe('pre-wrap');
      expect(message.body.height, `${where} is ${px(message.body.height)} tall`).toBeLessThanOrEqual(192.5);
      expect(message.body.scroll, `${where}: ${px(message.body.scroll)} wide in ${px(message.body.client)}`).toBeLessThanOrEqual(message.body.client);
    }
    // The 4 KB tail does not fit 192px: it scrolls inside its box, and the rest is still there.
    const tail = opened.messages[1];
    expect(tail?.body.scrollHeight ?? 0, 'the 4 KB tail scrolls vertically inside its box').toBeGreaterThan(tail?.body.height ?? 0);
    // The Trivy tail: every line, no escape of either spelling.
    const trivy = opened.messages[2]?.text ?? '';
    expect(trivy.split('\n'), 'the fifteen lines are all there').toHaveLength(15);
    expect(trivy, 'no ESC byte is drawn').not.toContain('\x1b');
    expect(trivy, 'no orphaned color code is drawn').not.toMatch(/\[\d+m/);
    expect(opened.panel.scroll, `opened: the panel is ${px(opened.panel.scroll)} in ${px(opened.panel.client)}`).toBeLessThanOrEqual(opened.panel.client);
    expect(opened.page.scroll, `opened: the page is ${px(opened.page.scroll)} in ${px(opened.page.client)}`).toBeLessThanOrEqual(opened.page.client);
  });
}

/* -------------------------------------------------------------------------- */
/* 16 — the tile's menu and dialog, save pending                             */
/* -------------------------------------------------------------------------- */
