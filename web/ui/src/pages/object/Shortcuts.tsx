import { useQueries } from '@tanstack/react-query';
import { Link } from 'react-router';
import { Suspense, useState, type ReactNode } from 'react';

import { useNav } from '../../api';
import { Dialog } from '../../components/Dialog';
import { KeyLayer } from '../../keyboard/layers';
import { Pending } from '../../state/StateNotice';
import { Pill } from '../../components/Pill';
import type { Extension, ExtensionMeter, ExtensionShortcut, ExtensionTabObject } from '../../extension';
import { focusRingInside } from '../../focus';
import { useShellIfPresent } from '../../shell/ShellContext';
import { enabledExtensions } from './extensionTabs';
import type { ObjectIdentity } from './address';

/** A shortcut as Details draws it: the extension's, with the extension's name. */
export interface ContributedShortcut extends ExtensionShortcut {
  extension: string;
  /** The extension's own name, the tile's eyebrow: "Redis viewer". */
  label: string;
  id: string;
}

export function contributedShortcuts(extensions: readonly Extension[], kind: string): ContributedShortcut[] {
  const wanted = kind.toLowerCase();
  const out: ContributedShortcut[] = [];
  for (const extension of extensions) {
    for (const one of extension.shortcuts ?? []) {
      if (one.kinds.some((k) => k === '*' || k.toLowerCase() === wanted)) {
        out.push({ ...one, extension: extension.key, label: extension.nav.label, id: `x.${extension.key}.${one.key}` });
      }
    }
  }
  return out;
}

/**
 * The extensions' lines for this object (`Extension.shortcuts`): one compact
 * line each - the extension's name, its graded word, what it found, one
 * detail - in a section of its own between what the object says about itself
 * and Related. Asks the menu only when some extension offers one for this
 * kind, and draws nothing until a shortcut has answered.
 *
 * A line opens, in this order: the extension's dialog when it declares one;
 * its tab on this object when it draws one here (`tabs`, `onTab`); otherwise
 * the page the shortcut names.
 */
