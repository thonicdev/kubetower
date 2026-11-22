package resources

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/apiraw"
)

// AnyObject reports whether q's kind holds at least one object - in namespace,
// or anywhere where namespace is "" - for the kinds column's occupancy.
// Called only inside a read door: it reviews nobody.
//
// One row, metadata only, and not Cached(): some API servers ignore the
// limit when serving from the watch cache, so a quorum read of one row is
// cheaper. Metadata only, so a Secret's values never leave the API server;
// an answer that is not metadata is refused, as in secretNamespaces.
func AnyObject(ctx context.Context, clients kubernetes.Interface, q Question, namespace string) (bool, error) {
	raw, err := apiraw.Do(ctx, clients.CoreV1().RESTClient().Get().
		AbsPath(objectsPath(q, namespace)...).
		Param("limit", "1").
		SetHeader("Accept", metadataOnly))
	if err != nil {
		return false, err
	}
	var page metav1.PartialObjectMetadataList
	if err := json.Unmarshal(raw, &page); err != nil {
		return false, fmt.Errorf("the metadata-only list of %s did not parse: %w", q.Attrs.Resource, err)
	}
	if page.Kind != "PartialObjectMetadataList" {
		return false, fmt.Errorf("the API server answered a metadata-only list of %s with %q, not their metadata",
			q.Attrs.Resource, page.Kind)
	}
	return len(page.Items) > 0, nil
}

// objectsPath is the list's address: `/api/v1` for the core group, else
// `/apis/{group}/{version}`, then the namespace where one is asked of a
// namespaced kind, then the resource. Not escaped here: rest.Request escapes
// the path it is handed, and every segment is a DNS name or a version.
func objectsPath(q Question, namespace string) []string {
	a := q.Attrs
	segments := []string{"/apis", a.Group, a.Version}
	if a.Group == "" {
		segments = []string{"/api", a.Version}
	}
	if namespace != "" && q.Namespaced {
		segments = append(segments, "namespaces", namespace)
	}
	return append(segments, a.Resource)
}
