package envoy

import (
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/minhthong582000/scale-to-0/internal/activity"
)

func node(id string, meta map[string]string) *corev3.Node {
	n := &corev3.Node{Id: id}
	if meta != nil {
		fields := make(map[string]*structpb.Value, len(meta))
		for k, v := range meta {
			fields[k] = structpb.NewStringValue(v)
		}
		n.Metadata = &structpb.Struct{Fields: fields}
	}
	return n
}

func TestWorkloadFromNode(t *testing.T) {
	tests := []struct {
		name string
		node *corev3.Node
		want activity.Key
		ok   bool
	}{
		{
			name: "istio metadata",
			node: node("sidecar~10.244.0.5~webapp-5b5f-nvq7v.demo~demo.svc.cluster.local",
				map[string]string{"NAMESPACE": "demo", "WORKLOAD_NAME": "webapp"}),
			want: activity.Key{Namespace: "demo", Name: "webapp"},
			ok:   true,
		},
		{
			name: "falls back to the node id",
			node: node("sidecar~10.244.0.5~webapp-5b5f-nvq7v.demo~demo.svc.cluster.local", nil),
			want: activity.Key{Namespace: "demo", Name: "webapp-5b5f-nvq7v"},
			ok:   true,
		},
		{
			name: "partial metadata falls back",
			node: node("sidecar~10.244.0.5~webapp-5b5f-nvq7v.demo~demo.svc.cluster.local",
				map[string]string{"NAMESPACE": "demo"}),
			want: activity.Key{Namespace: "demo", Name: "webapp-5b5f-nvq7v"},
			ok:   true,
		},
		{
			name: "unusable id",
			node: node("router~10.0.0.1", nil),
			ok:   false,
		},
		{
			name: "id without a namespace",
			node: node("sidecar~10.0.0.1~justapod~cluster.local", nil),
			ok:   false,
		},
		{
			name: "nil node",
			node: nil,
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := WorkloadFromNode(tt.node)
			if ok != tt.ok || got != tt.want {
				t.Errorf("WorkloadFromNode() = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