export function Shortcuts({
  identity,
  marks,
  tabs = [],
  onTab,
}: {
  identity: ObjectIdentity;
  marks: ExtensionTabObject;
  /** The extensions' tabs drawn on this object, to open one in place. */
  tabs?: readonly { id: string; extension: string }[];
  onTab?: (id: string) => void;
}): ReactNode {
  const shell = useShellIfPresent();
  const offered = contributedShortcuts(shell?.extensions ?? [], identity.kind);
  const nav = useNav(identity.cluster, offered.length > 0);
  const on = enabledExtensions(nav.data);
  const eligible = offered.filter((one) => on.has(one.extension) && (one.when === undefined || one.when(marks)));
  const answers = useQueries({
    queries: eligible.map((one) => ({
      queryKey: ['extension-shortcut', one.id, identity.cluster, identity.kind, identity.namespace ?? '', identity.name] as const,
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        one.resolve({ cluster: identity.cluster, kind: identity.kind, namespace: identity.namespace, name: identity.name }, signal),
      retry: false,
      staleTime: 60_000,
    })),
  });
  const drawn = eligible.flatMap((one, i) => {
    const target = answers[i]?.data;
    return target === undefined || target === null ? [] : [{ one, target }];
  });
  const [opened, setOpened] = useState<string | undefined>(undefined);
  if (drawn.length === 0) return null;
  const dialog = drawn.find(({ one }) => one.id === opened);
  const Window = dialog?.one.dialog;
  return (
    <section data-shortcuts="" aria-label="Extras" className="flex flex-col gap-1">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Extras</h3>
      <ul className="flex flex-col divide-y divide-line rounded-md border border-line">
        {drawn.map(({ one, target }) => {
          const tab = one.dialog === undefined && onTab !== undefined ? tabs.find((t) => t.extension === one.extension) : undefined;
          const body = (
            <>
              <span className="min-w-28 shrink-0 whitespace-nowrap text-2xs tracking-eyebrow text-muted uppercase" title={one.label}>
                {one.label}
              </span>
              {target.status === undefined ? null : (
                <Pill tone={target.status.tone} title={target.status.hint}>
                  {target.status.text}
                </Pill>
              )}
              <span className="max-w-full min-w-0 text-fg" title={target.title}>
                {target.title}
              </span>
              {target.detail === undefined ? null : <span className="min-w-0 grow basis-32 text-muted" title={target.detail}>· {target.detail}</span>}
              {target.meter === undefined ? null : (
                <span className="ml-auto min-w-0">
                  <Meter meter={target.meter} />
                </span>
              )}
              <span aria-hidden="true" className={`${target.meter === undefined ? 'ml-auto' : ''} shrink-0 text-muted`}>
                ›
              </span>
            </>
          );
          const look = `flex min-h-9 w-full flex-wrap items-center gap-x-2 gap-y-0.5 px-2 py-1.5 text-left text-xs hover:bg-panel-2 ${focusRingInside}`;
          const label = `Open ${target.title} in ${one.label}`;
          return (
            <li key={one.id} data-shortcut={one.id} data-tone={target.status?.tone ?? 'info'} title={(target.badges ?? []).join(' · ') || undefined}>
              {one.dialog !== undefined ? (
                <button type="button" className={look} aria-label={label} onClick={() => setOpened(one.id)}>
                  {body}
                </button>
              ) : tab !== undefined ? (
                <button type="button" className={look} aria-label={label} data-opens-tab={tab.id} onClick={() => onTab?.(tab.id)}>
                  {body}
                </button>
              ) : (
                <Link to={target.to} className={look} aria-label={label}>
                  {body}
                </Link>
              )}
            </li>
          );
        })}
      </ul>
      {dialog === undefined || Window === undefined ? null : (
        <Dialog title={`${dialog.one.label} · ${identity.name}`} size="viewport" layer={KeyLayer.Dialog} onClose={() => setOpened(undefined)}>
          <div data-shortcut-dialog={dialog.one.id}>
            <Suspense fallback={<Pending what={dialog.one.label} />}>
              <Window cluster={identity.cluster} kind={identity.kind} namespace={identity.namespace} name={identity.name} />
            </Suspense>
          </div>
        </Dialog>
      )}
    </section>
  );
}

const METER_FILL: Readonly<Record<string, string>> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
  info: 'bg-accent',
};

const clamp = (v: number): number => (Number.isFinite(v) ? Math.min(100, Math.max(0, v)) : 0);

/**
 * A shortcut's meter: the extension's parts end to end on a track, its
 * threshold as a line, its reading beside it. Drawn as given (the contract's
 * `ExtensionMeter`), never rescaled.
 */
export function Meter({ meter }: { meter: ExtensionMeter }): ReactNode {
  return (
    <div data-shortcut-meter="" className="flex min-w-0 items-center gap-2" title={meter.label}>
      <div role="img" aria-label={meter.text === undefined ? meter.label : `${meter.label}: ${meter.text}`} className="relative flex h-2 w-10 shrink-0 overflow-hidden rounded-full bg-white/10">
        {meter.segments.map((part, i) =>
          clamp(part.width) === 0 ? null : (
            <span
              key={`${String(i)}:${part.label}`}
              data-meter-part={part.tone ?? 'neutral'}
              className={`h-full shrink-0 ${METER_FILL[part.tone ?? ''] ?? 'bg-muted'}`}
              style={{ width: `${String(clamp(part.width))}%` }}
              title={part.label}
            />
          ),
        )}
        {meter.threshold === undefined ? null : (
          <span
            data-meter-threshold=""
            className="absolute inset-y-0 w-px bg-fg"
            style={{ left: `${String(clamp(meter.threshold.at))}%` }}
            title={meter.threshold.label}
          />
        )}
      </div>
      {meter.text === undefined ? null : (
        <span className="min-w-0 truncate text-2xs text-muted tabular-nums" title={meter.text}>
          {meter.text}
        </span>
      )}
    </div>
  );
}
