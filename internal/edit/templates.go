package edit

// The documents the list's "+" opens.
//
// A template is a starting point the person edits, never sent as is: it goes
// through PreviewCreate like any document. The written ones are small and run
// something real; every other kind, custom resources included, gets the
// apiVersion, kind and metadata skeleton.
//
// The head is marshalled, never concatenated: apiVersion and kind come from
// the cluster's discovery and the namespace from the request (held to a DNS
// label), written by the YAML encoder so no value can open a new key. The
// bodies are constants of this file.

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

// templateName is the placeholder a written body uses for the object's name,
// replaced by a name derived from the kind, never by anything a request
// carried.
const templateName = "{{name}}"

// templateBodies are the written templates, keyed by API group and kind, and
// holding everything after metadata.
var templateBodies = map[string]string{
	"apps/Deployment": `spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{name}}
  template:
    metadata:
      labels:
        app: {{name}}
    spec:
      containers:
        - name: app
          image: nginx:1.27
          ports:
            - containerPort: 80
`,
	"apps/StatefulSet": `spec:
  serviceName: {{name}}
  replicas: 1
  selector:
    matchLabels:
      app: {{name}}
  template:
    metadata:
      labels:
        app: {{name}}
    spec:
      containers:
        - name: app
          image: nginx:1.27
          ports:
            - containerPort: 80
`,
	"apps/DaemonSet": `spec:
  selector:
    matchLabels:
      app: {{name}}
  template:
    metadata:
      labels:
        app: {{name}}
    spec:
      containers:
        - name: app
          image: busybox:1.36
          command: ["sh", "-c", "while true; do sleep 3600; done"]
`,
	"/Service": `spec:
  selector:
    app: example-deployment
  ports:
    - name: http
      port: 80
      targetPort: 80
`,
	"/ConfigMap": `data:
  example.key: example value
`,
	// stringData, so the person types the value in clear and the API server
	// encodes it; and a placeholder, never a real secret.
	"/Secret": `type: Opaque
stringData:
  username: change-me
  password: change-me
`,
	"batch/Job": `spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: job
          image: busybox:1.36
          command: ["sh", "-c", "echo hello"]
`,
	"batch/CronJob": `spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      backoffLimit: 0
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: job
              image: busybox:1.36
              command: ["sh", "-c", "echo hello"]
`,
	"/Pod": `spec:
  containers:
    - name: app
      image: nginx:1.27
      ports:
        - containerPort: 80
`,
	"networking.k8s.io/Ingress": `spec:
  rules:
    - host: example.local
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: example-service
                port:
                  number: 80
`,
	"/PersistentVolumeClaim": `spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
`,
	// The three that point at something point at the Deployment template's
	// object, as the Service does: created in that order, they work.
	"autoscaling/HorizontalPodAutoscaler": `spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: example-deployment
  minReplicas: 1
  maxReplicas: 3
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 80
`,
	// Read-only on pods: the template grants nothing a person would regret
	// creating unread.
	"rbac.authorization.k8s.io/Role": `rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
`,
	// Selects one workload and lets in its own namespace, so creating it as it
	// stands cuts nothing but what reached that workload from elsewhere.
	"networking.k8s.io/NetworkPolicy": `spec:
  podSelector:
    matchLabels:
      app: example-deployment
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector: {}
`,
	"/ServiceAccount": ``,
	"/Namespace":      ``,
}

// Template is the starting document for a new object of the kind at where,
// filled with namespace where the kind is namespaced, and whether it is the
// generic skeleton. A namespace is ignored on a cluster-scoped kind and may be
// empty on a namespaced one, which leaves metadata.namespace for the person.
func Template(where Location, namespace string) (document string, generic bool, err error) {
	if namespace != "" {
		if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
			return "", false, refuse(RefusedInput, "%q is not a namespace name: %s", namespace, problems[0])
		}
	}
	if where.Kind == "" || where.Resource.Version == "" {
		return "", false, refuse(RefusedInput, "this kind has no apiVersion or kind to start a document from")
	}
	name := "example-" + strings.ToLower(where.Kind)
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		// A lower-cased kind too long for a DNS name: the person names it.
		name = "example"
	}

	type metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
	}
	type head struct {
		APIVersion string   `json:"apiVersion"`
		Kind       string   `json:"kind"`
		Metadata   metadata `json:"metadata"`
	}
	h := head{APIVersion: APIVersionOf(where), Kind: where.Kind, Metadata: metadata{Name: name}}
	if where.Namespaced {
		h.Metadata.Namespace = namespace
	}
	out, err := yaml.Marshal(h)
	if err != nil {
		return "", false, err
	}
	body, written := templateBodies[where.Resource.Group+"/"+where.Kind]
	if !written {
		return string(out), true, nil
	}
	return string(out) + strings.ReplaceAll(body, templateName, name), false, nil
}
