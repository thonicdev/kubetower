package redis

import (
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The prototype's nine tests of the credential walk, ported. What changed under
// them: the pod is the one the tunnel reached (Host.Forward names it), it is
// read through Host.List, and the Secret's value comes through Host.Reveal -
// one key of one named Secret, reviewed for the person and recorded - instead
// of a Get on the whole Secret with the console's own client.

func find(t *testing.T, h *fakeHost, podName string) (Credential, error) {
	t.Helper()
	return findPassword(httptest.NewRequest("GET", "/", nil), h, nil, "kind", "cache", podName)
}

// The ordinary case, and the one the operator asked for: a bitnami redis, and
// the console opens it without anybody running `base64 -d`.
func TestThePasswordIsReadFromThePodTheServiceRoutesTo(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-master-0", corev1.Container{
		Name: "redis",
		Env:  []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "redis-password")},
	}))
	h.secret("cache", "redis", map[string]string{"redis-password": "s3cret"})

	credential, err := find(t, h, "redis-master-0")
	if err != nil {
		t.Fatalf("findPassword: %v", err)
	}
	if credential.password != "s3cret" {
		t.Fatalf("password = %q, want the Secret's value", credential.password)
	}
	if credential.Secret != "redis" || credential.Key != "redis-password" || credential.Pod != "redis-master-0" {
		t.Fatalf("provenance = %s/%s for %s, want redis/redis-password for redis-master-0",
			credential.Secret, credential.Key, credential.Pod)
	}
	if credential.Source == "" {
		t.Fatal("a credential with no provenance cannot be shown to the operator, " +
			"and a console that reads a Secret silently is the thing this field exists against")
	}
	if len(h.reveals) != 1 || h.reveals[0] != "cache/redis/redis-password" {
		t.Fatalf("revealed %v, want exactly the one key the pod names", h.reveals)
	}
}

// **The one that decides whether this feature is safe to ship.** A Redis pod
// routinely runs an exporter beside the server, and the exporter's credential
// sits in the same pod under a name containing both REDIS and PASSWORD.
func TestTheServersOwnPasswordBeatsAnythingElseInTheSamePod(t *testing.T) {
	// The exporter is listed *first*, deliberately: a walk that returns on the
	// first plausible name passes this test only by accident of ordering.
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0",
		corev1.Container{
			Name: "metrics",
			Env:  []corev1.EnvVar{fromSecret("REDIS_EXPORTER_PASSWORD", "exporter", "password")},
		},
		corev1.Container{
			Name: "redis",
			Env: []corev1.EnvVar{
				fromSecret("REDIS_SENTINEL_PASSWORD", "sentinel", "password"),
				fromSecret("REDIS_PASSWORD", "redis", "redis-password"),
			},
		},
	))
	h.secret("cache", "exporter", map[string]string{"password": "wrong-exporter"})
	h.secret("cache", "sentinel", map[string]string{"password": "wrong-sentinel"})
	h.secret("cache", "redis", map[string]string{"redis-password": "right"})

	credential, err := find(t, h, "redis-0")
	if err != nil {
		t.Fatalf("findPassword: %v", err)
	}
	if credential.password != "right" {
		t.Fatalf("picked %q from Secret %q - the server's own REDIS_PASSWORD must win over "+
			"an exporter's and a sentinel's", credential.password, credential.Secret)
	}
	for _, read := range h.reveals {
		if !strings.HasPrefix(read, "cache/redis/") {
			t.Fatalf("revealed %s: a Secret the winning variable does not name was read", read)
		}
	}
}

// **The case only the refusal list reaches**: the exporter's is the *only*
// password-shaped variable in the pod. Ranked rather than refused, it would be
// picked, Redis would answer WRONGPASS, and a healthy instance would read as
// refusing a credential it was never using.
func TestAnExporterCredentialAloneIsRefusedRatherThanUsed(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0",
		corev1.Container{Name: "redis"},
		corev1.Container{
			Name: "metrics",
			Env:  []corev1.EnvVar{fromSecret("REDIS_EXPORTER_PASSWORD", "exporter", "password")},
		},
	))
	h.secret("cache", "exporter", map[string]string{"password": "not-the-servers"})

	credential, err := find(t, h, "redis-0")
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("authenticated with %q from Secret %q - an exporter's own credential is not "+
			"the server's (err = %v)", credential.password, credential.Secret, err)
	}
	if len(h.reveals) != 0 {
		t.Fatalf("revealed %v for a pod whose only candidate is refused", h.reveals)
	}
}

