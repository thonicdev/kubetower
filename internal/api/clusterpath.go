package api

import "net/url"

// ClusterSegment is a context name as one path segment: what `{cluster}` takes
// in this API's routes and in the console's pages.
//
// A context name may hold `/` (an EKS ARN), so it is percent-encoded, and
// net/http decodes it exactly once on the way in, as `API.cluster` reads it.
// Every address naming a cluster is built through this. The front end's
// `web/ui/src/clusters/clusterPath.ts` is held to the same answers by
// `clusterPath.cases.json`.
func ClusterSegment(name string) string {
	return url.PathEscape(name)
}

// ClusterPath is `/c/{cluster}`: a cluster's console page, and with "/api" in
// front, the prefix of every API operation about it.
func ClusterPath(name string) string {
	return "/c/" + ClusterSegment(name)
}
