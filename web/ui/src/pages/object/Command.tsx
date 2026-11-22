import { useRef, type ReactNode } from 'react';

import type { PodContainer } from '../../api';
import { CopyButton } from '../../components/CopyButton';
import { focusRing } from '../../focus';
import { useEnvFold } from './envFolds';

/**
 * What a container is started with: its `command` and `args` exactly as the
 * spec gives them, and the script a shell is handed.
 *
 * ## Every argument is its own box, and nothing is re-quoted
 *
 * The server sends the two lists element by element. Joined with spaces they
 * would lose the one thing an argv is: where each argument ends - `--greeting`
 * `hello world` and `--greeting hello` `world` would read alike. Quoting them
 * for a shell would put characters on the screen that are not in the spec, in a
 * dialect the container may not even run. So each element is drawn as it is, in
 * a box of its own, and an empty one says `empty`, because an empty argument is
 * an argument (`getopt` sees it, a positional count includes it) and a blank box
 * reads as a drawing fault.
 *
 * ## The copy says which list it copies
 *
 * One control, writing a JSON array, which is exact and is also what a manifest
 * accepts after the key it names. With a `command`, the array is the whole argv,
 * `command` then `args`, valid after `command:`. With `args` alone it is the
 * args, valid after `args:` only: pasted after `command:` its first argument
 * would be run as the executable instead of the image's entrypoint, so the
 * control's name says `args`.
 *
 * ## Nothing set is a statement
 *
 * Both lists absent means the image's own ENTRYPOINT and CMD run, and only the
 * registry knows what they are. The block says `image entrypoint`, because a
 * missing block would read as a panel that did not look; with `args` alone, the
 * `cmd` row says it, since that is the half the image supplies.
 *
 * ## A script is read in full, under the block
 *
 * `sh -c "<script>"` hands the shell a whole program as one argument, often
 * dozens of lines: drawn inline it would be the tile. So the argument's box says
 * what it is and how long, and the script is folded under the block the way a
 * termination message is (`FoldedMessage`): one line closed, the whole text
 * opened, monospace, in a box that scrolls on its own, with a copy control. Any
 * other argument that runs to several lines folds the same way. The text is
 * never cleaned: unlike a log tail it is the spec, and what is drawn is what the
 * shell runs. Whether a fold is open is remembered with the tile's sections
 * (`envFolds.ts`), so moving between the object's tabs does not close it.
 */
export function Command({
  container,
  memory,
}: {
  container: PodContainer;
  /** Where this container's folds are remembered (`envFolds.ts`); each fold
   *  adds its position to it. */
  memory: string;
}): ReactNode {
  const command = container.command ?? [];
  const args = container.args ?? [];

  if (command.length === 0 && args.length === 0) {
    return (
      <div data-command className={BLOCK}>
        <span data-entrypoint="image" className="text-muted italic">
          image entrypoint
        </span>
      </div>
    );
  }

  // With no `command` the image's entrypoint is first and unknown, and `args`
  // is usually what it execs (`tini --`, `docker-entrypoint.sh`), so the shell
  // is looked for in `args` alone.
  const argv = command.length === 0 ? args : [...command, ...args];
  const script = scriptAt(argv);
  const offset = command.length;
  // Decided before anything is drawn, so the boxes in the lists and the folds
  // under them are the same set.
  const folds = new Map<number, Fold>();
  argv.forEach((value, at) => {
    if (at !== script && !value.includes('\n')) return;
    folds.set(at, { value, what: at === script ? 'script' : `argument ${String(at + 1)}`, key: at });
  });

  return (
    <div className="flex flex-col gap-1">
      <div data-command className={`${BLOCK} flex items-start gap-2`}>
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div data-argv="command" className="flex gap-2">
            <span className={LABEL}>cmd</span>
            <span className="flex min-w-0 flex-1 flex-wrap gap-1">
              {command.length === 0 ? (
                <span data-entrypoint="args" className="text-muted italic">
                  image entrypoint
                </span>
              ) : (
                // The position is the identity: two equal arguments are two
                // arguments, and the list never reorders.
                command.map((value, at) => <Argument key={at} value={value} folded={folds.get(at)} />)
              )}
            </span>
          </div>
          {args.length === 0 ? null : (
            <div data-argv="args" className="flex gap-2">
              <span className={LABEL}>args</span>
              <span className="flex min-w-0 flex-1 flex-wrap gap-1">
                {args.map((value, at) => (
                  <Argument key={at} value={value} folded={folds.get(at + offset)} />
                ))}
              </span>
            </div>
          )}
        </div>
        <CopyButton
          value={JSON.stringify(argv)}
          label={command.length === 0 ? 'Copy the args as a JSON array' : 'Copy the command and args as a JSON array'}
        />
      </div>
      {[...folds.values()].map((fold) => (
        <Script key={fold.key} fold={fold} memory={`${memory}:${String(fold.key)}`} />
      ))}
    </div>
  );
}

