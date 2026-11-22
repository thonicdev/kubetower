import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { btnSecondary, externalHref, forwardLink, openForward, useCapabilities, type Scheme } from '@kubetower/ui';

import { configuredHref, tunnelSuffix, uisQuery, type FlowUI } from './data';

const KINDS = [
  { kind: 'hubble', label: 'Hubble UI' },
  { kind: 'whisker', label: 'Whisker' },
] as const;

/**
 * The buttons that open Hubble UI (Cilium) and Whisker (Calico) in a new tab,
 * for the UIs this cluster has, and nothing where it has neither.
 *
 * An address set in the extra's settings is a link. Without one the button
 * opens a tunnel with the console's own port-forward, which exists only where
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

  const open = (): void => {
    // The tab is opened inside the click, before the forward is awaited, or the
    // browser takes it for a pop-up.
    const tab = window.open('', '_blank');
    if (tab === null) {
      setFailure('The browser blocked the new tab');
      return;
    }
    tab.opener = null;
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
        const link = externalHref(forwardLink(forward, scheme) + tunnelSuffix(kind, namespace));
        if (link === undefined) {
          tab.close();
          setFailure('The tunnel opened at an address that cannot be linked');
          return;
        }
        tab.location.href = link;
      })
      .catch((error: unknown) => {
        tab.close();
        setFailure(error instanceof Error ? error.message : String(error));
      })
      .finally(() => setPending(false));
  };

  return (
    <>
      <button type="button" data-flow-ui={kind} disabled={pending} onClick={open} className={btnSecondary}>
        {label}
      </button>
      {failure === null ? null : (
        <span role="alert" className="text-xs text-bad">
          {failure}
        </span>
      )}
    </>
  );
}
