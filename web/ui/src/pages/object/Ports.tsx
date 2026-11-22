import { useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import {
  closeForward,
  forwardsQuery,
  openForward,
  queryKeys,
  useCapabilities,
  type Forward,
  type ObjectPort,
} from '../../api';
import { CopyButton } from '../../components/CopyButton';
import { externalHref } from '../../externalHref';
import { btnSecondary, btnToggle } from '../../components/buttons';
import { focusRingTight } from '../../focus';
import { forwardLink, type Scheme } from '../forwards/address';

/**
 * What an object exposes, and the one panel whose row is also a control.
 *
 * ## Whether a port can be forwarded is the server's answer
 *
 * `port.forwardable` arrives already decided, with a `reason` where it is false.
 * Nothing here derives it from `protocol`, and that is deliberate: the rule
 * lives on the server and a copy in the browser is the copy that
 * offers a button the server then refuses. A port that cannot be forwarded is a
 * **disabled control carrying why** rather than an absent one - a user
 * looking for it finds it and learns that port-forwarding is TCP.
 *
 * ## A forward is not a tab
 *
 * Opening one does not add a pane to the dock. The
 * button becomes the address it is listening on, and the link's scheme is the
 * server's - it decides from the Service's `appProtocol`, then the port's name,
 * then a table of known TLS ports, and a copy of that table here would drift the
 * first time one of them learned a new port.
 *
 * **And the user can overrule it.** The server's answer is a guess with
 * three tiers of authority behind it, and a guess is sometimes wrong: a service
 * on 8443 that guessed `http` is a click that fails.
 * A small `http` / `https` pair beside the address rewrites the link's scheme
 * **client-side and for this address only** - nothing is sent, the server still
 * decides the default, and the table stays where it is. A segmented pair rather
 * than a select, because there are two answers and both should be visible.
 *
 * ## And the address has a way back
 *
 * Without one, a forward would outlive the drawer and the tab until the process
 * exited. A **Stop** sits beside the address and the panel asks
 * `GET /api/forwards` on mount, so a forward opened from another drawer, or
 * before a reload, is drawn beside its port and is stoppable here too. The cap
 * on forwards is per process, so stopping is also what frees a slot. The list is one query-cache entry shared by every Ports panel, and
 * opening or closing writes to it rather than to a row's own state - a row's
 * own state is how the same tunnel was drawn as open in one drawer and absent
 * in the next.
 *
 * ## Or the command, where the console cannot forward for you
 *
 * `capabilities.portForward` false is **show the command**. A forward binds a
 * port inside the console's own process and hands back `127.0.0.1:<port>`; where
 * the console runs in a pod, that address in the viewer's browser is their own
 * machine, where nothing listens - broken whatever the RBAC, and if it worked,
 * every person signed in would share the pod's one range of ports. So the row
 * draws the `kubectl port-forward` line for its port instead, with a copy
 * button: the tunnel opens on the viewer's machine, with the viewer's own
 * credentials, and it is the cluster that decides whether they may. A port that
 * cannot be forwarded (UDP) gets no command, and keeps its reason.
 *
 * Nothing is drawn in the control's place while capabilities are in flight: a
 * Forward button that turned into a command a moment later would be a control
 * offered and withdrawn.
 */
export function Ports({
  cluster,
  namespace,
  name,
  kind,
  ports,
  note,
}: {
  cluster: string;
  namespace: string | undefined;
  name: string;
  /** The route's plural. It decides which half of the request this object is -
   *  a Service forward survives the pod being replaced under it, which is what
   *  a rollout does while somebody is watching the tunnel. */
  kind: string;
  ports: readonly ObjectPort[];
  note: string | undefined;
}): ReactNode {
  // Which of the two this console draws, and nothing until it is known.
  const capabilities = useCapabilities();
  const mode: Mode | undefined =
    capabilities.data === undefined
      ? undefined
      : capabilities.data.portForward
        ? 'forward'
        : 'command';
  // Asked only where a row could hold a forward. A ConfigMap's drawer has no
  // business listing tunnels, and a profile without a forwarder has no such
  // operation at all - asking would be a 404 from every object opened.
  const live = useQuery(
    forwardsQuery(mode === 'forward' && ports.some((port) => port.forwardable === true)),
  );

  if (ports.length === 0) {
    if (note === undefined || note === '') return null;
    return (
      <section className="flex flex-col gap-1">
        <h3 className="text-xs font-medium text-muted uppercase">Ports</h3>
        <p className="text-xs text-muted">{note}</p>
      </section>
    );
  }

  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-xs font-medium text-muted uppercase">Ports</h3>
      <ul className="flex flex-col gap-1">
        {ports.map((port, index) => (
          <Row
            // A port number is unique per object in every case Kubernetes
            // allows, but a malformed object is not something to crash on.
            key={`${port.port}-${port.protocol ?? ''}-${index}`}
            cluster={cluster}
            namespace={namespace}
            name={name}
            kind={kind}
            port={port}
            mode={mode}
            // A failed list is not an empty one, but the row can do nothing
            // about the difference except keep offering the control: the
            // forward it opens is written to the cache itself, so it is drawn
            // whatever the list said.
            forward={held(live.data ?? [], cluster, namespace, name, kind, port)}
          />
        ))}
      </ul>
      {note === undefined || note === '' ? null : <p className="text-xs text-muted">{note}</p>}
    </section>
  );
}

