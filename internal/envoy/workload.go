package envoy

import (
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"

	"github.com/minhthong582000/scale-to-0/internal/activity"
)

// Istio populates these on every proxy's bootstrap node metadata.
const (
	metaNamespace = "NAMESPACE"
	metaWorkload  = "WORKLOAD_NAME"
)

// WorkloadFromNode derives the scalable workload from a proxy's node identity.
//
// Identity comes from the node rather than from stat names: a sidecar reports
// its own workload, so no parsing of tag-mangled metric names is needed. Falls
// back to the node ID ("sidecar~ip~pod.namespace~domain") when Istio's metadata
// is absent, which happens for proxies not bootstrapped by istiod.
func WorkloadFromNode(node *corev3.Node) (activity.Key, bool) {
	if node == nil {
		return activity.Key{}, false
	}

	fields := node.GetMetadata().GetFields()
	ns := fields[metaNamespace].GetStringValue()
	name := fields[metaWorkload].GetStringValue()
	if ns != "" && name != "" {
		return activity.Key{Namespace: ns, Name: name}, true
	}

	return workloadFromID(node.GetId())
}

func workloadFromID(id string) (activity.Key, bool) {
	parts := strings.Split(id, "~")
	if len(parts) < 3 {
		return activity.Key{}, false
	}

	pod, ns, found := strings.Cut(parts[2], ".")
	if !found || pod == "" || ns == "" {
		return activity.Key{}, false
	}
	return activity.Key{Namespace: ns, Name: pod}, true
}