// A pod that names no password is an ordinary answer, not a failure.
func TestAPodWithNoPasswordSaysSoRatherThanFailing(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{
		Name: "redis",
		Env:  []corev1.EnvVar{{Name: "REDIS_PORT", Value: "6379"}},
	}))

	if _, err := find(t, h, "redis-0"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

// A refused Secret read is the *other* answer, and conflating the two sends the
// operator to the wrong person: "nothing names a password" is a matter for
// whoever wrote the chart, "you may not read that Secret" is an RBAC grant.
func TestARefusedSecretIsNotReportedAsNoPassword(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{
		Name: "redis",
		Env:  []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "redis-password")},
	}))
	h.revealErr = extension.ErrRevealDenied

	_, err := find(t, h, "redis-0")
	if err == nil {
		t.Fatal("a refused read must be an error")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Fatalf("a refused Secret read was reported as the pod naming no password: %v", err)
	}
	if !errors.Is(err, extension.ErrRevealDenied) {
		t.Fatalf("the refusal lost Host.Reveal's own answer, so the page cannot say whose it is: %v", err)
	}
}

// `kubectl create secret --from-file` leaves the file's trailing newline in the
// value, and a password that differs from the right one by an invisible byte
// fails as WRONGPASS.
func TestATrailingNewlineIsTrimmedFromTheSecretValue(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{
		Name: "redis",
		Env:  []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "password")},
	}))
	h.secret("cache", "redis", map[string]string{"password": "s3cret\r\n"})

	credential, err := find(t, h, "redis-0")
	if err != nil {
		t.Fatalf("findPassword: %v", err)
	}
	if credential.password != "s3cret" {
		t.Fatalf("password = %q, want it trimmed of the trailing newline", credential.password)
	}
}

// An `envFrom.secretRef` hands the container the whole Secret, so the key has to
// be picked out by name. It ranks below every named variable, and the key it
// picks must itself be a password: a Secret holding `redis-password` beside
// `redis-host` must not yield the host - and here, must not even be *asked* for
// the host, since Host.Reveal records every key it is asked for.
func TestAWholeSecretEnvironmentYieldsThePasswordKeyAndNotAnother(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{
		Name: "redis",
		EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "redis-env"}}}},
	}))
	h.secret("cache", "redis-env", map[string]string{
		"redis-host":     "redis.cache.svc",
		"redis-password": "s3cret",
		"redis-port":     "6379",
	})

	credential, err := find(t, h, "redis-0")
	if err != nil {
		t.Fatalf("findPassword: %v", err)
	}
	if credential.Key != "redis-password" || credential.password != "s3cret" {
		t.Fatalf("picked key %q = %q, want redis-password", credential.Key, credential.password)
	}
	for _, read := range h.reveals {
		key := read[strings.LastIndex(read, "/")+1:]
		if rankVariable(strings.ReplaceAll(key, "-", "_")) < 0 {
			t.Fatalf("asked for key %q, which is not named like a password", key)
		}
	}
}

