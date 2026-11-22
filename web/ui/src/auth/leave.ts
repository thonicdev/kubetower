/**
 * Leaves the single-page console for an address the server renders, as a
 * navigation of the whole document.
 *
 * The router cannot go there: the sign-in page is not one of its routes, and a
 * client-side move would only change the address bar. `replace`, so Back does
 * not return to a page that has just lost its session.
 *
 * Held in a variable so a test can see where the console left for: jsdom does
 * not navigate, and its `location` cannot be replaced.
 */
let leave = (href: string): void => {
  window.location.replace(href);
};

export function leaveFor(href: string): void {
  leave(href);
}

/** Test seam: where the console leaves for is handed to `spy` instead. */
export function watchLeaving(spy: (href: string) => void): void {
  leave = spy;
}

/** Test seam. Leaves the module as a fresh import would. */
export function resetLeaving(): void {
  leave = (href: string): void => {
    window.location.replace(href);
  };
}
