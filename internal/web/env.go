package web

// A container's environment: names are shown freely (the Secrets list already
// prints key names), a ConfigMap's values are on the page, and a Secret's values
// are fetched one key at a time on request (RevealEnv). There is no "reveal all":
// decoding a whole Secret into the page would put every credential in it on
// screen. Nothing keeps a revealed value across a redraw.
