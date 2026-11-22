package detail

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// object parses a manifest the way the API would hand it over: a bare
// map[string]any with no typing. Numbers arrive as float64 here and as int64
// from a live decode, which is exactly why num() reads both.
func object(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return &unstructured.Unstructured{Object: raw}
}

// fakeResolver stands in for the cluster's REST mapper. Node is cluster-scoped
// on purpose: it is the case that must not come back with a namespace.
func fakeResolver(_ string, kind string) (string, bool, bool) {
	switch kind {
	case "ReplicaSet":
		return "replicasets", true, true
	case "StatefulSet":
		return "statefulsets", true, true
	case "Node":
		return "nodes", false, true
	case "VolumeSnapshot":
		return "volumesnapshots", true, true
	}
	return "", false, false
}

func relate(t *testing.T, resource, doc string) ([]Relation, string, []Port, []URL) {
	t.Helper()
	set, ports, urls := readRelations(object(t, doc), resource, fakeResolver)
	relations, note := set.finish()
	return relations, note, ports, urls
}

func find(t *testing.T, relations []Relation, role, label string) Relation {
	t.Helper()
	for _, rel := range relations {
		if rel.Role == role && rel.Label == label {
			return rel
		}
	}
	t.Fatalf("no %q relation labeled %q; got %s", role, label, describe(relations))
	return Relation{}
}

func absent(t *testing.T, relations []Relation, role, label string) {
	t.Helper()
	for _, rel := range relations {
		if rel.Role == role && rel.Label == label {
			t.Fatalf("%q/%q should not be listed; got %s", role, label, describe(relations))
		}
	}
}

func describe(relations []Relation) string {
	var out []string
	for _, rel := range relations {
		out = append(out, rel.Role+"/"+rel.Label)
	}
	return strings.Join(out, ", ")
}

// A pod, with one of every volume shape that matters. Modeled on what
// docker-desktop and MI-K3S actually serve: every pod there carries a
// `kube-api-access-*` projected volume nesting a configMap, and that is the
// case a fixture-only extractor gets wrong.
const podManifest = `
apiVersion: v1
kind: Pod
metadata:
  name: api-7d9f
  namespace: shop
  ownerReferences:
    - apiVersion: apps/v1
      kind: ReplicaSet
      name: api-7d9f
      controller: true
spec:
  nodeName: worker-3
  serviceAccountName: api
  priorityClassName: high
  imagePullSecrets:
    - name: registry-creds
  containers:
    - name: api
      ports:
        - name: http
          containerPort: 8080
        - containerPort: 9090
          protocol: UDP
          hostPort: 9090
      volumeMounts:
        - {name: config, mountPath: /etc/api}
        - {name: creds, mountPath: /etc/creds}
        - {name: scratch, mountPath: /tmp}
        - {name: kube-api-access-abcde, mountPath: /var/run/secrets/kubernetes.io/serviceaccount}
    - name: sidecar
      volumeMounts:
        - {name: config, mountPath: /shared/etc/api}
  initContainers:
    - name: migrate
      ports:
        - {name: admin, containerPort: 7000}
      volumeMounts:
        - {name: data, mountPath: /var/lib/api}
  volumes:
    - name: config
      configMap: {name: api-config}
    - name: creds
      secret: {secretName: api-creds, optional: true}
    - name: data
      persistentVolumeClaim: {claimName: api-data}
    - name: scratch
      emptyDir: {medium: Memory, sizeLimit: 64Mi}
    - name: logs
      hostPath: {path: /var/log, type: Directory}
    - name: kube-api-access-abcde
      projected:
        sources:
          - serviceAccountToken: {path: token}
          - configMap: {name: kube-root-ca.crt}
          - secret: {name: extra-ca}
          - downwardAPI: {}
    - name: cache
      ephemeral:
        volumeClaimTemplate: {spec: {}}
    - name: exotic
      flexVolume: {driver: acme/whatever}
`

