import { useQueries } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';

import { useNav } from '../../api';
import { Pill } from '../../components/Pill';
import type { Extension, ExtensionOverviewTile, ExtensionShortcutTarget } from '../../extension';
import { focusRing } from '../../focus';
import { useShellIfPresent } from '../../shell/ShellContext';
import { enabledExtensions } from '../object/extensionTabs';
import { Meter } from '../object/Shortcuts';
import { Panel } from './Panel';

interface Offered extends ExtensionOverviewTile {
  key: string;
  label: string;
}

/** The extensions that offer an Overview tile, in the order they were handed
 *  to `createApp`. */
export function offeredTiles(extensions: readonly Extension[]): Offered[] {
  return extensions.flatMap((extension) =>
    extension.overview === undefined ? [] : [{ ...extension.overview, key: extension.key, label: extension.nav.label }],
  );
}

/**
 * The extensions' tiles on the Overview (`Extension.overview`), one each, in a
 * panel of their own. Asks the menu only when some extension offers one, asks
 * each tile only while its extension is on for the cluster, and draws nothing
 * until a tile has answered: no panel of placeholders, and no tile over a
 * refusal.
 */
export function ExtraTiles({ cluster }: { cluster: string }): ReactNode {
  const shell = useShellIfPresent();
  const offered = offeredTiles(shell?.extensions ?? []);
  const nav = useNav(cluster, offered.length > 0);
  const on = enabledExtensions(nav.data);
  const eligible = offered.filter((one) => on.has(one.key));
  const answers = useQueries({
    queries: eligible.map((one) => ({
      queryKey: ['extension-overview', one.key, cluster] as const,
      queryFn: ({ signal }: { signal: AbortSignal }) => one.resolve(cluster, signal),
      retry: false,
      staleTime: 60_000,
    })),
  });
  const drawn = eligible.flatMap((one, i) => {
    const target = answers[i]?.data;
    return target === undefined || target === null ? [] : [{ one, target }];
  });
  if (drawn.length === 0) return null;
  return (
    <Panel title="Extras" icon="extra">
      <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" data-extra-tiles="">
        {drawn.map(({ one, target }) => (
          <li key={one.key} data-extra-tile={one.key} data-tone={target.status?.tone ?? 'info'}>
            <Tile label={one.label} target={target} />
          </li>
        ))}
      </ul>
    </Panel>
  );
}

function Tile({ label, target }: { label: string; target: ExtensionShortcutTarget }): ReactNode {
  const body = (
    <>
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-2xs tracking-eyebrow text-muted uppercase" title={label}>
          {label}
        </span>
        {target.status === undefined ? null : (
          <Pill tone={target.status.tone} title={target.status.hint}>
            {target.status.text}
          </Pill>
        )}
      </span>
      <span className="truncate text-2xl leading-tight font-semibold tabular-nums" title={target.title}>
        {target.title}
      </span>
      {target.badges === undefined || target.badges.length === 0 ? null : (
        <span className="flex flex-wrap gap-1">
          {target.badges.map((badge) => (
            <Pill key={badge}>
              {badge}
            </Pill>
          ))}
        </span>
      )}
      {target.detail === undefined ? null : (
        <span className="truncate text-xs text-muted" title={target.detail}>
          {target.detail}
        </span>
      )}
      {target.meter === undefined ? null : <Meter meter={target.meter} />}
    </>
  );
  const look = 'flex h-full flex-col gap-1 rounded-xl border border-line bg-panel-2 px-3 py-2.5';
  return target.to.startsWith('/') ? (
    <Link to={target.to} aria-label={`${label}: ${target.title}`} className={`${look} hover:border-accent ${focusRing}`}>
      {body}
    </Link>
  ) : (
    <div className={look}>{body}</div>
  );
}
