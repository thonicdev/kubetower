import type { ReactNode } from 'react';

import type { Certificate, CertificateSet, ObjectURL } from '../../api';
import { externalHref } from '../../externalHref';
import { focusRing } from '../../focus';
import { TABLE } from '../../components/tableLook';

/**
 * What the TLS material in this object actually says, and the addresses it
 * serves.
 *
 * Both are **sections and not tabs**: the certificate parsing is arithmetic on
 * bytes already in hand, so there is no round trip to defer.
 *
 * ## One card per certificate, not per key
 *
 * `tls.crt` routinely holds a leaf plus its intermediates, and **rendering two
 * certificates as one is a wrong answer that looks right**. So the shape is
 * bundle -> certificates, and the bundle's key is drawn above them.
 *
 * ## The expiry grade is the server's
 *
 * `expiryTone` is graded against the certificate's **own lifetime**:
 * cert-manager renews at two thirds elapsed, so 30 days left on a 90-day
 * certificate is healthy and 30 days left on a one-year one is three months
 * late. A client holding only `notAfter` cannot tell those apart, so it draws
 * the tone and computes none of it.
 *
 * ## Five refusals, drawn as five sentences
 *
 * The refusals are: no `tls.crt` under a `kubernetes.io/tls` type, a value that
 * is not base64, bytes with no PEM block, a PEM block that is a key or a CSR.
 * Each is its own sentence, and each arrives as a `note` - on the set where there
 * is no bundle at all, on the bundle where the walk stopped.
 *
 * **A bundle's note sits beside the certificates that parsed, never instead of
 * them**: a chain whose second block is malformed
 * still has a valid leaf, and drawing the note alone says nothing was read.
 *
 * ## And the private key is named and not read
 *
 * The panel says that `tls.key` is in the Secret and the console does not read
 * it. That line is the difference between a console that cannot and one that
 * chose not to, so the names are drawn and there is no control anywhere that
 * would fetch a value.
 */

/** Full class strings, looked up. Tailwind scans source text, so an interpolated
 *  class exists in the DOM and in no stylesheet. */
const TONE: Readonly<Record<string, string>> = {
  normal: '',
  muted: 'text-muted',
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
};

export function Certificates({ set }: { set: CertificateSet | undefined }): ReactNode {
  if (set === undefined) return null;
  const bundles = set.bundles ?? [];
  const privateKeys = set.privateKeys ?? [];
  const note = set.note ?? '';
  if (bundles.length === 0 && privateKeys.length === 0 && note === '') return null;

  return (
    <section data-section="certificates">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Certificates</h3>

      {/* Why there is no bundle at all. An object that promised a certificate and
          has none is not an object with no certificates. */}
      {note === '' ? null : (
        <p data-certificates-note className="text-xs text-warn">
          {note}
        </p>
      )}

      <div className="flex flex-col gap-2">
        {bundles.map((bundle) => (
          <div key={bundle.key} data-bundle={bundle.key} className="rounded-md border border-line p-2">
            <p className="font-mono text-xs text-muted">{bundle.key}</p>

            {(bundle.certificates ?? []).map((certificate) => (
              <Card key={certificate.position} certificate={certificate} />
            ))}

            {/* Beside what parsed, never instead of it. */}
            {bundle.note === undefined || bundle.note === '' ? null : (
              <p data-bundle-note className="mt-1 text-xs text-warn">
                {bundle.note}
              </p>
            )}
          </div>
        ))}
      </div>

      {privateKeys.length === 0 ? null : (
        <p data-private-keys className="mt-1 text-xs text-muted">
          {/* Named, and deliberately not read. */}
          {privateKeys.join(', ')} {privateKeys.length === 1 ? 'is' : 'are'} in this object and
          this console does not read {privateKeys.length === 1 ? 'it' : 'them'}.
        </p>
      )}
    </section>
  );
}

