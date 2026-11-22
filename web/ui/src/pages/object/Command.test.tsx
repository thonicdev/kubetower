import { QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { PodContainer } from '../../api';
import { createQueryClient } from '../../query/client';
import { scriptAt } from './Command';
import { Containers } from './Containers';
import { envFoldIsOpen, envFoldKey, resetEnvFolds } from './envFolds';
import { commandSummary } from './TileParts';

/**
 * The container tile, through the real panel: every argument as the spec gives
 * it, the image's entrypoint said when the spec sets nothing, a shell's script
 * readable whole, and the three sections that fold - Command and Resources
 * open, Probes closed - each saying what it holds when closed.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(navigator, 'clipboard');
  resetEnvFolds();
});

const SCRIPT = [
  'set -eu',
  'echo "rendering ${POD_NAME}"',
  "until nc -z db 5432; do echo 'waiting'; sleep 2; done",
  '',
  'touch /config/.ready',
].join('\n');

const started: PodContainer[] = [
  {
    name: 'render',
    image: 'busybox:1.36',
    init: true,
    command: ['/bin/sh', '-ec'],
    args: [SCRIPT, 'render-0'],
  },
  {
    name: 'api',
    image: 'shop/api:1.4.2',
    init: false,
    command: ['/app/api', '--listen=:8080'],
    args: ['--greeting', 'hello world', '--filter=app="web"', '', "it's"],
    requests: [
      { name: 'cpu', value: '100m' },
      { name: 'memory', value: '256Mi' },
    ],
    limits: [{ name: 'memory', value: '512Mi' }],
    probes: [
      { kind: 'liveness', state: 'declared', handler: 'httpGet', target: '/healthz:8080', periodSeconds: 10, failureThreshold: 3 },
      { kind: 'readiness', state: 'declared', handler: 'tcpSocket', target: ':8080', periodSeconds: 5 },
      { kind: 'startup', state: 'absent' },
    ],
    status: { state: 'running', ready: true, restarts: 2 },
  },
  { name: 'envoy', image: 'envoy:1.31', init: false },
  { name: 'tool', image: 'tool:2', init: false, args: ['serve', '--port', '9090'] },
];

function draw(containers: readonly PodContainer[] = started): HTMLElement {
  const view = render(
    <QueryClientProvider client={createQueryClient()}>
      <Containers cluster="prod" kind="pods" namespace="shop" name="api-1" containers={containers} />
    </QueryClientProvider>,
  );
  return view.container;
}

function tile(container: HTMLElement, name: string): HTMLElement {
  const found = container.querySelector<HTMLElement>(`[data-container="${name}"]`);
  if (found === null) throw new Error(`no tile for ${name}`);
  return found;
}

/** The boxes of one list, as drawn. */
function boxes(of: HTMLElement, list: 'command' | 'args'): string[] {
  return [...of.querySelectorAll(`[data-argv="${list}"] [data-arg]`)].map((box) => box.textContent);
}

/** A clipboard that records what it is given. */
function clipboard(): string[] {
  const written: string[] = [];
  vi.stubGlobal('isSecureContext', true);
  Object.defineProperty(navigator, 'clipboard', {
    value: {
      writeText: (value: string) => {
        written.push(value);
        return Promise.resolve();
      },
    },
    configurable: true,
  });
  return written;
}

function section(of: HTMLElement, name: string): HTMLElement {
  const found = of.querySelector<HTMLElement>(`[data-tile-section="${name}"]`);
  if (found === null) throw new Error(`no ${name} section`);
  return found;
}

function control(of: HTMLElement, name: string): HTMLButtonElement {
  const found = section(of, name).querySelector<HTMLButtonElement>('button[data-fold-control]');
  if (found === null) throw new Error(`no control on the ${name} section`);
  return found;
}

describe('a container tile shows what the container is started with', () => {
  it('draws the command and every argument in a box of its own, nothing joined or re-quoted', () => {
    const api = tile(draw(), 'api');
    expect(boxes(api, 'command')).toEqual(['/app/api', '--listen=:8080']);
    // The space inside an argument stays inside its box; the quotes are the
    // spec's own and none is added; an empty argument is said, not left blank.
    expect(boxes(api, 'args')).toEqual(['--greeting', 'hello world', '--filter=app="web"', 'empty', "it's"]);
    expect(api.querySelectorAll('[data-arg-empty]')).toHaveLength(1);
  });

  it('copies the whole argv as a JSON array, exact and pasteable after `command:`', async () => {
    const written = clipboard();
    const api = tile(draw(), 'api');
    const copy = api.querySelector<HTMLButtonElement>('[data-command] button');
    if (copy === null) throw new Error('no copy control on the command');
    expect(copy.getAttribute('aria-label')).toBe('Copy the command and args as a JSON array');
    fireEvent.click(copy);
    await waitFor(() => {
      expect(written).toEqual([
        JSON.stringify(['/app/api', '--listen=:8080', '--greeting', 'hello world', '--filter=app="web"', '', "it's"]),
      ]);
    });
  });

  it('copies only the args where the spec sets no command, and its name says so', async () => {
    // Pasted after `command:`, `serve` would be run as the executable instead
    // of the image's entrypoint, so the control must not claim the whole argv.
    const written = clipboard();
    const tool = tile(draw(), 'tool');
    const copy = tool.querySelector<HTMLButtonElement>('[data-command] button');
    if (copy === null) throw new Error('no copy control on the args');
    expect(copy.getAttribute('aria-label')).toBe('Copy the args as a JSON array');
    fireEvent.click(copy);
    await waitFor(() => {
      expect(written).toEqual([JSON.stringify(['serve', '--port', '9090'])]);
    });
  });

  it('says `image entrypoint` when the spec sets neither, and nothing more', () => {
    const envoy = tile(draw(), 'envoy');
    expect(envoy.querySelector('[data-entrypoint="image"]')?.textContent).toBe('image entrypoint');
    expect(envoy.querySelectorAll('[data-arg]')).toHaveLength(0);
  });

  it('says `image entrypoint` on the command row when only args is set', () => {
    const tool = tile(draw(), 'tool');
    expect(tool.querySelector('[data-argv="command"] [data-entrypoint="args"]')?.textContent).toBe('image entrypoint');
    expect(boxes(tool, 'args')).toEqual(['serve', '--port', '9090']);
  });

  it("folds a shell's script under the block, and opened it is the whole script, exactly", () => {
    const init = tile(draw(), 'render');
    expect(boxes(init, 'command')).toEqual(['/bin/sh', '-ec']);
    // The script's box says what it is; `$0` after it stays an argument.
    expect(boxes(init, 'args')).toEqual(['script · 5 lines', 'render-0']);

    const fold = init.querySelector<HTMLDetailsElement>('details[data-command-script="script"]');
    if (fold === null) throw new Error('the script is not folded under the tile');
    expect(fold.open).toBe(false);
    expect(fold.querySelector('summary')?.textContent).toContain('set -eu');
    // Every line, the blank one and the quotes included, as the spec holds it.
    expect(fold.querySelector('[data-command-script-body]')?.textContent).toBe(SCRIPT);
  });

  it('folds any argument that runs to several lines, even with no shell before it', () => {
    const job = tile(
      draw([{ name: 'job', image: 'job:1', init: false, command: ['/job'], args: ['--query', 'a\nb'] }]),
      'job',
    );
    expect(boxes(job, 'args')).toEqual(['--query', 'argument 3 · 2 lines']);
    expect(job.querySelector('[data-command-script="argument 3"] [data-command-script-body]')?.textContent).toBe(
      'a\nb',
    );
  });
});

describe('the tile folds into sections', () => {
  it('opens Command and Resources and closes Probes by default, and says so to assistive technology', () => {
    const api = tile(draw(), 'api');
    expect(control(api, 'command').getAttribute('aria-expanded')).toBe('true');
    expect(control(api, 'resources').getAttribute('aria-expanded')).toBe('true');
    expect(control(api, 'probes').getAttribute('aria-expanded')).toBe('false');
    // `aria-controls` names a box that is there, open or not.
    const body = control(api, 'probes').getAttribute('aria-controls') ?? '';
    expect(api.querySelector(`[id="${body}"]`)).not.toBeNull();
    // Closed, the rows are not rendered at all.
    expect(section(api, 'probes').querySelectorAll('[data-probe]')).toHaveLength(0);
    expect(section(api, 'command').querySelectorAll('[data-arg]')).not.toHaveLength(0);
  });

  it('says what a closed section holds, in data only', () => {
    const api = tile(draw(), 'api');
    // Declared, never a mark that reads as passing: whether a probe passes is the pill's.
    expect(section(api, 'probes').textContent).toBe('Probesdeclared: liveness, readiness · none: startup');
    fireEvent.click(control(api, 'command'));
    fireEvent.click(control(api, 'resources'));
    expect(section(api, 'command').textContent).toBe('Command/app/api · 6 args');
    expect(section(api, 'resources').textContent).toBe('Resourcescpu 100m / – · memory 256Mi / 512Mi');
  });

  it('opens and closes on a native button, and draws the probes as chips once opened', () => {
    // A native `<button type="button">` is what gives the keyboard Enter and
    // Space; jsdom does not turn a key into a click, so this asserts the element
    // and its focus and then presses it, and claims no more than that.
    const api = tile(draw(), 'api');
    const probes = control(api, 'probes');
    expect(probes.tagName).toBe('BUTTON');
    expect(probes.getAttribute('type')).toBe('button');
    probes.focus();
    expect(document.activeElement).toBe(probes);
    fireEvent.click(probes);
    expect(probes.getAttribute('aria-expanded')).toBe('true');
    const chips = [...section(api, 'probes').querySelectorAll('[data-probe]')].map((chip) => chip.textContent);
    expect(chips).toEqual(['livenesshttpGet /healthz:8080every 10s×3', 'readinesstcpSocket :8080every 5s', 'startupnone']);
    fireEvent.click(probes);
    expect(probes.getAttribute('aria-expanded')).toBe('false');
    expect(section(api, 'probes').querySelectorAll('[data-probe]')).toHaveLength(0);
  });

  it('keeps a section as it was left while the panel is drawn again', () => {
    const first = draw();
    fireEvent.click(control(tile(first, 'api'), 'command'));
    first.remove();
    const again = tile(draw(), 'api');
    expect(control(again, 'command').getAttribute('aria-expanded')).toBe('false');
    expect(control(again, 'probes').getAttribute('aria-expanded')).toBe('false');
  });

  it('keeps an opened script open while the panel is drawn again', async () => {
    const first = draw();
    const summary = tile(first, 'render').querySelector('details[data-command-script] > summary');
    if (summary === null) throw new Error('no script');
    fireEvent.click(summary);
    expect(tile(first, 'render').querySelector<HTMLDetailsElement>('details[data-command-script]')?.open).toBe(true);
    // `toggle` is queued after the change; the script is element 2 of the argv.
    const key = `${envFoldKey({ cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'api-1' }, { name: 'render', init: true }, 'script')}:2`;
    await waitFor(() => {
      expect(envFoldIsOpen(key)).toBe(true);
    });
    first.remove();
    const again = tile(draw(), 'render').querySelector<HTMLDetailsElement>('details[data-command-script]');
    expect(again?.open).toBe(true);
  });

  it('puts state, readiness and restarts in one pill', () => {
    const pill = tile(draw(), 'api').querySelector('[data-pill]');
    expect(pill?.textContent).toBe('Running · ready · 2 restarts');
    expect(pill?.getAttribute('data-pill')).toBe('ok');
  });
});

describe('commandSummary', () => {
  it.each([
    [{ command: ['/bin/sh', '-c'], args: ['x'] }, '/bin/sh · 2 args'],
    [{ command: ['/app'] }, '/app'],
    [{ command: ['/app', '-v'] }, '/app · 1 arg'],
    [{ args: ['serve'] }, 'image entrypoint · 1 arg'],
    [{}, 'image entrypoint'],
  ])('%j → %s', (argv, said) => {
    expect(commandSummary({ name: 'c', image: 'i', init: false, ...argv })).toBe(said);
  });
});

describe('scriptAt finds the script a shell is told to run', () => {
  it.each([
    [['sh', '-c', 'x'], 2],
    [['/bin/bash', '-euxc', 'x', 'name'], 2],
    [['bash', '-o', 'pipefail', '-c', 'x'], 4],
    [['sh', '-e', '-c', 'x'], 3],
    [['busybox', 'sh', '-c', 'x'], 3],
    [['/usr/bin/env', 'bash', '-c', 'x'], 3],
  ])('%j → %i', (argv, at) => {
    expect(scriptAt(argv)).toBe(at);
  });

  it.each([
    [['python', '-c', 'x']],
    [['sh', '/scripts/run.sh']],
    [['sh', '-c']],
    [['sh', '-e', 'run.sh', '-c', 'x']],
    [[]],
  ])('%j has none', (argv) => {
    expect(scriptAt(argv)).toBeUndefined();
  });
});