const BLOCK = 'min-w-0 rounded border border-line bg-panel-2 p-1.5 font-mono text-xs';
const LABEL = 'w-8 shrink-0 pt-px font-sans text-2xs text-muted uppercase';

interface Fold {
  value: string;
  /** `script`, or `argument 4` for a multi-line argument no shell is run on. */
  what: string;
  /** Its position in the argv, which is its identity. */
  key: number;
}

function Argument({ value, folded }: { value: string; folded: Fold | undefined }): ReactNode {
  const box = 'min-w-0 rounded-sm border border-line bg-panel px-1';
  if (folded !== undefined) {
    return (
      <span data-arg data-arg-folded={folded.what} className={`${box} text-muted italic`}>
        {folded.what} · {lines(folded.value)}
      </span>
    );
  }
  if (value === '') {
    return (
      <span data-arg data-arg-empty className={`${box} text-muted italic`}>
        empty
      </span>
    );
  }
  return (
    <span data-arg className={`${box} whitespace-pre-wrap wrap-anywhere`}>
      {value}
    </span>
  );
}

function lines(text: string): string {
  const count = text.replace(/\n$/, '').split('\n').length;
  return count === 1 ? '1 line' : `${String(count)} lines`;
}

/**
 * One script, folded: closed, its first line with something on it; opened, the
 * whole text exactly as the spec holds it. `FoldedMessage`'s shape, without its
 * cleaning, because an escape in a script is part of the program. Its open
 * state is remembered under `memory`, as the sections' is.
 */
function Script({ fold, memory }: { fold: Fold; memory: string }): ReactNode {
  const body = useRef<HTMLPreElement>(null);
  const [open, remember] = useEnvFold(memory);
  const first = fold.value.split('\n').find((line) => line.trim() !== '') ?? '';
  return (
    <details
      data-command-script={fold.what}
      open={open}
      onToggle={(event) => {
        // Fired after the element changed; storing the same value again is a no-op.
        if (event.currentTarget.open !== open) remember(event.currentTarget.open);
      }}
      className="group w-full min-w-0 text-xs"
    >
      <summary
        className={`flex min-w-0 cursor-pointer items-baseline gap-2 rounded-sm [&::-webkit-details-marker]:hidden ${focusRing}`}
      >
        <span className="shrink-0 rounded-sm border border-line px-1 text-2xs text-muted uppercase hover:text-fg">
          {fold.what} · {lines(fold.value)}
        </span>
        <span title={first} className="min-w-0 truncate font-mono text-muted group-open:hidden">
          {first}
        </span>
      </summary>
      <div className="mt-1 flex items-start gap-2">
        {/* Focusable, so the keyboard can scroll it where the engine does not
            make a scroller focusable on its own. */}
        <pre
          ref={body}
          data-command-script-body
          tabIndex={0}
          className={`max-h-64 min-w-0 flex-1 overflow-y-auto rounded border border-line bg-panel-2 p-2 font-mono text-2xs whitespace-pre-wrap wrap-anywhere text-fg ${focusRing}`}
        >
          {fold.value}
        </pre>
        <CopyButton value={fold.value} label={`Copy this ${fold.what}`} selectRef={body} />
      </div>
    </details>
  );
}

/** The shells whose `-c` takes a script. */
const SHELLS = new Set(['sh', 'bash', 'ash', 'dash', 'zsh', 'ksh', 'mksh']);

/** The last path segment: `/bin/sh` is `sh`. */
function base(path: string | undefined): string {
  return (path ?? '').split('/').at(-1) ?? '';
}

/**
 * Which element of `argv` is the script a shell is told to run, or `undefined`.
 *
 * `sh -c <script>`, with the flags a script is usually started with folded into
 * the same word (`-ec`, `-euxc`) or written apart (`-e -c`, `-o pipefail -c`),
 * and behind `busybox` or `env`. The element after the cluster holding `c` is
 * the script; what follows it is `$0` and the positional parameters, drawn as
 * the arguments they are. The first word that is not an option ends the search:
 * `sh run.sh -c x` runs a file, and `-c` there is that file's argument.
 * Exported for the tests.
 */
export function scriptAt(argv: readonly string[]): number | undefined {
  let at = 0;
  if (['busybox', 'env'].includes(base(argv[0])) && SHELLS.has(base(argv[1]))) at = 1;
  if (!SHELLS.has(base(argv[at]))) return undefined;
  for (at += 1; at < argv.length; at += 1) {
    const word = argv[at] ?? '';
    if (word === '-o' || word === '+o') {
      at += 1; // `-o pipefail`: the option's name is not a flag cluster.
      continue;
    }
    if (!/^[-+][A-Za-z]+$/.test(word)) return undefined;
    if (word.startsWith('-') && word.includes('c')) return at + 1 < argv.length ? at + 1 : undefined;
  }
  return undefined;
}
