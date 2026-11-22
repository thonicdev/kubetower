// KubeTower service worker.
//
// It exists for one reason: to make the site installable as an application, so
// it gets its own window and its own icon in the Windows taskbar rather than
// living in a tab. It is deliberately the smallest thing that achieves that.
//
// It caches nothing. Every byte this console shows is live cluster state, and a
// worker holding on to any of it would serve a stale Kubernetes console after a
// rebuild — which is worse than no console, because it looks like it is working.
// A pod list from twenty minutes ago is not a degraded answer, it is a wrong
// one, and there is no way for the reader to tell.
//
// The fetch handler therefore passes everything straight through. It is here
// because a controlled page is what makes the install prompt appear, not because
// it does anything to the response.

self.addEventListener('install', () => {
  // Take over immediately rather than waiting for every tab to close. Nothing is
  // cached, so there is no old version to be careful about.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    // Clear anything a previous version of this worker may have cached, so an
    // upgrade cannot leave stale state behind.
    caches.keys()
      .then((names) => Promise.all(names.map((name) => caches.delete(name))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  // A navigation is left to the browser entirely: no respondWith, so it goes to
  // the network as the browser's own request, with the headers the server reads
  // to tell a page from a file. Re-issued through fetch(event.request) it does
  // not keep them - Chrome sends Sec-Fetch-Dest: empty, Firefox also
  // Sec-Fetch-Mode: same-origin, the shape of a script's fetch - and a reload
  // would then be answered "not in the front-end bundle" (internal/web/spa.go,
  // spaWantsAsset).
  //
  // The cost: Chrome and WebKit still count the page as controlled, which is
  // what Chrome's install offer reads; Firefox may not, and there that changes
  // nothing a person sees, since this worker caches nothing.
  if (event.request.mode === 'navigate') return;
  // Everything else straight through, always. See above.
  event.respondWith(fetch(event.request));
});
