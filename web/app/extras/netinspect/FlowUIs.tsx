import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { btnSecondary, externalHref, forwardLink, openForward, useCapabilities, type Scheme } from '@kubetower/ui';

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
        <FlowUIButton key={kind} kind={kind} label={label} ui={ui} cluster={cluster} namespace={namespace} />
      ))}
    </span>
  );
}

function FlowUIButton({ kind, label, ui, cluster, namespace }: { kind: 'hubble' | 'whisker'; label: string; ui: FlowUI; cluster: string; namespace: string }): ReactNode {
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  /** The open forward's address, once there is one: the button becomes a link to it. */
  const [opened, setOpened] = useState<string | null>(null);
  const href = ui.url === undefined ? undefined : externalHref(configuredHref(kind, ui.url, namespace));
  if (ui.url !== undefined) {
    if (href === undefined) return null;
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" data-flow-ui={kind} className={btnSecondary}>
        {label}
      </a>
    );
  }
  const tunnel = ui.tunnel;
  if (tunnel === undefined) return null;

  // The forward is opened first and the UI is then an ordinary link to it, as
  // the Ports panel draws one: a browser opens it in a tab, the desktop's own
  // window hands it to the default browser. No blank window is asked for
  // ahead of the forward - the desktop window refuses every window a page
  // opens, and hands on only http and https addresses
  // (`internal/launch/nativewindownav.go`).
  if (opened !== null) {
    const link = externalHref(opened + tunnelSuffix(kind, namespace));
    if (link !== undefined) {
      return (
        <a href={link} target="_blank" rel="noopener noreferrer" data-flow-ui={kind} data-flow-ui-open="" title={link} className={btnSecondary}>
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
      .then((forward) => {
        const scheme: Scheme = forward.scheme ?? 'http';
        const base = forwardLink(forward, scheme);
        if (externalHref(base + tunnelSuffix(kind, namespace)) === undefined) {
          setFailure('The tunnel opened at an address that cannot be linked');
          return;
        }
        setOpened(base);
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