/** What a row offers: a forward this console opens, the command that opens one
 *  on the viewer's own machine, or - while the capabilities are unknown -
 *  nothing yet. */
type Mode = 'forward' | 'command';

function Row({
  cluster,
  namespace,
  name,
  kind,
  port,
  mode,
  forward,
}: {
  cluster: string;
  namespace: string | undefined;
  name: string;
  kind: string;
  port: ObjectPort;
  mode: Mode | undefined;
  /** The forward this process holds on this port, whoever opened it. */
  forward: Forward | undefined;
}): ReactNode {
  const client = useQueryClient();
  const [failure, setFailure] = useState<string | null>(null);
  // The user's scheme, tied to the forward it was chosen for: a choice made
  // on one tunnel must not outlive it and apply to the next one opened on the
  // same port, whose default the server may have got right.
  const [chosen, setChosen] = useState<{ id: string; scheme: Scheme } | null>(null);
  const scheme: Scheme =
    forward !== undefined && chosen?.id === forward.id ? chosen.scheme : (forward?.scheme ?? 'http');

  const open = useMutation({
    mutationFn: () =>
      openForward({
        cluster,
        namespace: namespace ?? '',
        // Exactly one of the two, which the server refuses to guess at: a
        // request naming both would leave the target to whichever field the
        // layer below read first.
        ...(isServiceKind(kind) ? { service: name } : { pod: name }),
        remotePort: port.port,
        // Sent so the **server** decides the scheme. They take no part in the
        // identity of the target, so two clicks on one port are still one
        // tunnel whatever this panel knew about it.
        ...(port.name === undefined || port.name === '' ? {} : { portName: port.name }),
        ...(port.appProtocol === undefined || port.appProtocol === ''
          ? {}
          : { appProtocol: port.appProtocol }),
      }),
    onSuccess: (made) => {
      setFailure(null);
      // Into the shared list, at once: the row is drawn from it, and a re-read
      // follows so that what is drawn is what the server holds.
      client.setQueryData<Forward[]>(queryKeys.forwards, (before) => [
        ...(before ?? []).filter((one) => one.id !== made.id),
        made,
      ]);
      void client.invalidateQueries({ queryKey: queryKeys.forwards });
    },
    onError: (error: Error) => setFailure(error.message),
  });

  const close = useMutation({
    mutationFn: (id: string) => closeForward(id),
    onSuccess: (_nothing, id) => {
      setFailure(null);
      client.setQueryData<Forward[]>(queryKeys.forwards, (before) =>
        (before ?? []).filter((one) => one.id !== id),
      );
      void client.invalidateQueries({ queryKey: queryKeys.forwards });
    },
    // The address stays: a stop the server refused is a forward still open,
    // and taking the link away would say otherwise.
    onError: (error: Error) => setFailure(error.message),
  });

  return (
    <li className="flex flex-wrap items-baseline gap-2 border-b border-line py-1 text-xs last:border-b-0">
      <span className="tabular-nums">{port.port}</span>
      {port.protocol === undefined || port.protocol === '' ? null : (
        <span className="text-xs text-muted">{port.protocol}</span>
      )}
      {port.name === undefined || port.name === '' ? null : (
        <span className="text-muted">{port.name}</span>
      )}
      {/* A Service's pod-side port is a number **or the name of a container
          port**, so it is drawn as the string it is rather than parsed. */}
      {port.targetPort === undefined || port.targetPort === '' ? null : (
        <span className="text-xs text-muted">&rarr; {port.targetPort}</span>
      )}
      {port.nodePort === undefined || port.nodePort === 0 ? null : (
        <span className="text-xs text-muted tabular-nums">node {port.nodePort}</span>
      )}
      {port.hostPort === undefined || port.hostPort === 0 ? null : (
        <span className="text-xs text-muted tabular-nums">host {port.hostPort}</span>
      )}
      {port.container === undefined || port.container === '' ? null : (
        <span className="text-xs text-muted">{port.container}</span>
      )}

      <span className="flex-1" />

      {mode === undefined ? null : mode === 'command' ? (
        port.forwardable === true ? (
          <Command line={portForwardCommand(namespace, name, kind, port.port)} />
        ) : null
      ) : forward !== undefined ? (
        <>
          {/* **The button became the address.** A forward is not a tab: what it
              gives back is a link, and the scheme on it is the server's until
              the user says otherwise. */}
          {/* The scheme is a closed set of two, and the address is the
              console's own listener - but every anchor that leaves the console
              goes through the one guard (`externalHref`), so this
              one cannot be the exception the day either half changes. */}
          {externalHref(forwardLink(forward, scheme)) === undefined ? (
            <span className="rounded-md border border-line px-2 py-0.5 text-xs text-fg">
              {forwardLink(forward, scheme)}
            </span>
          ) : (
            <a
              href={externalHref(forwardLink(forward, scheme))}
              target="_blank"
              rel="noreferrer"
              className={`rounded-md border border-line px-2 py-0.5 text-xs text-fg hover:underline ${focusRingTight}`}
            >
              {forwardLink(forward, scheme)}
            </a>
          )}
          <div role="group" aria-label="Scheme" className="flex gap-1">
            {SCHEMES.map((one) => {
              const on = one === scheme;
              return (
                <button
                  key={one}
                  type="button"
                  aria-pressed={on}
                  title={
                    on
                      ? `The link opens with ${one}`
                      : `Open the link with ${one} instead; the server guessed ${forward.scheme ?? 'http'}`
                  }
                  onClick={() => setChosen({ id: forward.id, scheme: one })}
                  className={btnToggle(on)}
                >
                  {one}
                </button>
              );
            })}
          </div>
          <button
            type="button"
            disabled={close.isPending}
            title={`Stop forwarding ${port.port}; the address stops answering and the slot is freed`}
            onClick={() => close.mutate(forward.id)}
            className={btnSecondary}
          >
            {close.isPending ? 'Stopping…' : 'Stop'}
          </button>
        </>
      ) : (
        <button
          type="button"
          disabled={port.forwardable !== true || open.isPending}
          // Disabled and carrying why, never absent: a user looking for
          // the control finds it and learns that port-forwarding is TCP.
          title={
            port.forwardable === true
              ? `Forward ${port.port} to this machine`
              : (port.reason?.message ?? 'this port cannot be forwarded')
          }
          onClick={() => open.mutate()}
          className={btnSecondary}
        >
          {open.isPending ? 'Opening…' : 'Forward'}
        </button>
      )}

      {failure === null ? null : (
        // The server's own sentence, drawn rather than paraphrased - a refused
        // forward says whether the cap was reached, whether the cluster refused
        // it, or whether nothing was listening.
        <span role="alert" className="w-full text-xs text-bad">
          {failure}
        </span>
      )}
      {port.forwardable !== true && port.reason?.message !== undefined ? (
        <span className="w-full text-xs text-muted">{port.reason.message}</span>
      ) : null}
    </li>
  );
}