func TestPodRelations(t *testing.T) {
	relations, note, ports, urls := relate(t, "pods", podManifest)
	if note != "" {
		t.Errorf("nothing should have been left out: %q", note)
	}
	if len(urls) != 0 {
		t.Errorf("a pod has no addresses, got %v", urls)
	}

	owner := find(t, relations, RoleControlledBy, "ReplicaSet/api-7d9f")
	if owner.Kind != "replicasets" || owner.Namespace != "shop" || !owner.Linkable() {
		t.Errorf("owner: %+v", owner)
	}

	// Cluster-scoped targets, and the whole point: a namespace
	// here reaches `kubectl -n` and comes back empty for something that exists.
	for _, cluster := range []struct{ role, label, kind string }{
		{RoleNode, "worker-3", "nodes"},
		{RolePriorityClass, "high", "priorityclasses"},
	} {
		rel := find(t, relations, cluster.role, cluster.label)
		if rel.Kind != cluster.kind {
			t.Errorf("%s: kind %q", cluster.label, rel.Kind)
		}
		if rel.Namespace != "" {
			t.Errorf("%s is cluster-scoped and was given namespace %q", cluster.label, rel.Namespace)
		}
	}

	sa := find(t, relations, RoleServiceAccount, "api")
	if sa.Kind != "serviceaccounts" || sa.Namespace != "shop" {
		t.Errorf("service account: %+v", sa)
	}

	config := find(t, relations, RoleConfig, "api-config")
	if config.Kind != "configmaps" || config.Namespace != "shop" {
		t.Errorf("configMap: %+v", config)
	}
	// Mounted in two containers, listed once, with both paths.
	if got := strings.Join(config.Mounts, " "); got != "/etc/api /shared/etc/api" {
		t.Errorf("configMap mounts: %q", got)
	}

	secret := find(t, relations, RoleSecret, "api-creds")
	if secret.Kind != "secrets" || len(secret.Mounts) != 1 || secret.Mounts[0] != "/etc/creds" {
		t.Errorf("secret: %+v", secret)
	}
	if !strings.Contains(secret.Detail, "optional") {
		t.Errorf("an optional secret should say so: %q", secret.Detail)
	}

	claim := find(t, relations, RoleVolumeClaim, "api-data")
	if claim.Kind != "persistentvolumeclaims" || claim.Namespace != "shop" {
		t.Errorf("claim: %+v", claim)
	}

	pull := find(t, relations, RoleImagePullSecret, "registry-creds")
	if pull.Kind != "secrets" || pull.Namespace != "shop" {
		t.Errorf("image pull secret: %+v", pull)
	}

	// A projected volume is both: the volume, because that is what is at the
	// path, and the objects nested in it, because those are what opens.
	projected := find(t, relations, RoleVolume, "kube-api-access-abcde (projected)")
	if projected.Linkable() {
		t.Error("a projected volume is not an object")
	}
	if !strings.Contains(projected.Detail, "service account token") || !strings.Contains(projected.Detail, "downward API") {
		t.Errorf("projected detail: %q", projected.Detail)
	}
	nested := find(t, relations, RoleConfig, "kube-root-ca.crt")
	if nested.Kind != "configmaps" || nested.Mounts[0] != "/var/run/secrets/kubernetes.io/serviceaccount" {
		t.Errorf("nested configMap: %+v", nested)
	}
	if find(t, relations, RoleSecret, "extra-ca").Kind != "secrets" {
		t.Error("a secret projected into a volume must still be a secret")
	}

	// No object behind them, still listed: "what is at /tmp" is the same
	// question as "which secret is at /etc/creds".
	scratch := find(t, relations, RoleVolume, "scratch (emptyDir)")
	if scratch.Linkable() || !strings.Contains(scratch.Detail, "Memory") || !strings.Contains(scratch.Detail, "64Mi") {
		t.Errorf("emptyDir: %+v", scratch)
	}
	logs := find(t, relations, RoleVolume, "logs (hostPath)")
	if !strings.Contains(logs.Detail, "/var/log") {
		t.Errorf("hostPath: %+v", logs)
	}

	// The kubelet names a generic ephemeral volume's claim after the pod.
	ephemeral := find(t, relations, RoleVolumeClaim, "api-7d9f-cache")
	if ephemeral.Kind != "persistentvolumeclaims" || ephemeral.Namespace != "shop" {
		t.Errorf("ephemeral claim: %+v", ephemeral)
	}

	// A source nobody wrote a case for is named, never dropped.
	find(t, relations, RoleVolume, "exotic (flexVolume)")

	want := []Port{
		{Name: "http", Port: 8080, Protocol: "TCP", Container: "api"},
		{Port: 9090, HostPort: 9090, Protocol: "UDP", Container: "api"},
		{Name: "admin", Port: 7000, Protocol: "TCP", Container: "migrate"},
	}
	if len(ports) != len(want) {
		t.Fatalf("ports: got %+v", ports)
	}
	for i, port := range ports {
		if port != want[i] {
			t.Errorf("port %d: got %+v, want %+v", i, port, want[i])
		}
	}
}

