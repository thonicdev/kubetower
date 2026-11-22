import { expect, test } from '@playwright/test';
import { px, show } from './layoutkit';

/* -------------------------------------------------------------------------- */
/* What each container is started with                                       */
/* -------------------------------------------------------------------------- */

/**
 * The `commands` scene: an init container whose `sh -c` script runs to twelve
 * lines, a container with seven arguments - one holding a space, one quotes,
 * one empty - and a container setting neither. At the drawer's width, every
 * argument is a box that wraps with the others and nothing in the panel or the
 * page scrolls sideways; the script is one line closed and, opened, all twelve
 * lines in a box at most `max-h-64` (256px) tall.
 */
for (const width of [1024, 1280]) {
  test(`a container's command, arguments and script stay in the panel at ${String(width)}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await show(page, 'commands');
    await page.waitForSelector('[data-command-script]');

    const read = (): Promise<{
      panel: { scroll: number; client: number };
      page: { scroll: number; client: number };
      args: string[];
      entrypoint: string;
      script: { open: boolean; summary: number; text: string; height: number; scroll: number; client: number };
    }> =>
      page.evaluate(() => {
        const panel = document.querySelector('[data-pod-panel]');
        const fold = document.querySelector<HTMLDetailsElement>('[data-container="render-config"] details[data-command-script]');
        const body = fold?.querySelector('[data-command-script-body]');
        const summary = fold?.querySelector('summary');
        if (panel === null || fold == null || !(body instanceof HTMLElement) || summary == null) {
          throw new Error('the pod panel or its script is not drawn');
        }
        return {
          panel: { scroll: panel.scrollWidth, client: panel.clientWidth },
          page: { scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth },
          args: [...document.querySelectorAll('[data-container="api"] [data-argv="args"] [data-arg]')].map((box) => box.textContent),
          entrypoint: document.querySelector('[data-container="envoy"] [data-entrypoint]')?.textContent ?? '',
          script: {
            open: fold.open,
            summary: summary.getBoundingClientRect().height,
            text: body.textContent,
            height: body.getBoundingClientRect().height,
            scroll: body.scrollWidth,
            client: body.clientWidth,
          },
        };
      });

    const closed = await read();
    expect(closed.args).toEqual(['--listen=:8080', '--log-format', 'json', '--greeting', 'hello world', '--filter=app="web"', 'empty']);
    expect(closed.entrypoint).toBe('image entrypoint');
    expect(closed.script.open, 'the script is folded until asked').toBe(false);
    expect(closed.script.summary, `closed, the script is ${px(closed.script.summary)} tall`).toBeLessThan(24);
    expect(closed.panel.scroll, `closed: the panel is ${px(closed.panel.scroll)} in ${px(closed.panel.client)}`).toBeLessThanOrEqual(closed.panel.client);
    expect(closed.page.scroll, `closed: the page is ${px(closed.page.scroll)} in ${px(closed.page.client)}`).toBeLessThanOrEqual(closed.page.client);

    await page.locator('[data-container="render-config"] [data-command-script] > summary').click();
    const open = await read();
    expect(open.script.open).toBe(true);
    expect(open.script.text.split('\n'), 'every line of the script').toHaveLength(12);
    expect(open.script.text).toContain('touch /config/.ready');
    expect(open.script.height, `opened, the script's box is ${px(open.script.height)} tall`).toBeLessThanOrEqual(256.5);
    expect(open.script.scroll, 'the script wraps rather than scrolling sideways').toBeLessThanOrEqual(open.script.client);
    expect(open.panel.scroll, `opened: the panel is ${px(open.panel.scroll)} in ${px(open.panel.client)}`).toBeLessThanOrEqual(open.panel.client);
    expect(open.page.scroll, `opened: the page is ${px(open.page.scroll)} in ${px(open.page.client)}`).toBeLessThanOrEqual(open.page.client);
  });
}