/**
 * The forward this process holds on one row, out of everything it holds.
 *
 * A Service row is recognized by `service`, a pod row by `pod` **with no
 * `service`**: a Service forward names the replica it landed on too, and a pod
 * row matching on that alone would draw somebody else's tunnel and offer to
 * stop it. The other way round, a Service row matching on `pod` would lose its
 * own forward the moment a rollout replaced the pod behind it - which is the
 * one thing a Service forward exists to survive.
 */
function held(
  live: readonly Forward[],
  cluster: string,
  namespace: string | undefined,
  name: string,
  kind: string,
  port: ObjectPort,
): Forward | undefined {
  return live.find(
    (one) =>
      one.cluster === cluster &&
      one.namespace === (namespace ?? '') &&
      one.remotePort === port.port &&
      (isServiceKind(kind)
        ? one.service === name
        : one.pod === name && (one.service === undefined || one.service === '')),
  );
}

/** Both answers, in the order a reader expects them. Two and not a table: the
 *  table of which port speaks TLS is the server's, and this is only the choice
 *  of what to put in front of an address it already opened. */
const SCHEMES: readonly Scheme[] = ['http', 'https'];

/**
 * The line to copy, drawn as code so that what is selected is what runs.
 *
 * `role="group"` with the command as its name, so a screen reader announces the
 * command once and the copy button beside it as the thing that acts on it.
 */
function Command({ line }: { line: string }): ReactNode {
  return (
    <span role="group" aria-label={line} className="flex min-w-0 items-center gap-1">
      <code
        title="Run this on your own machine: the tunnel opens there, with your credentials"
        className="truncate rounded-md border border-line px-2 py-0.5 font-mono text-2xs text-fg select-all"
      >
        {line}
      </code>
      <CopyButton value={line} label="Copy" />
    </span>
  );
}

/**
 * `kubectl port-forward -n <ns> <pod|svc>/<name> <local>:<remote>`.
 *
 * The local port is the remote one, except below 1024, where it is moved up by
 * 8000 - 80 becomes 8080 and 443 becomes 8443. A port under 1024 needs a
 * privilege on the viewer's machine that the command should not assume, and
 * those two are what anybody would have typed. Only a suggestion: the viewer
 * edits it, and `kubectl` says plainly when a port is taken.
 */
function portForwardCommand(
  namespace: string | undefined,
  name: string,
  kind: string,
  remote: number,
): string {
  const local = remote < 1024 ? remote + 8000 : remote;
  const target = `${isServiceKind(kind) ? 'svc' : 'pod'}/${name}`;
  const scope = namespace === undefined || namespace === '' ? '' : ` -n ${namespace}`;
  return `kubectl port-forward${scope} ${target} ${String(local)}:${String(remote)}`;
}

/** Which half of the request this object is. The route carries the discovery
 *  plural, and only `services` forwards as a service. */
function isServiceKind(kind: string): boolean {
  return (kind.split('.')[0] ?? '').toLowerCase() === 'services';
}