// An owner of a kind this cluster does not serve is listed and not linked. It
// is usually a CRD whose definition has been removed, and the object is then
// stuck — which is exactly when the panel is being read.
func TestUnknownOwnerIsNotLinked(t *testing.T) {
	relations, _, _, _ := relate(t, "pods", `
apiVersion: v1
kind: Pod
metadata:
  name: orphan
  namespace: shop
  ownerReferences:
    - {apiVersion: acme.io/v1, kind: Widget, name: w1, controller: true}
spec: {}
`)
	owner := find(t, relations, RoleControlledBy, "Widget/w1")
	if owner.Linkable() {
		t.Errorf("a kind the cluster does not serve must not be a link: %+v", owner)
	}
	if !strings.Contains(owner.Detail, "acme.io/v1") {
		t.Errorf("say why it is not a link: %q", owner.Detail)
	}
}

// A cluster-scoped owner keeps no namespace, even though the object has one.
func TestClusterScopedOwnerHasNoNamespace(t *testing.T) {
	relations, _, _, _ := relate(t, "pods", `
apiVersion: v1
kind: Pod
metadata:
  name: p
  namespace: shop
  ownerReferences:
    - {apiVersion: v1, kind: Node, name: worker-3}
spec: {}
`)
	owner := find(t, relations, RoleOwnedBy, "Node/worker-3")
	if owner.Kind != "nodes" || owner.Namespace != "" {
		t.Errorf("a Node owner is cluster-scoped: %+v", owner)
	}
}

// The generic pass runs for a kind nobody has heard of, because that is the
// kind whose panel is otherwise empty exactly when it is being looked at.
func TestGenericPassCoversAnUnknownKind(t *testing.T) {
	relations, _, _, _ := relate(t, "widgets", `
apiVersion: acme.io/v1
kind: Widget
metadata: {name: w1, namespace: shop}
spec:
  nodeName: worker-3
  serviceAccountName: widget
`)
	if find(t, relations, RoleNode, "worker-3").Kind != "nodes" {
		t.Error("a CRD with a nodeName gets its node")
	}
	if find(t, relations, RoleServiceAccount, "widget").Namespace != "shop" {
		t.Error("a CRD with a serviceAccountName gets its account")
	}
}

func TestDeploymentReadsItsTemplate(t *testing.T) {
	relations, _, ports, _ := relate(t, "deployments", `
apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop}
spec:
  selector:
    matchLabels: {app: api, tier: web}
  template:
    spec:
      serviceAccountName: api
      containers:
        - name: api
          ports: [{containerPort: 8080}]
          volumeMounts: [{name: config, mountPath: /etc/api}]
      volumes:
        - name: config
          configMap: {name: api-config}
`)
	if find(t, relations, RoleServiceAccount, "api").Namespace != "shop" {
		t.Error("the template's service account")
	}
	if find(t, relations, RoleConfig, "api-config").Mounts[0] != "/etc/api" {
		t.Error("the template's volumes, with their mount paths")
	}

	selects := find(t, relations, RoleSelects, "app=api,tier=web")
	if selects.Kind != resPods || selects.Namespace != "shop" || selects.Selector != "app=api,tier=web" {
		t.Errorf("selector: %+v", selects)
	}
	if selects.Linkable() {
		t.Error("a selector is not one object")
	}

	// Ports are for the things a port-forward can reach. A Deployment's
	// template ports have no pod behind them.
	if len(ports) != 0 {
		t.Errorf("a Deployment exposes no forwardable port, got %+v", ports)
	}
}

