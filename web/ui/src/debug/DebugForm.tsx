import { useId, useState, type ReactNode } from 'react';

import { btnPrimary, btnToggle } from '../components/buttons';
import { Icon } from '../components/Icon';
import { Pill } from '../components/Pill';
import { focusRing } from '../focus';
import { DEBUG_IMAGES, KEPT_TITLE, validImage, type DebugChoice } from './debug';

/** The kept badge: an ephemeral container stays in the pod, said on the form
 *  before Start and on the container's tile after it. */
export function KeptBadge(): ReactNode {
  return (
    <span data-kept="">
      <Pill neutral="muted" title={KEPT_TITLE}>
        stays in pod
      </Pill>
    </span>
  );
}

const heading = 'text-2xs tracking-heading text-muted uppercase';

/**
 * The debug form every entry shares: the image, the container whose processes
 * the debug container joins, and Start.
 *
 * Labels, values and a badge, and nothing to read: what each choice means is
 * on its tooltip. `refused` grays the whole form with the server's sentence on
 * it, so a person who may not add a container still sees what Debug is.
 */
export function DebugForm({
  targets,
  initialTarget,
  refused,
  onStart,
}: {
  /** The running app containers a debug container may join (`debugTargets`). */
  targets: readonly string[];
  /** The one chosen on opening: the container a Shell found no shell in, or
   *  the first running one. Absent, or not among `targets`: the first. */
  initialTarget?: string | undefined;
  /** Why Debug is refused here, when it is. */
  refused?: string | undefined;
  onStart: (choice: DebugChoice) => void;
}): ReactNode {
  const [preset, setPreset] = useState<string>(DEBUG_IMAGES[0].image);
  const [other, setOther] = useState(false);
  const [typed, setTyped] = useState('');
  const [target, setTarget] = useState<string>(
    initialTarget !== undefined && targets.includes(initialTarget) ? initialTarget : (targets[0] ?? ''),
  );
  const id = useId();
  const image = other ? typed.trim() : preset;
  const valid = validImage(image);

  return (
    <form
      data-debug-form=""
      className="text-sm"
      onSubmit={(event) => {
        event.preventDefault();
        if (refused !== undefined || !valid) return;
        onStart({ image, ...(target === '' ? {} : { target }) });
      }}
    >
      {/* A fieldset so `refused` disables every control in one attribute, and
          the sentence is on the box the pointer is over. */}
      <fieldset
        disabled={refused !== undefined}
        {...(refused === undefined ? {} : { title: refused })}
        className="m-0 flex min-w-0 flex-col gap-2 border-0 p-0"
      >
        <div className="flex flex-col gap-1">
          <span id={`${id}-image`} className={heading}>
            Image
          </span>
          <div role="group" aria-labelledby={`${id}-image`} className="flex flex-wrap gap-1">
            {DEBUG_IMAGES.map((one) => (
              <button
                key={one.label}
                type="button"
                aria-pressed={!other && preset === one.image}
                title={one.image}
                onClick={() => {
                  setOther(false);
                  setPreset(one.image);
                }}
                className={btnToggle(!other && preset === one.image)}
              >
                {one.label}
              </button>
            ))}
            <button type="button" aria-pressed={other} onClick={() => setOther(true)} className={btnToggle(other)}>
              Other
            </button>
          </div>
          {other ? (
            <input
              aria-label="Image reference"
              placeholder="registry/image:tag"
              value={typed}
              // Asked for by the press on Other, so the keys go where the
              // person is about to type.
              autoFocus
              spellCheck={false}
              autoCapitalize="off"
              autoComplete="off"
              aria-invalid={typed !== '' && !valid}
              onChange={(event) => setTyped(event.target.value)}
              className={`w-full rounded-md border bg-panel px-2 py-1 font-mono text-xs ${
                typed !== '' && !valid ? 'border-bad' : 'border-line'
              } ${focusRing}`}
            />
          ) : null}
        </div>

        <div className="flex flex-col gap-1">
          <span id={`${id}-target`} className={heading} title="The debug container sees this container's processes">
            Processes of
          </span>
          <div role="group" aria-labelledby={`${id}-target`} className="flex flex-wrap gap-1">
            {targets.map((name) => (
              <button
                key={name}
                type="button"
                aria-pressed={target === name}
                onClick={() => setTarget(name)}
                className={btnToggle(target === name)}
              >
                {name}
              </button>
            ))}
            <button
              type="button"
              aria-pressed={target === ''}
              title="Its own processes only"
              onClick={() => setTarget('')}
              className={btnToggle(target === '')}
            >
              none
            </button>
          </div>
        </div>

        <div className="flex items-center justify-between gap-2 pt-1">
          <KeptBadge />
          <button
            type="submit"
            disabled={!valid}
            title={valid ? `Add a container running ${image} and open its shell` : 'An image reference, without spaces'}
            className={btnPrimary}
          >
            <Icon name="debug" />
            Start
          </button>
        </div>
      </fieldset>
    </form>
  );
}
