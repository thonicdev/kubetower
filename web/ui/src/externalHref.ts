/**
 * An address that leaves the console, or `undefined` where it may not become a
 * link.
 *
 * Five anchors open something outside the console in a new tab: an Ingress's
 * addresses on its detail page and in a list cell, the repository and the
 * release page on Settings, and a port forward. In three of them somebody else
 * wrote the address - a cluster tenant chose the Ingress host, and the release
 * page is the `html_url` of GitHub's answer, copied unvalidated
 * (`internal/update/update.go`). Each producer happens to prefix `http` or
 * `https` today; this makes the renderer refuse anything else rather than rely
 * on that, the way `internalHref` below refuses anything that is not a
 * path for the console's own links.
 *
 * Only `http:` and `https:`, read by the URL parser rather than by a prefix
 * test, so `JaVaScRiPt:`, a leading space and a protocol-relative `//host` are
 * all answered by the same rule the browser would apply. The address is handed
 * back as it was written, never as the parser re-spelled it: what is drawn is
 * what the server said. A refused address is still drawn by the caller, as
 * text - hiding it would be a different answer from "this is not a link".
 */
export function externalHref(raw: string | undefined): string | undefined {
  if (raw === undefined || raw === '') return undefined;
  let url: URL;
  try {
    // No base: a relative or schemeless string throws, and is not an address
    // that leaves the console.
    url = new URL(raw);
  } catch {
    return undefined;
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return undefined;
  // `new URL` trims leading and trailing spaces before it reads the scheme; the
  // anchor's own parser does too, but a string that needed trimming is not one
  // a producer of this console writes.
  if (raw.trim() !== raw) return undefined;
  return raw;
}

/**
 * A server-supplied href, accepted only where it is a path on this origin.
 *
 * The console and the API are one process on one origin, so every `Ref.href`
 * that means anything is a path. Anything else - a scheme, a host, a
 * protocol-relative `//` - is either a mistake or a link out of the console
 * drawn as part of its own navigation, and neither is worth rendering as an
 * anchor an operator will click without reading.
 *
 * Was `Menu.tsx`'s own; it lives beside `externalHref` since a detail field's
 * `Ref` can be either kind (review of #80), so the two guards are one module.
 */
export function internalHref(href: string | undefined): string | undefined {
  if (href === undefined || href === '') return undefined;
  if (!href.startsWith('/') || href.startsWith('//') || href.includes('\\')) return undefined;
  return href;
}