// A StatefulSet writes a placeholder claimName in its template and the
// controller overwrites it. ECK writes the literal "claim-name-placeholder";
// linking it opens a PVC that has never existed.
func TestStatefulSetPerReplicaClaims(t *testing.T) {
	relations, _, _, _ := relate(t, "statefulsets", `
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: es, namespace: eck}
spec:
  selector:
    matchLabels: {app: es}
  volumeClaimTemplates:
    - metadata: {name: data}
      spec: {storageClassName: fast}
  template:
    spec:
      containers:
        - name: es
          volumeMounts: [{name: data, mountPath: /usr/share/elasticsearch/data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: claim-name-placeholder}
`)
	absent(t, relations, RoleVolumeClaim, "claim-name-placeholder")

	claim := find(t, relations, RoleVolumeClaim, "data (one per replica)")
	if claim.Linkable() {
		t.Error("the per-replica claims are not one object")
	}
	if !strings.Contains(claim.Detail, "data-es-0") {
		t.Errorf("spell out the names: %q", claim.Detail)
	}
	if len(claim.Mounts) != 1 || claim.Mounts[0] != "/usr/share/elasticsearch/data" {
		t.Errorf("the mount path belongs on the claim: %+v", claim.Mounts)
	}

	class := find(t, relations, RoleStorageClass, "fast")
	if class.Kind != "storageclasses" || class.Namespace != "" {
		t.Errorf("a storage class is cluster-scoped: %+v", class)
	}
}

func TestCronJobReadsItsNestedTemplate(t *testing.T) {
	relations, _, _, _ := relate(t, "cronjobs", `
apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly, namespace: shop}
spec:
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: batch
          containers:
            - name: run
              volumeMounts: [{name: out, mountPath: /data}]
          volumes:
            - name: out
              persistentVolumeClaim: {claimName: exports}
`)
	find(t, relations, RoleServiceAccount, "batch")
	if find(t, relations, RoleVolumeClaim, "exports").Mounts[0] != "/data" {
		t.Error("two templates deep, with the mount path")
	}
}

func TestServicePortsAndSelector(t *testing.T) {
	relations, _, ports, _ := relate(t, "services", `
apiVersion: v1
kind: Service
metadata: {name: api, namespace: shop}
spec:
  type: NodePort
  clusterIP: 10.0.0.9
  selector: {app: api}
  ports:
    - {name: http, port: 80, targetPort: web, nodePort: 31000}
    - {port: 53, protocol: UDP, targetPort: 5353}
`)
	selects := find(t, relations, RoleSelects, "app=api")
	if selects.Kind != resPods || selects.Namespace != "shop" {
		t.Errorf("selector: %+v", selects)
	}

	want := []Port{
		{Name: "http", Port: 80, TargetPort: "web", NodePort: 31000, Protocol: "TCP"},
		{Port: 53, TargetPort: "5353", Protocol: "UDP"},
	}
	if len(ports) != len(want) {
		t.Fatalf("ports: %+v", ports)
	}
	for i, port := range ports {
		if port != want[i] {
			t.Errorf("port %d: got %+v, want %+v", i, port, want[i])
		}
	}
}

// A Service with no selector has its endpoints written by something else, and
// saying so is the answer to "why does this Service have no pods".
func TestServiceWithoutSelector(t *testing.T) {
	relations, _, _, _ := relate(t, "services", `
apiVersion: v1
kind: Service
metadata: {name: kubernetes, namespace: default}
spec: {clusterIP: 10.0.0.1, ports: [{name: https, port: 443, targetPort: 6443}]}
`)
	none := find(t, relations, RoleSelects, "no selector")
	if none.Linkable() || none.Selector != "" {
		t.Errorf("no selector: %+v", none)
	}
}

func TestClaimAndVolumeAreClusterScoped(t *testing.T) {
	relations, _, _, _ := relate(t, "persistentvolumeclaims", `
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, namespace: shop}
spec:
  volumeName: pvc-1234
  storageClassName: longhorn
  dataSourceRef: {apiGroup: snapshot.storage.k8s.io, kind: VolumeSnapshot, name: nightly}
`)
	volume := find(t, relations, RolePersistentVolume, "pvc-1234")
	if volume.Kind != "persistentvolumes" || volume.Namespace != "" {
		t.Errorf("a PersistentVolume is cluster-scoped: %+v", volume)
	}
	if find(t, relations, RoleStorageClass, "longhorn").Namespace != "" {
		t.Error("a StorageClass is cluster-scoped")
	}
	source := find(t, relations, RoleDataSource, "VolumeSnapshot/nightly")
	if source.Kind != "volumesnapshots" || source.Namespace != "shop" {
		t.Errorf("a namespaced data source keeps the claim's namespace: %+v", source)
	}

	relations, _, _, _ = relate(t, "persistentvolumes", `
apiVersion: v1
kind: PersistentVolume
metadata: {name: pvc-1234}
spec:
  storageClassName: longhorn
  claimRef: {namespace: shop, name: data}
`)
	claim := find(t, relations, RoleVolumeClaim, "data")
	if claim.Kind != "persistentvolumeclaims" || claim.Namespace != "shop" {
		t.Errorf("the claim keeps its own namespace: %+v", claim)
	}
}