function Card({ certificate }: { certificate: Certificate }): ReactNode {
  const sans = certificate.sans ?? [];

  return (
    <div data-certificate={String(certificate.position)} className="mt-2 first:mt-1">
      <p className="flex flex-wrap items-baseline gap-2">
        <span className="text-2xs text-muted uppercase">
          {/* Read off the certificate, never off its position: a leaf is not a
              CA, a root is a CA that signed itself, an intermediate is one that
              did not. */}
          {certificate.role}
        </span>
        <span
          data-expiry-tone={certificate.expiryTone ?? 'normal'}
          className={`text-sm ${TONE[certificate.expiryTone ?? 'normal'] ?? ''}`}
        >
          {certificate.expiry}
        </span>
        {certificate.selfSigned === true ? (
          <span data-self-signed className="text-xs text-muted">
            self-signed
          </span>
        ) : null}
        {certificate.isCA === true ? (
          <span data-is-ca className="text-xs text-muted">
            CA
          </span>
        ) : null}
      </p>

      {sans.length === 0 ? null : (
        <div data-sans className="mt-1 flex flex-wrap gap-1">
          {/* Chips, and the list a user matches against the Ingress host
              they are debugging. The common name alone has not been an answer
              since Chrome 58. */}
          {sans.map((name) => (
            <span
              key={name}
              className="rounded-sm border border-line bg-panel-2 px-1 font-mono text-xs"
            >
              {name}
            </span>
          ))}
          {certificate.sansNote === undefined || certificate.sansNote === '' ? null : (
            <span data-sans-note className="text-xs text-muted">
              {certificate.sansNote}
            </span>
          )}
        </div>
      )}

      <table className={`${TABLE} mt-1 w-full`}>
        <tbody>
          <Line label="Subject" value={certificate.subject} />
          <Line label="Issuer" value={certificate.issuer} />
          <Line label="Not before" value={certificate.notBefore} />
          <Line label="Not after" value={certificate.notAfter} />
          <Line label="Serial" value={certificate.serial} />
          <Line label="Signature" value={certificate.signatureAlgorithm} />
          <Line label="Public key" value={certificate.publicKey} />
        </tbody>
      </table>
    </div>
  );
}

function Line({ label, value }: { label: string; value: string | undefined }): ReactNode {
  if (value === undefined || value === '') return null;
  return (
    <tr>
      <th scope="row" className="w-28 py-0.5 pr-2 text-left align-top font-normal text-muted">
        {label}
      </th>
      {/* `wrap` on the cell: a subject or a serial is one unbreakable token that
          may be broken anywhere, and `white-space` inherits so nothing inside the
          cell can lift the rule. */}
      <td className="wrap py-0.5 align-top font-mono break-all">{value}</td>
    </tr>
  );
}

/**
 * The addresses this object serves.
 *
 * **Ingresses have them; nothing else does**, and the restraint is the point:
 * inventing a scheme for a LoadBalancer Service is the guess this type exists to
 * avoid, and a console that guesses one teaches the user that the addresses are
 * guesses.
 *
 * Every link opens in a new tab with `rel="noopener noreferrer"`, and the label
 * is http or https from the object's own TLS block rather than from parsing the
 * address string.
 *
 * **Only an http or https address becomes a link** (`externalHref`): the host is whoever wrote the Ingress. Anything else is drawn as the
 * text it is, in the same row.
 */
export function Addresses({
  urls,
  note,
}: {
  urls: readonly ObjectURL[];
  note?: string | undefined;
}): ReactNode {
  if (urls.length === 0 && (note === undefined || note === '')) return null;

  return (
    <section data-section="addresses">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Addresses</h3>
      <ul className="flex flex-col gap-0.5">
        {urls.map((url) => {
          const href = externalHref(url.address);
          const row = (
            <>
              <span className="w-12 shrink-0 text-2xs text-muted uppercase">
                {/* From the object's own TLS block, not from the string. */}
                {url.tls === true ? 'https' : 'http'}
              </span>
              <span className="min-w-0 flex-1 break-all">{url.address}</span>
              {url.service === undefined || url.service === '' ? null : (
                <span className="shrink-0 text-xs text-muted">{url.service}</span>
              )}
            </>
          );
          return (
            <li key={url.address} data-address={url.address} className="text-sm">
              {href === undefined ? (
                <span className="flex items-baseline gap-2">{row}</span>
              ) : (
                <a
                  href={href}
                  target="_blank"
                  rel="noopener noreferrer"
                  className={`flex items-baseline gap-2 rounded-xs text-accent hover:underline ${focusRing}`}
                >
                  {row}
                </a>
              )}
              {/* Why the address is not literally what the object writes - a path
                  pattern whose literal beginning is all that can be opened. */}
              {url.note === undefined || url.note === '' ? null : (
                <p data-address-note className="text-xs text-muted">
                  {url.note}
                </p>
              )}
            </li>
          );
        })}
      </ul>
      {note === undefined || note === '' ? null : (
        <p data-urls-note className="mt-1 text-xs text-muted">
          {note}
        </p>
      )}
    </section>
  );
}
