package redis

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// Finding the password an instance is already using.
//
// It reads no Secret the Redis pod is not already given, and only for the pod
// the tunnel reached: the `secretKeyRef` (or `envFrom.secretRef`) that pod's
// own container is started with, one key through `Host.Reveal`, reviewed for
// the person as `get secrets/<name>` and recorded. A Service fronting pods
// with different passwords can never be sent another pod's credential,
// because the password goes down the tunnel it was read for.
//
// It runs only after the instance answered NOAUTH to an unauthenticated dial,
// so an unauthenticated Redis is browsed without reading any Secret.
//
// The value lives in an unexported field of a Credential and is gone with the
// request. A response carries only its provenance (Secret, Key, Source, Pod),
// never the value: a password has exactly one destination.

// ErrNoCredential is "nothing in the pod names a password this console can
// use".
//
// A distinct error, because the page draws it differently from a refused read,
// which is for whoever grants RBAC.
var ErrNoCredential = errors.New("no password found in the pod the tunnel reached")

// Credential is a password and, more importantly, where it came from.
type Credential struct {
	// Secret is the Secret's name, Key the key inside it. Both are shown.
	Secret string `json:"secret"`
	Key    string `json:"key"`
	// Source is the sentence the page puts beside them - which container of
	// which pod is started with it.
	Source string `json:"source"`
	// Pod is the pod it was read for, which is the pod the tunnel reached.
	Pod string `json:"pod"`
	// password is the value. **Unexported, so no encoder can carry it; never
	// rendered, never logged, never in an error or a URL.**
	password string
}

// findPassword reads the password pod - the one the tunnel reached - is started
// with, for the person behind r.
//
// It returns ErrNoCredential (wrapped, with the sentence) when the pod names
// none; that is an ordinary answer and not a failure. A refused or failed read
// comes back wrapping Host.Reveal's own answer (extension.ErrRevealDenied,
// ErrRevealUnreviewed, ErrUnknownCluster, or a failure), which the caller must
// say differently.
//
// names remembers which key an `envFrom` Secret answered to; nil remembers
// nothing.
func findPassword(r *http.Request, h extension.Host, names *keyNames, cluster, namespace, podName string) (Credential, error) {
	pod, err := readPod(r, h, cluster, namespace, podName)
	if err != nil {
		return Credential{}, fmt.Errorf("the pod %s the tunnel reached could not be read: %w", podName, err)
	}
	if pod == nil {
		return Credential{}, fmt.Errorf("the pod %s the tunnel reached is not there any more: %w", podName, ErrNoCredential)
	}

	best := bestInPod(pod)
	if best.rank < 0 {
		return Credential{}, fmt.Errorf("pod %s is started with no variable naming a password: %w", podName, ErrNoCredential)
	}

	keys := []string{best.key}
	if best.key == "" {
		// An `envFrom.secretRef` hands the container every key, so no variable
		// name exists to match on. Host.Reveal reads one named key, never the
		// whole Secret, so the password names are asked for in rank order and
		// the first present wins.
		keys = envFromCandidates
		if known := names.get(keyNameOf(cluster, namespace, best.secret)); known != "" {
			// The name this Secret answered to last time, asked first.
			keys = append([]string{known}, without(envFromCandidates, known)...)
		}
	}
	for _, key := range keys {
		value, err := h.Reveal(r, cluster, namespace, best.secret, key)
		switch {
		case err == nil:
			if best.key == "" {
				names.set(keyNameOf(cluster, namespace, best.secret), key)
			}
			return Credential{
				Secret: best.secret,
				Key:    key,
				Source: best.source,
				Pod:    pod.Name,
				// Trimmed of the trailing newline `kubectl create secret
				// --from-file` leaves, which would cause WRONGPASS.
				password: strings.TrimRight(value, "\r\n"),
			}, nil
		case errors.Is(err, extension.ErrNoSuchKey) && best.key == "":
			if names.get(keyNameOf(cluster, namespace, best.secret)) == key {
				names.set(keyNameOf(cluster, namespace, best.secret), "")
			}
			continue
		case errors.Is(err, extension.ErrNoSuchSecret), errors.Is(err, extension.ErrNoSuchKey):
			return Credential{}, fmt.Errorf("%s names Secret %s, key %s, which is not there: %w",
				best.source, best.secret, key, ErrNoCredential)
		default:
			return Credential{}, fmt.Errorf("reading Secret %s/%s for %s: %w", namespace, best.secret, best.source, err)
		}
	}
	return Credential{}, fmt.Errorf("Secret %s holds no key named like a password: %w", best.secret, ErrNoCredential)
}

