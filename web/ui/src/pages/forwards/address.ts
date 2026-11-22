import type { Forward } from '../../api';

/**
 * Where a forward is reached from this browser, for the two places that draw
 * one: a pod's or service's Ports panel (`pages/object/Ports.tsx`) and the list
 * of open forwards (`PortForwards.tsx`).
 *
 * Shared rather than copied: the loopback rewrite below is a security property,
 * and a second copy is the one that forgets it the day the first is corrected.
 */

/** `http` or `https`, the server's guess unless the user overrules it. */
export type Scheme = NonNullable<Forward['scheme']>;

/** The address to open, with the scheme the **server** chose unless the user
 *  chose otherwise. `Forward.address` is host and port only; putting `http` in
 *  front of an `https` port produces *"The plain HTTP request was sent to an
 *  HTTPS port"*, which reads as a broken tunnel rather than as a wrong link. */
export function forwardLink(forward: Forward, scheme: Scheme): string {
  return `${scheme}://${forwardAddress(forward)}`;
}

/** The forward's host and port as this browser should reach them - `apart`,
 *  below, applied to what the server answered. */
export function forwardAddress(forward: Forward): string {
  return apart(forward.address, window.location.hostname);
}

/**
 * The forward's address, under a loopback spelling the console itself is not
 * served from.
 *
 * **A cookie does not isolate by port** (RFC 6265 §8.5): the session cookie the
 * console set on `127.0.0.1:5823` is sent by the browser to `127.0.0.1` on
 * every port, the forward's included. Whatever listens behind the tunnel - a
 * pod's web server, its access log - would then receive a cookie that opens
 * every cluster on this machine, from a click the user made to look at that
 * pod. `localhost` and `127.0.0.1` are the same listener and two cookie hosts,
 * so the link is written under whichever of the two the console is not on: a
 * console on `127.0.0.1` (the launcher's address, `KT_LISTEN`'s default) links
 * `localhost`, and a console on `localhost` (the development loop) keeps the
 * `127.0.0.1` the server answered.
 *
 * The forward listens on `127.0.0.1` only (`internal/terminal/portforward.go`),
 * and `localhost` reaches it because a browser that tries `::1` first falls
 * back to `127.0.0.1` when nothing answers there. The guarantee rests on RFC 6265
 * and the unit test, not on observation in every browser.
 *
 * Only a `127.0.0.1:<port>` address is moved. Anything else the server says is
 * drawn as it came, because a host this function did not expect is not one it
 * knows a safe alternative for.
 */
function apart(address: string, consoleHost: string): string {
  const port = /^127\.0\.0\.1:(\d+)$/.exec(address)?.[1];
  if (port === undefined || consoleHost !== '127.0.0.1') return address;
  return `localhost:${port}`;
}