// The boundary, stated as a test rather than as a paragraph: this console reads
// no Secret the Redis pod is not already given.
func TestNoSecretIsReadThatThePodDoesNotName(t *testing.T) {
	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis"}))
	h.secret("cache", "redis-password", map[string]string{"password": "not-yours"})
	h.secret("cache", "postgres-credentials", map[string]string{"password": "definitely-not-yours"})

	if _, err := find(t, h, "redis-0"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if len(h.reveals) != 0 {
		t.Fatalf("%d Secret(s) read for a pod that names none - this must never go looking: %v",
			len(h.reveals), h.reveals)
	}
}

// The ranking, exercised directly, because the table is where a future edit
// happens and a whole-pod test would only catch the cases it happens to build.
func TestRankVariableRefusesTheCredentialsThatBelongToSomethingElse(t *testing.T) {
	for _, name := range []string{
		"REDIS_SENTINEL_PASSWORD", "REDIS_EXPORTER_PASSWORD", "REDIS_METRICS_PASSWORD",
		"REDIS_PASSWORD_FILE", "POSTGRES_PASSWORD", "AWS_SECRET_ACCESS_KEY", "REDIS_PORT",
	} {
		if rank := rankVariable(name); rank >= 0 {
			t.Errorf("%s scored %d - it is not the password this server is started with", name, rank)
		}
	}

	if rankVariable("REDIS_PASSWORD") <= rankVariable("REDIS_MASTER_PASSWORD") {
		t.Error("the server's own password must outrank a replica's copy of the master's")
	}
	if rankVariable("REDIS_MASTER_PASSWORD") <= rankVariable("PASSWORD") {
		t.Error("a named variable must outrank a bare PASSWORD")
	}
	if rankVariable("PASSWORD") < 0 {
		t.Error("a hand-written Deployment's bare PASSWORD is vague, not wrong")
	}
}

// Every name asked of an `envFrom` Secret is an audited Host.Reveal, so the
// walk is ordered by rank and the commonest spelling first, and the extension
// remembers which name answered: the first request costs the walk, every later
// one a single reveal.
func TestAWholeSecretEnvironmentCostsItsWalkOnce(t *testing.T) {
	for i := 1; i < len(envFromCandidates); i++ {
		rank := func(k string) int { return rankVariable(strings.ReplaceAll(k, "-", "_")) }
		if rank(envFromCandidates[i]) > rank(envFromCandidates[i-1]) {
			t.Fatalf("%s outranks %s and is asked after it", envFromCandidates[i], envFromCandidates[i-1])
		}
		if rank(envFromCandidates[i]) < 0 {
			t.Fatalf("%s is not named like a password", envFromCandidates[i])
		}
	}
	if envFromCandidates[0] != "redis-password" {
		t.Fatalf("the first name asked is %q; bitnami's redis-password should cost one reveal", envFromCandidates[0])
	}

	h := newHost()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{
		Name: "redis",
		EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "redis-env"}}}},
	}))
	h.secret("cache", "redis-env", map[string]string{"REQUIREPASS": "s3cret"})
	names := newKeyNames()
	r := httptest.NewRequest("GET", "/", nil)

	if _, err := findPassword(r, h, names, "kind", "cache", "redis-0"); err != nil {
		t.Fatal(err)
	}
	first := len(h.reveals)
	if first != slices.Index(envFromCandidates, "REQUIREPASS")+1 {
		t.Fatalf("the first request revealed %d names, want the walk up to REQUIREPASS", first)
	}
	for range 3 {
		if c, err := findPassword(r, h, names, "kind", "cache", "redis-0"); err != nil || c.Key != "REQUIREPASS" {
			t.Fatalf("a later request found %q (%v)", c.Key, err)
		}
	}
	if got := len(h.reveals) - first; got != 3 {
		t.Fatalf("three later requests revealed %d names, want one each", got)
	}

	// A key that moved is forgotten and the walk runs again.
	h.secret("cache", "redis-env", map[string]string{"redis-password": "rotated"})
	if c, err := findPassword(r, h, names, "kind", "cache", "redis-0"); err != nil || c.Key != "redis-password" {
		t.Fatalf("after the key moved, found %q (%v)", c.Key, err)
	}
}

// filePod is bitnami's shape with `usePasswordFiles` (its default): the
// server reads REDIS_PASSWORD_FILE, a file of a Secret volume, and the
// sentinel container beside it reads the same file.
func filePod(volume corev1.Volume, mountPath, subPath string, env ...corev1.EnvVar) *corev1.Pod {
	mount := corev1.VolumeMount{Name: volume.Name, MountPath: mountPath, SubPath: subPath}
	p := pod("cache", "redis-node-0",
		corev1.Container{Name: "redis", Env: env, VolumeMounts: []corev1.VolumeMount{mount}},
		corev1.Container{
			Name:         "sentinel",
			Env:          []corev1.EnvVar{{Name: "REDIS_SENTINEL_PASSWORD_FILE", Value: "/secrets/sentinel"}},
			VolumeMounts: []corev1.VolumeMount{mount},
		},
	)
	p.Spec.Volumes = []corev1.Volume{volume}
	return p
}

func secretVolume(secret string, items ...corev1.KeyToPath) corev1.Volume {
	return corev1.Volume{Name: "redis-password", VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: secret, Items: items},
	}}
}