// envFromCandidates are the keys asked for in a Secret handed over whole, by
// rank as rankVariable scores them and, within a rank, the commonest spelling
// first, because every ask is an audited Host.Reveal: the common
// `redis-password` costs one record. keyNames then remembers which name a
// Secret answered to.
//
// Only names that are themselves a password, so `redis-host` is never read. A
// freely named key (`my-cache-passwd`) is not found: finding it would mean
// listing the Secret, which hands over every value.
var envFromCandidates = []string{
	// 40: the server's own.
	"redis-password", "REDIS_PASSWORD", "valkey-password", "VALKEY_PASSWORD",
	"redis_password", "valkey_password", "REDISCLI_AUTH", "REDIS_AUTH", "REQUIREPASS",
	// 30: a replica's copy of the master's.
	"redis-master-password", "REDIS_MASTER_PASSWORD", "valkey-master-password",
	"VALKEY_MASTER_PASSWORD", "MASTER_PASSWORD",
	// 10: a hand-written Deployment's.
	"password", "PASSWORD", "PASS",
}

// keyNames remembers, per Secret, which candidate name answered (a name, never
// a value), so an `envFrom` Secret's walk of audited reveals happens once per
// console. Past maxKeyNames it starts again empty, which costs only a walk.
type keyNames struct {
	mu    sync.Mutex
	names map[string]string
}

const maxKeyNames = 512

func newKeyNames() *keyNames { return &keyNames{names: map[string]string{}} }

func keyNameOf(cluster, namespace, secret string) string {
	return cluster + "\x00" + namespace + "\x00" + secret
}

func (k *keyNames) get(id string) string {
	if k == nil {
		return ""
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.names[id]
}

func (k *keyNames) set(id, name string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if name == "" {
		delete(k.names, id)
		return
	}
	if len(k.names) >= maxKeyNames {
		k.names = map[string]string{}
	}
	k.names[id] = name
}

func without(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if item != drop {
			out = append(out, item)
		}
	}
	return out
}

// ranked is one candidate reference and how good a match its variable name is.
type ranked struct {
	rank   int
	secret string
	// key is empty for an `envFrom.secretRef`, where the key is decided from the
	// Secret's own contents instead.
	key    string
	source string
}