// The scheme. This is the one field that can send a person somewhere that does
// not answer, so every rule it follows has a case here.
func TestIngressScheme(t *testing.T) {
	cases := []struct {
		name    string
		tls     string
		host    string
		address string
		tlsFlag bool
	}{
		{
			name:    "no tls section at all is http",
			tls:     "",
			host:    "argocd.example.com",
			address: "http://argocd.example.com/",
		},
		{
			name:    "an exact host in spec.tls is https",
			tls:     "  tls: [{hosts: [app.example.com], secretName: app-cert}]\n",
			host:    "app.example.com",
			address: "https://app.example.com/",
			tlsFlag: true,
		},
		{
			name:    "a tls entry for another host leaves this one http",
			tls:     "  tls: [{hosts: [other.example.com], secretName: other-cert}]\n",
			host:    "app.example.com",
			address: "http://app.example.com/",
		},
		{
			name:    "a wildcard covers one label",
			tls:     "  tls: [{hosts: ['*.example.com'], secretName: star}]\n",
			host:    "app.example.com",
			address: "https://app.example.com/",
			tlsFlag: true,
		},
		{
			name:    "a wildcard does not cover two labels",
			tls:     "  tls: [{hosts: ['*.example.com'], secretName: star}]\n",
			host:    "a.b.example.com",
			address: "http://a.b.example.com/",
		},
		{
			name:    "a wildcard does not cover the bare domain",
			tls:     "  tls: [{hosts: ['*.example.com'], secretName: star}]\n",
			host:    "example.com",
			address: "http://example.com/",
		},
		{
			name:    "an entry with no hosts is the default certificate and covers everything",
			tls:     "  tls: [{secretName: default-cert}]\n",
			host:    "anything.example.com",
			address: "https://anything.example.com/",
			tlsFlag: true,
		},
		{
			// An Ingress may list a host under tls
			// with no secretName at all. The certificate comes from elsewhere;
			// the host is still served over TLS.
			name:    "a tls entry with a host and no secret still means https",
			tls:     "  tls: [{hosts: [docker.example.com]}]\n",
			host:    "docker.example.com",
			address: "https://docker.example.com/",
			tlsFlag: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := "apiVersion: networking.k8s.io/v1\nkind: Ingress\n" +
				"metadata: {name: i, namespace: shop}\nspec:\n" + c.tls +
				"  rules:\n    - host: " + c.host +
				"\n      http: {paths: [{path: /, backend: {service: {name: api, port: {number: 80}}}}]}\n"
			_, _, _, urls := relate(t, "ingresses", doc)
			if len(urls) != 1 {
				t.Fatalf("one address expected, got %+v", urls)
			}
			if urls[0].Address != c.address {
				t.Errorf("address: got %q, want %q", urls[0].Address, c.address)
			}
			if urls[0].TLS != c.tlsFlag {
				t.Errorf("TLS: got %v, want %v", urls[0].TLS, c.tlsFlag)
			}
		})
	}
}

// An annotation must never decide the scheme. `ssl-redirect` says what happens
// to an http request; it does not say a certificate exists, and reading it as
// https is how a link ends at a certificate error.
func TestIngressIgnoresRedirectAnnotations(t *testing.T) {
	_, _, _, urls := relate(t, "ingresses", `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: i
  namespace: shop
  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
spec:
  rules:
    - host: app.example.com
      http: {paths: [{path: /, backend: {service: {name: api, port: {number: 80}}}}]}
`)
	if urls[0].Address != "http://app.example.com/" {
		t.Errorf("an annotation is not a certificate: %q", urls[0].Address)
	}
}

