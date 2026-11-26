import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { btnSecondary, externalHref, forwardLink, forwardsQuery, openForward, openForwardWindow, queryKeys, useCapabilities, type Forward } from '@kubetower/ui';

import { configuredHref, tunnelSuffix, uisQuery, type FlowUI } from './data';

const KINDS = [
  { kind: 'hubble', label: 'Hubble UI' },
  { kind: 'whisker', label: 'Whisker' },
] as const;

/**
 * The buttons that open Hubble UI (Cilium) and Whisker (Calico), for the UIs
 * this cluster has, and nothing where it has neither.
 *
 * An address set in the extra's settings is a link. Without one the button
 * opens a tunnel with the console's own port-forward and then becomes a link
 * to it, opened in a new tab or the default browser. The tunnel exists only where
 * the console can hand the browser a loopback address; where it cannot (the
 * server profile) there is no button for a UI that has no address configured.
 * `namespace` is the one the page or the pod is about, if any.
 */
export function FlowUIButtons({ cluster, namespace }: { cluster: string; namespace: string }): ReactNode {
  const uis = useQuery(uisQuery(cluster));
  const capabilities = useCapabilities();
  if (uis.data === undefined) return null;
  const canTunnel = capabilities.data?.portForward === true;
  const inWindow = canTunnel && capabilities.data?.toolWindow === true;
  const shown = KINDS.flatMap(({ kind, label }) => {
    const ui = uis.data[kind];
    if (ui === undefined) return [];
    if (ui.url === undefined && !(canTunnel && ui.tunnel !== undefined)) return [];
    return [{ kind, label, ui }];
  });
  if (shown.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-2" data-flow-uis="">
      {shown.map(({ kind, label, ui }) => (
        // Keyed by the cluster too: a press on one cluster never leaves its
        // state on the next one's button.
        ui.url === undefined && ui.tunnel !== undefined && inWindow ? (
          <FlowUIWindowButton key={`${cluster}/${kind}`} kind={kind} label={label} tunnel={ui.tunnel} cluster={cluster} namespace={namespace} />
        ) : (
          <FlowUIButton key={`${cluster}/${kind}`} kind={kind} label={label} ui={ui} cluster={cluster} namespace={namespace} />
        )
      ))}
    </span>
  );
}

/**
 * Focuses the link of the forward a press opened (`wanted`), once the list
 * draws it. The request ends there, or when the list, read again, does not
 * hold that forward (a page address naming the cluster another way never
 * matches it): a forward that appears later does not take the focus.
 */
function useFocusOnce(
  wanted: string | null,
  drawn: string | undefined,
  list: readonly Forward[] | undefined,
  reading: boolean,
  link: { current: HTMLAnchorElement | null },
  done: () => void,
): void {
  useEffect(() => {
    if (wanted === null) return;
    if (drawn === wanted && link.current !== null) {
      link.current.focus();
      done();
    } else if (!reading && list !== undefined) {
      done();
    }
  }, [wanted, drawn, list, reading, link, done]);
}

function FlowUIButton({ kind, label, ui, cluster, namespace }: { kind: 'hubble' | 'whisker'; label: string; ui: FlowUI; cluster: string; namespace: string }): ReactNode {
  const client = useQueryClient();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  /** The forward a press opened: its link, once drawn, takes the focus. */
  const [focusFor, setFocusFor] = useState<string | null>(null);
  const link = useRef<HTMLAnchorElement>(null);
  const tunnel = ui.url === undefined ? ui.tunnel : undefined;
  // The forwards this process holds, as the Ports panel reads them: the link
  // is drawn from the live list, so a tunnel the server closed (its pod went
  // away) turns back into a button that opens it again.
  const live = useQuery(forwardsQuery(tunnel !== undefined));
  const forward =
    tunnel === undefined
      ? undefined
      : live.data?.find((f) => f.cluster === cluster && f.namespace === tunnel.namespace && f.service === tunnel.service && f.remotePort === tunnel.port);
  useFocusOnce(focusFor, forward?.id, live.data, live.isFetching, link, () => setFocusFor(null));
  const href = ui.url === undefined ? undefined : externalHref(configuredHref(kind, ui.url, namespace));
  if (ui.url !== undefined) {
    if (href === undefined) return null;
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" data-flow-ui={kind} className={btnSecondary}>
        {label}
      </a>
    );
  }
  if (tunnel === undefined) return null;

  // An open forward is an ordinary link to the UI, as the Ports panel draws
  // one: a browser opens it in a tab, the desktop's own window hands it to the
  // default browser. No blank window is asked for ahead of the forward - the
  // desktop window refuses every window a page opens, and hands on only http,
  // https and mailto addresses (`internal/launch/nativewindownav.go`).
  if (forward !== undefined) {
    const to = externalHref(forwardLink(forward, forward.scheme ?? 'http') + tunnelSuffix(kind, namespace));
    if (to !== undefined) {
      return (
        <a
          ref={link}
          href={to}
          target="_blank"
          rel="noopener noreferrer"
          data-flow-ui={kind}
          data-flow-ui-open=""
          title={to}
          // Asked again on the way out, so a tunnel closed meanwhile is a
          // button again when the person comes back.
          onClick={() => void client.invalidateQueries({ queryKey: queryKeys.forwards })}
          className={btnSecondary}
        >
          {label} ↗
        </a>
      );
    }
  }

  const open = (): void => {
    setPending(true);
    setFailure(null);
    openForward({
      cluster,
      namespace: tunnel.namespace,
      service: tunnel.service,
      remotePort: tunnel.port,
      ...(tunnel.portName === undefined || tunnel.portName === '' ? {} : { portName: tunnel.portName }),
      ...(tunnel.appProtocol === undefined || tunnel.appProtocol === '' ? {} : { appProtocol: tunnel.appProtocol }),
    })
      .then((made) => {
        if (externalHref(forwardLink(made, made.scheme ?? 'http') + tunnelSuffix(kind, namespace)) === undefined) {
          setFailure('The tunnel opened at an address that cannot be linked');
          return;
        }
        // Into the shared list at once, then read again, as the Ports panel does.
        client.setQueryData<Forward[]>(queryKeys.forwards, (before) => [...(before ?? []).filter((one) => one.id !== made.id), made]);
        void client.invalidateQueries({ queryKey: queryKeys.forwards });
        setFocusFor(made.id);
      })
      .catch((error: unknown) => {
        setFailure(error instanceof Error ? error.message : String(error));
      })
      .finally(() => setPending(false));
  };

  return (
    <>
      <button type="button" data-flow-ui={kind} disabled={pending} onClick={open} className={btnSecondary}>
        {pending ? `${label}…` : label}
      </button>
      {failure === null ? null : (
        <span role="alert" className="text-xs text-bad">
          {failure}
        </span>
      )}
    </>
  );
}

