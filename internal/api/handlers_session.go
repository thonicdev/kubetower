package api

import "net/http"

// --- Probes -----------------------------------------------------------------

// liveness says the process is serving, and nothing else. It consults no
// backend, so a cluster being down never restarts the console; that is
// readiness's question.
func (a *API) liveness(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	writeOK(w, ProbeResult{OK: true, Detail: "the console is serving"})
}

// readiness says whether the cluster configuration is readable. It must be
// able to fail, or a process whose kubeconfig mount vanished would stay in
// service. A build with no probe answers 503: not knowing is not knowing it
// can.
func (a *API) readiness(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if a.deps.Probe == nil {
		WriteProblem(w, http.StatusServiceUnavailable, ProblemInternal,
			"Not ready", "this build has no readiness probe, so readiness cannot be established")
		return
	}
	ok, detail := a.deps.Probe.Ready(r.Context())
	if !ok {
		WriteProblem(w, http.StatusServiceUnavailable, ProblemInternal, "Not ready", detail)
		return
	}
	writeOK(w, ProbeResult{OK: true, Detail: detail})
}

// noStore is on both probes and on the endpoint that reveals a secret; a
// cached "ok" is a probe that cannot fail.
func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

// --- Session ----------------------------------------------------------------

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if a.deps.Sessions == nil {
		missing(w, "a front door")
		return
	}
	a.deps.Sessions.LoginJSON(w, r)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if a.deps.Sessions == nil {
		// Ending a session must not require one or a door: a failing logout
		// leaves the front end unable to reach its login screen.
		writeNoContent(w)
		return
	}
	a.deps.Sessions.LogoutJSON(w, r)
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	if a.deps.Sessions == nil {
		missing(w, "a front door")
		return
	}
	s, ok := a.deps.Sessions.Current(r)
	if !ok {
		unauthenticated(w, ProblemSessionExpired, "no session, or it has lapsed")
		return
	}
	writeOK(w, s)
}

// getSignInMethods is public, and a build with no SignIn answers the shape of a
// build without single sign-on: a password, and no provider.
func (a *API) getSignInMethods(w http.ResponseWriter, r *http.Request) {
	// no-store: the provider's state changes while a page is open, and a
	// cached "unavailable" would keep the sign-in button hidden.
	noStore(w)
	if a.deps.SignIn == nil {
		writeOK(w, SignInMethods{Password: true, SSO: SSO{State: SSOOff}})
		return
	}
	writeOK(w, a.deps.SignIn.Methods())
}

func (a *API) getCapabilities(w http.ResponseWriter, r *http.Request) {
	if a.deps.Profile == nil {
		missing(w, "a profile")
		return
	}
	writeOK(w, a.deps.Profile.Capabilities(r))
}
