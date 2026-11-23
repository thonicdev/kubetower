import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { btnSecondary, externalHref, forwardLink, forwardsQuery, openForward, queryKeys, useCapabilities, type Forward } from '@kubetower/ui';

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
        <FlowUIButton key={`${cluster}/${kind}`} kind={kind} label={label} ui={ui} cluster={cluster} namespace={namespace} />
      ))}
    </span>
  );
}

function FlowUIButton({ kind, label, ui, cluster, namespace }: { kind: 'hubble' | 'whisker'; label: string; ui: FlowUI; cluster: string; namespace: string }): ReactNode {
  const client = useQueryClient();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  /** Set by a press that opened the forward: the link that replaces the button takes the focus. */
  const [focusLink, setFocusLink] = useState(false);
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
  useEffect(() => {
    if (focusLink && link.current !== null) {
      link.current.focus();
      setFocusLink(false);
    }
  }, [focusLink, forward?.id]);
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
        setFocusLink(true);
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