type Tunnel = NonNullable<FlowUI['tunnel']>;

/**
 * The desktop's own native window (`capabilities.toolWindow`): the button opens
 * the UI in a KubeTower window of its own, through the tunnel, opening the
 * tunnel first when it is not open; ↗ beside it opens the tunnel in the default
 * browser instead. A tunnel opened for the window closes with it.
 */
function FlowUIWindowButton({ kind, label, tunnel, cluster, namespace }: { kind: 'hubble' | 'whisker'; label: string; tunnel: Tunnel; cluster: string; namespace: string }): ReactNode {
  const client = useQueryClient();
  const [pending, setPending] = useState<'window' | 'browser' | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [focusFor, setFocusFor] = useState<string | null>(null);
  const link = useRef<HTMLAnchorElement>(null);
  const live = useQuery(forwardsQuery(true));
  const forward = live.data?.find((f) => f.cluster === cluster && f.namespace === tunnel.namespace && f.service === tunnel.service && f.remotePort === tunnel.port);
  useFocusOnce(focusFor, forward?.id, live.data, live.isFetching, link, () => setFocusFor(null));
  const suffix = tunnelSuffix(kind, namespace);

  /** The live forward, or one opened now (`opened`). */
  const ensure = async (): Promise<{ made: Forward; opened: boolean }> => {
    if (forward !== undefined) return { made: forward, opened: false };
    const made = await openForward({
      cluster,
      namespace: tunnel.namespace,
      service: tunnel.service,
      remotePort: tunnel.port,
      ...(tunnel.portName === undefined || tunnel.portName === '' ? {} : { portName: tunnel.portName }),
      ...(tunnel.appProtocol === undefined || tunnel.appProtocol === '' ? {} : { appProtocol: tunnel.appProtocol }),
    });
    client.setQueryData<Forward[]>(queryKeys.forwards, (before) => [...(before ?? []).filter((one) => one.id !== made.id), made]);
    return { made, opened: true };
  };
  const run = (which: 'window' | 'browser', then: (made: Forward, opened: boolean) => Promise<void>): void => {
    setPending(which);
    setFailure(null);
    ensure()
      .then(({ made, opened }) => then(made, opened))
      .catch((error: unknown) => setFailure(error instanceof Error ? error.message : String(error)))
      .finally(() => {
        setPending(null);
        void client.invalidateQueries({ queryKey: queryKeys.forwards });
      });
  };
  const toWindow = (): void => run('window', (made, opened) => openForwardWindow(made.id, { tool: kind, path: suffix, closeWithWindow: opened }));
  const toBrowser = (): void =>
    run('browser', (made) => {
      setFocusFor(made.id);
      return Promise.resolve();
    });
  const to = forward === undefined ? undefined : externalHref(forwardLink(forward, forward.scheme ?? 'http') + suffix);

  return (
    <>
      <span className="inline-flex gap-1">
        <button type="button" data-flow-ui={kind} data-flow-ui-window="" disabled={pending !== null} onClick={toWindow} className={btnSecondary}>
          {pending === 'window' ? `${label}…` : label}
        </button>
        {to !== undefined ? (
          <a
            ref={link}
            href={to}
            target="_blank"
            rel="noopener noreferrer"
            data-flow-ui-browser={kind}
            title={to}
            aria-label={`${label} in browser`}
            onClick={() => void client.invalidateQueries({ queryKey: queryKeys.forwards })}
            className={btnSecondary}
          >
            ↗
          </a>
        ) : (
          <button type="button" data-flow-ui-browser={kind} disabled={pending !== null} onClick={toBrowser} title="Open in browser" aria-label={`${label} in browser`} className={btnSecondary}>
            {pending === 'browser' ? '…' : '↗'}
          </button>
        )}
      </span>
      {failure === null ? null : (
        <span role="alert" className="text-xs text-bad">
          {failure}
        </span>
      )}
    </>
  );
}