// bestInPod is the highest-ranked password reference in one pod.
func bestInPod(pod *corev1.Pod) ranked {
	best := ranked{rank: -1}
	containers := make([]corev1.Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	containers = append(containers, pod.Spec.Containers...)
	// Init containers too: a chart may template `redis.conf` in one, so the
	// reference is only there. Listed after the server's own, so an equal
	// rank in the server container wins.
	containers = append(containers, pod.Spec.InitContainers...)

	for i := range containers {
		container := &containers[i]
		for _, env := range container.Env {
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil || env.ValueFrom.SecretKeyRef.Name == "" {
				continue
			}
			rank := rankVariable(env.Name)
			if rank <= best.rank {
				continue
			}
			best = ranked{
				rank:   rank,
				secret: env.ValueFrom.SecretKeyRef.Name,
				key:    env.ValueFrom.SecretKeyRef.Key,
				source: fmt.Sprintf("the %s variable of container %s in pod %s",
					env.Name, container.Name, pod.Name),
			}
		}
		for _, env := range container.Env {
			// `REDIS_PASSWORD_FILE=...`: the password is a file mounted from a
			// Secret (Bitnami's default, `usePasswordFiles`). Ranked as the
			// variable it stands for, so the sentinel's and the exporter's files
			// are refused as their variables are.
			if env.ValueFrom != nil || env.Value == "" || !strings.HasSuffix(strings.ToUpper(env.Name), "_FILE") {
				continue
			}
			rank := rankVariable(strings.TrimSuffix(strings.ToUpper(env.Name), "_FILE"))
			if rank <= best.rank {
				continue
			}
			secret, key, ok := mountedSecretKey(pod, container, env.Value)
			if !ok {
				continue
			}
			best = ranked{
				rank:   rank,
				secret: secret,
				key:    key,
				source: fmt.Sprintf("the file %s that the %s variable of container %s in pod %s names",
					env.Value, env.Name, container.Name, pod.Name),
			}
		}
		for _, from := range container.EnvFrom {
			if from.SecretRef == nil || from.SecretRef.Name == "" {
				continue
			}
			// Ranked at zero, below every named variable: a key picked from a
			// whole environment by name is a weaker signal.
			if best.rank >= 0 {
				continue
			}
			best = ranked{
				rank:   0,
				secret: from.SecretRef.Name,
				source: fmt.Sprintf("the environment container %s in pod %s takes from Secret %s",
					container.Name, pod.Name, from.SecretRef.Name),
			}
		}
	}
	return best
}

// mountedSecretKey answers which key of which Secret the file at path is, in
// container: the volume mounted over that path must be a `secret` volume of
// the pod, and the file its `items` maps to a key - or, with no `items`, the
// key named like the file. Anything else (a projected volume, a ConfigMap, a
// subdirectory, an unmounted path) answers false: a guess would be an audited
// read of the wrong thing.
func mountedSecretKey(pod *corev1.Pod, container *corev1.Container, path string) (secret, key string, ok bool) {
	for _, mount := range container.VolumeMounts {
		var file string
		switch {
		case mount.SubPath != "":
			if path != mount.MountPath {
				continue
			}
			file = mount.SubPath
		default:
			dir := strings.TrimSuffix(mount.MountPath, "/") + "/"
			if !strings.HasPrefix(path, dir) {
				continue
			}
			file = strings.TrimPrefix(path, dir)
		}
		if file == "" || strings.Contains(file, "/") {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.Name != mount.Name || volume.Secret == nil || volume.Secret.SecretName == "" {
				continue
			}
			if len(volume.Secret.Items) == 0 {
				return volume.Secret.SecretName, file, true
			}
			for _, item := range volume.Secret.Items {
				if item.Path == file {
					return volume.Secret.SecretName, item.Key, true
				}
			}
		}
	}
	return "", "", false
}

// rankVariable scores an environment variable name as "this is the password the
// server itself is using", or refuses it outright.
//
// A ranking, not a first match: one container may hold `REDIS_PASSWORD` and
// `REDIS_SENTINEL_PASSWORD`, different credentials, and the highest rank has
// to win wherever it sits.
//
// A negative score is a refusal, not a low match, which keeps out an
// exporter's `REDIS_EXPORTER_PASSWORD` that any substring rule would accept.
func rankVariable(name string) int {
	upper := strings.ToUpper(strings.TrimSpace(name))

	// Refusals first, and by phrase rather than by word. These are credentials
	// for something else that happens to live in the same pod.
	for _, wrong := range []string{"SENTINEL", "EXPORTER", "METRICS", "PROMETHEUS"} {
		if strings.Contains(upper, wrong) {
			return -1
		}
	}
	// Not a password at all. `REDIS_PASSWORD_FILE` names a path.
	if strings.HasSuffix(upper, "_FILE") {
		return -1
	}

	switch upper {
	case "REDIS_PASSWORD", "VALKEY_PASSWORD", "REDISCLI_AUTH", "REDIS_AUTH", "REQUIREPASS":
		// What the server itself is started with. Unambiguous.
		return 40
	case "REDIS_MASTER_PASSWORD", "VALKEY_MASTER_PASSWORD", "MASTER_PASSWORD":
		// A replica's copy of the master's password. The same value in every
		// chart that sets both, and the only one present on a replica-only pod.
		return 30
	case "PASSWORD", "PASS":
		// A hand-written Deployment. Right often enough to use and vague enough
		// to rank below anything that names the product.
		return 10
	}

	named := strings.Contains(upper, "REDIS") || strings.Contains(upper, "VALKEY")
	secret := strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "PASSWD") ||
		strings.HasSuffix(upper, "_AUTH")
	if named && secret {
		return 20
	}
	return -1
}
