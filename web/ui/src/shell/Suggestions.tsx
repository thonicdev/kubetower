import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router';

import { getNav, listExtensionSuggestions, setExtensionEnabled } from '../api/endpoints';
import { queryKeys, useCapabilities } from '../api/queries';
import type { ExtensionSuggestion } from '../api/types';
import { btnPrimary, btnSecondary } from '../components/buttons';
import { dismissSuggestion, dismissedSuggestions, isDismissed } from './suggestionMemory';

/**
 * The line above a cluster's page that offers an extension the cluster gives a
 * reason for: every extra is off by default, and
 * one whose component the cluster runs is suggested there rather than left for
 * somebody to find in Settings - any extension that can tell
 * (`extension.Suggester`).
 *
 * **Only where the switch can be pressed**: wherever it is registered - every
 * profile with one replica - and only what this person could turn on, which the
 * server decides (the chart's values, the `use` verb in a cluster). Several
 * replicas register neither the suggestion nor the switch, so it is not asked
 * there. What it says is the extension's own evidence, from what the person may
 * list; a suggestion that could not be read draws nothing, never an error -
 * there is nothing to act on in it.
 *
 * **Turn on** flips that extension's switch on this cluster and opens its page;
 * **Not now** hides it for this cluster in this browser. Settings → Extra stays
 * where the switch lives, either way.
 */
/** How many suggestions at once fold into one line. */
const GROUP_FROM = 3;

export function Suggestions(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const capabilities = useCapabilities().data;
  // Strictly: a profile that was never read is not one that offers the switch.
  // Several replicas register no switch (`Capabilities.replicated`); a server
  // profile with no state volume answers the question 404, which draws nothing.
  const writable = capabilities !== undefined && !capabilities.replicated;
  const on = cluster !== undefined && writable;
  const suggestions = useQuery({
    queryKey: queryKeys.extensionSuggestions(cluster ?? ''),
    queryFn: ({ signal }) => listExtensionSuggestions(cluster ?? '', signal),
    enabled: on,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const [dismissed, setDismissed] = useState(dismissedSuggestions);
  if (!on || suggestions.data?.state !== 'ok') return null;
  const shown = suggestions.data.items.filter((item) => item.absent !== true && !isDismissed(dismissed, cluster, item.key));
  if (shown.length === 0) return null;
  const dismiss = (key: string): void => {
    setDismissed(dismissSuggestion(dismissed, cluster, key));
  };
  if (shown.length > GROUP_FROM - 1) {
    // Past two, one line that points at Settings > Extra, where each extra
    // carries its own sentence. "Not now" answers every extra shown now, one
    // by one, so an extra suggested later is not covered by it and comes back
    // on its own line.
    const dismissAll = (): void => {
      let next = dismissed;
      for (const item of shown) next = dismissSuggestion(next, cluster, item.key);
      setDismissed(next);
    };
    return (
      <section className="shrink-0" aria-label="Suggestions" data-suggestions="">
        <div
          className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-l-2 border-line border-l-accent bg-panel px-3 py-1.5 text-xs"
          data-suggestion-group=""
        >
          <p className="min-w-0 flex-1 basis-60 truncate" title={shown.map((item) => item.label).join(', ')}>
            <span className="font-medium text-fg">{`${String(shown.length)} extras`}</span>{' '}
            <span className="text-muted">{shown.map((item) => item.label).join(' · ')}</span>
          </p>
          <Link to={`/settings/extra?cluster=${encodeURIComponent(cluster)}`} className={btnPrimary} data-suggestion-see="">
            See
          </Link>
          <button type="button" className={btnSecondary} onClick={dismissAll} data-suggestion-dismiss="">
            Not now
          </button>
        </div>
      </section>
    );
  }
  return (
    <section className="shrink-0" aria-label="Suggestions" data-suggestions="">
      {shown.map((item) => (
        <Suggestion key={item.key} cluster={cluster} item={item} onDismiss={() => dismiss(item.key)} />
      ))}
    </section>
  );
}

function Suggestion({
  cluster,
  item,
  onDismiss,
}: {
  cluster: string;
  item: ExtensionSuggestion;
  onDismiss: () => void;
}): ReactNode {
  const cache = useQueryClient();
  const navigate = useNavigate();
  const turnOn = useMutation({
    mutationFn: async () => {
      await setExtensionEnabled(cluster, item.key, true);
      // Its page's address is the menu's Extra entry, read once it is on: the
      // console builds that link from the key (`internal/web/extranav.go`),
      // so the entry is the one whose address ends in it - a label two extras
      // could share is not a key. No entry, no navigation: the switch is on
      // and the menu shows it.
      const menu = await getNav(cluster);
      if (menu.state !== 'ok') return undefined;
      const extra = menu.data.groups.find((group) => group.key === 'extra');
      return extra?.items.find((entry) => entry.href?.endsWith(`/x/${item.key}`) === true)?.href;
    },
    onSuccess: (href) => {
      if (href !== undefined) void navigate(href);
    },
    onSettled: () =>
      Promise.all([
        cache.invalidateQueries({ queryKey: queryKeys.extensions(cluster) }),
        cache.invalidateQueries({ queryKey: queryKeys.nav(cluster) }),
      ]),
  });
  return (
    <div
      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-l-2 border-line border-l-accent bg-panel px-3 py-1.5 text-xs"
      data-suggestion={item.key}
    >
      {/* basis-60: on a narrow page the buttons go under the sentence
          rather than squeezing it to a word a line. */}
      <p className="min-w-0 flex-1 basis-60">
        <span className="font-medium text-fg">{item.label}</span>{' '}
        <span className="text-muted">{item.evidence}</span>
      </p>
      {turnOn.isError ? (
        <span className="text-bad" title={turnOn.error.message} data-suggestion-failed="">
          Not turned on
        </span>
      ) : null}
      <button
        type="button"
        className={btnPrimary}
        disabled={turnOn.isPending}
        onClick={() => turnOn.mutate()}
        data-suggestion-on=""
      >
        {turnOn.isPending ? 'Turning on…' : 'Turn on'}
      </button>
      <button
        type="button"
        className={btnSecondary}
        onClick={onDismiss}
        data-suggestion-dismiss=""
      >
        Not now
      </button>
    </div>
  );
}