func TestIngressRoutingAndPaths(t *testing.T) {
	relations, _, _, urls := relate(t, "ingresses", `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: dt, namespace: security}
spec:
  ingressClassName: traefik
  tls: [{hosts: [dt.example.com], secretName: dt-cert}]
  defaultBackend: {service: {name: frontend, port: {name: web}}}
  rules:
    - host: dt.example.com
      http:
        paths:
          - {path: /api, backend: {service: {name: api-server, port: {number: 8080}}}}
          - {path: '/health(/|$)(.*)', backend: {service: {name: api-server, port: {number: 8080}}}}
          - {backend: {service: {name: frontend, port: {name: web}}}}
`)
	class := find(t, relations, RoleIngressClass, "traefik")
	if class.Kind != "ingressclasses" || class.Namespace != "" {
		t.Errorf("an IngressClass is cluster-scoped: %+v", class)
	}
	cert := find(t, relations, RoleTLSCertificate, "dt-cert")
	if cert.Kind != "secrets" || cert.Namespace != "security" {
		t.Errorf("certificate: %+v", cert)
	}
	find(t, relations, RoleService, "api-server")
	if !strings.Contains(find(t, relations, RoleService, "frontend").Detail, "no rule matches") {
		t.Error("the default backend should say it is the default")
	}

	if len(urls) != 3 {
		t.Fatalf("three addresses expected, got %+v", urls)
	}
	if urls[0].Address != "https://dt.example.com/api" || urls[0].Service != "api-server" {
		t.Errorf("first: %+v", urls[0])
	}
	// A regular-expression path pasted into a browser is a 404. The literal
	// beginning is an address that works, and the note says the rest was cut.
	if urls[1].Address != "https://dt.example.com/health" {
		t.Errorf("pattern path: %q", urls[1].Address)
	}
	if !strings.Contains(urls[1].Note, "pattern") || !strings.Contains(urls[1].Note, "/health(/|$)(.*)") {
		t.Errorf("say the path was cut and show it: %q", urls[1].Note)
	}
	if urls[1].Path != "/health(/|$)(.*)" {
		t.Errorf("Path keeps what the object says: %q", urls[1].Path)
	}
	// An absent path is "/", which is what jenkins on BUILD-EKS-1 writes.
	if urls[2].Address != "https://dt.example.com/" {
		t.Errorf("absent path: %+v", urls[2])
	}
}

// A rule with no host answers on whatever address the controller published. If
// there is none, there is nothing to open, and a URL with no host is a broken
// link that looks like a feature.
func TestIngressWithoutHost(t *testing.T) {
	const doc = `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: i, namespace: shop}
spec:
  rules:
    - http: {paths: [{path: /, backend: {service: {name: api, port: {number: 80}}}}]}
%s`

	_, _, _, urls := relate(t, "ingresses", strings.Replace(doc, "%s", `
status:
  loadBalancer:
    ingress: [{hostname: lb-123.elb.amazonaws.com}]
`, 1))
	if len(urls) != 1 || urls[0].Address != "http://lb-123.elb.amazonaws.com/" {
		t.Fatalf("the published address: %+v", urls)
	}
	if !strings.Contains(urls[0].Note, "sets no host") {
		t.Errorf("say where the host came from: %q", urls[0].Note)
	}

	_, _, _, urls = relate(t, "ingresses", strings.Replace(doc, "%s", "", 1))
	if len(urls) != 0 {
		t.Errorf("no host and no published address is no address: %+v", urls)
	}
	// The routing is still reported even when nothing can be opened.
	relations, _, _, _ := relate(t, "ingresses", strings.Replace(doc, "%s", "", 1))
	find(t, relations, RoleService, "api")
}