// Measured on kind, 2026-10-25: the bitnami redis chart with a sentinel and a
// password read as "no password found" because the password was a file.
func TestAPasswordFileFromASecretVolumeIsReadAsTheKeyItMaps(t *testing.T) {
	h := newHost()
	h.add(t, "pods", filePod(
		secretVolume("rsent-redis", corev1.KeyToPath{Key: "redis-password", Path: "redis-password"}),
		"/opt/bitnami/redis/secrets/", "",
		corev1.EnvVar{Name: "REDIS_PASSWORD_FILE", Value: "/opt/bitnami/redis/secrets/redis-password"},
		corev1.EnvVar{Name: "REDIS_MASTER_PASSWORD_FILE", Value: "/opt/bitnami/redis/secrets/redis-password"},
	))
	h.secret("cache", "rsent-redis", map[string]string{"redis-password": "from-a-file\n"})

	credential, err := find(t, h, "redis-node-0")
	if err != nil {
		t.Fatalf("findPassword: %v", err)
	}
	if credential.password != "from-a-file" || credential.Secret != "rsent-redis" || credential.Key != "redis-password" {
		t.Fatalf("got %s/%s = %q, want rsent-redis/redis-password trimmed", credential.Secret, credential.Key, credential.password)
	}
	if !strings.Contains(credential.Source, "REDIS_PASSWORD_FILE") {
		t.Fatalf("source %q does not say which variable named the file", credential.Source)
	}
	if !slices.Equal(h.reveals, []string{"cache/rsent-redis/redis-password"}) {
		t.Fatalf("revealed %v, want the one key the file is", h.reveals)
	}
}

// `items` renames: the file is `pass`, the key is `redis-password`. And with
// no items, the file is named like its key; a subPath mount names the file
// the mount path is.
func TestAPasswordFileFollowsItemsAndSubPath(t *testing.T) {
	for _, tc := range []struct {
		name           string
		volume         corev1.Volume
		mountPath, sub string
		file, wantKey  string
	}{
		{"items", secretVolume("redis", corev1.KeyToPath{Key: "redis-password", Path: "pass"}), "/secrets", "", "/secrets/pass", "redis-password"},
		{"no items", secretVolume("redis"), "/secrets", "", "/secrets/redis-password", "redis-password"},
		{"subPath", secretVolume("redis"), "/etc/redis/pass", "auth", "/etc/redis/pass", "auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost()
			h.add(t, "pods", filePod(tc.volume, tc.mountPath, tc.sub, corev1.EnvVar{Name: "REDIS_PASSWORD_FILE", Value: tc.file}))
			h.secret("cache", "redis", map[string]string{tc.wantKey: "x"})
			credential, err := find(t, h, "redis-node-0")
			if err != nil || credential.Key != tc.wantKey {
				t.Fatalf("key = %q, err = %v; want %q", credential.Key, err, tc.wantKey)
			}
		})
	}
}

// A file that is not one key of a Secret volume is not guessed at, and the
// sentinel's own file is refused as its variable is.
func TestAPasswordFileThisConsoleCannotNameIsNotRead(t *testing.T) {
	configMap := corev1.Volume{Name: "redis-password", VolumeSource: corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "redis"}},
	}}
	for _, tc := range []struct {
		name   string
		volume corev1.Volume
		env    corev1.EnvVar
	}{
		{"a configmap", configMap, corev1.EnvVar{Name: "REDIS_PASSWORD_FILE", Value: "/secrets/redis-password"}},
		{"no mount covers it", secretVolume("redis"), corev1.EnvVar{Name: "REDIS_PASSWORD_FILE", Value: "/elsewhere/redis-password"}},
		{"a subdirectory", secretVolume("redis"), corev1.EnvVar{Name: "REDIS_PASSWORD_FILE", Value: "/secrets/a/redis-password"}},
		{"the sentinel's", secretVolume("redis"), corev1.EnvVar{Name: "REDIS_SENTINEL_PASSWORD_FILE", Value: "/secrets/redis-password"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost()
			h.add(t, "pods", filePod(tc.volume, "/secrets", "", tc.env))
			h.secret("cache", "redis", map[string]string{"redis-password": "x"})
			if _, err := find(t, h, "redis-node-0"); !errors.Is(err, ErrNoCredential) {
				t.Fatalf("err = %v, want ErrNoCredential", err)
			}
			if len(h.reveals) != 0 {
				t.Fatalf("revealed %v", h.reveals)
			}
		})
	}
}