func TestCapsSayWhatWasLeftOut(t *testing.T) {
	var volumes, mounts strings.Builder
	for i := 0; i < 60; i++ {
		name := "v" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		volumes.WriteString("    - {name: " + name + ", emptyDir: {}}\n")
		mounts.WriteString("        - {name: " + name + ", mountPath: /m/" + name + "}\n")
	}
	// One configMap mounted at more paths than a panel can show.
	volumes.WriteString("    - {name: shared, configMap: {name: big}}\n")
	for i := 0; i < 12; i++ {
		mounts.WriteString("        - {name: shared, mountPath: /s/" + string(rune('a'+i)) + "}\n")
	}

	doc := "apiVersion: v1\nkind: Pod\nmetadata: {name: p, namespace: shop}\nspec:\n" +
		"  containers:\n    - name: c\n      volumeMounts:\n" + mounts.String() +
		"  volumes:\n" + volumes.String()

	relations, note, _, _ := relate(t, "pods", doc)
	if len(relations) != maxRelations {
		t.Fatalf("the list should be capped at %d, got %d", maxRelations, len(relations))
	}
	if !strings.Contains(note, "21 more related objects are not shown") {
		t.Errorf("count what was cut: %q", note)
	}
	// The cap takes from the end of the reading order, so the configMap
	// survives and the emptyDirs are what goes.
	shared := find(t, relations, RoleConfig, "big")
	if len(shared.Mounts) != maxMounts+1 || shared.Mounts[maxMounts] != "+6 more" {
		t.Errorf("mounts should be capped and counted: %+v", shared.Mounts)
	}
}

func TestLiteralPath(t *testing.T) {
	cases := []struct {
		path, want string
		trimmed    bool
	}{
		{"", "/", false},
		{"/", "/", false},
		{"/api/v1", "/api/v1", false},
		{"/api(/|$)(.*)", "/api", true},
		{"(.*)", "/", true},
		{"/f.o", "/f.o", false}, // a dot is not worth cutting on
	}
	for _, c := range cases {
		got, trimmed := literalPath(c.path)
		if got != c.want || trimmed != c.trimmed {
			t.Errorf("literalPath(%q) = %q, %v; want %q, %v", c.path, got, trimmed, c.want, c.trimmed)
		}
	}
}

func TestPortValueReadsBothShapes(t *testing.T) {
	// targetPort is a number or the *name* of a container port, and a UI that
	// assumes the first forwards to port 0.
	if got := portValue("web"); got != "web" {
		t.Errorf("named targetPort: %q", got)
	}
	if got := portValue(int64(8080)); got != "8080" {
		t.Errorf("int64 targetPort: %q", got)
	}
	if got := portValue(float64(8080)); got != "8080" {
		t.Errorf("float64 targetPort: %q", got)
	}
	if got := portValue(nil); got != "" {
		t.Errorf("absent targetPort: %q", got)
	}
}

// A Service port's `appProtocol` is read, and it is read for one caller: the
// port-forward's link, which asks whether the thing on the other end speaks TLS.
// It is the only authoritative answer to that — a port number is a convention
// and a name is a habit — so losing it in this reader silently downgrades every
// declared https port to a guess.
//
// A container port has no appProtocol in the API, so a pod's ports carry "" and
// that is correct rather than missing.
func TestServicePortsCarryAppProtocol(t *testing.T) {
	_, _, ports, _ := relate(t, "services", `
apiVersion: v1
kind: Service
metadata: {name: controller, namespace: ingress-nginx}
spec:
  clusterIP: 10.0.0.9
  ports:
  - {name: https, port: 443, targetPort: https, appProtocol: https}
  - {name: http, port: 80, targetPort: http}
`)
	if len(ports) != 2 {
		t.Fatalf("ports: got %+v", ports)
	}
	if ports[0].AppProtocol != "https" {
		t.Errorf("the declared appProtocol is %q, want %q", ports[0].AppProtocol, "https")
	}
	if ports[1].AppProtocol != "" {
		t.Errorf("a port declaring nothing came back with appProtocol %q; "+
			"an invented one is worse than none, since it wins over the number",
			ports[1].AppProtocol)
	}
}

func TestPodPortsHaveNoAppProtocol(t *testing.T) {
	_, _, ports, _ := relate(t, "pods", `
apiVersion: v1
kind: Pod
metadata: {name: api-0, namespace: default}
spec:
  containers:
  - name: api
    ports: [{name: https, containerPort: 8443}]
`)
	if len(ports) != 1 {
		t.Fatalf("ports: got %+v", ports)
	}
	if ports[0].AppProtocol != "" {
		t.Errorf("a container port came back with appProtocol %q; the API has no such "+
			"field on one, so anything here is invented", ports[0].AppProtocol)
	}
}
