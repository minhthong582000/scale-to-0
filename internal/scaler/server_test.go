package scaler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/minhthong582000/scale-to-0/internal/activity"
	pb "github.com/minhthong582000/scale-to-0/pkg/externalscaler"
)

func testServer(store *activity.Store) *Server {
	return NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func ref(md map[string]string) *pb.ScaledObjectRef {
	return &pb.ScaledObjectRef{Name: "webapp", Namespace: "demo", ScalerMetadata: md}
}

// traffic moves a workload from idle to active.
func traffic(store *activity.Store, key activity.Key) {
	store.Observe(key, "node-1", 0)
	store.Observe(key, "node-1", 10)
}

func TestIsActive(t *testing.T) {
	store := activity.New(time.Minute)
	srv := testServer(store)
	key := activity.Key{Namespace: "demo", Name: "webapp"}

	got, err := srv.IsActive(context.Background(), ref(nil))
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if got.GetResult() {
		t.Error("idle workload reported active")
	}

	traffic(store, key)

	got, err = srv.IsActive(context.Background(), ref(nil))
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if !got.GetResult() {
		t.Error("workload with traffic reported inactive")
	}
}

// The workload defaults to the ScaledObject's own namespace and name.
func TestIsActiveUsesExplicitWorkload(t *testing.T) {
	store := activity.New(time.Minute)
	srv := testServer(store)

	traffic(store, activity.Key{Namespace: "other", Name: "api"})

	got, err := srv.IsActive(context.Background(), ref(map[string]string{"workload": "other/api"}))
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if !got.GetResult() {
		t.Error("explicit workload was not resolved")
	}
}

func TestIsActiveRejectsBadMetadata(t *testing.T) {
	srv := testServer(activity.New(time.Minute))

	tests := map[string]map[string]string{
		"malformed workload": {"workload": "webapp"},
		"idleSeconds":        {"idleSeconds": "soon"},
		"negative idle":      {"idleSeconds": "-5"},
		"targetRPS":          {"targetRPS": "lots"},
		"zero target":        {"targetRPS": "0"},
	}

	for name, md := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := srv.IsActive(context.Background(), ref(md))
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestGetMetricSpec(t *testing.T) {
	srv := testServer(activity.New(time.Minute))

	got, err := srv.GetMetricSpec(context.Background(), ref(map[string]string{"targetRPS": "25"}))
	if err != nil {
		t.Fatalf("GetMetricSpec: %v", err)
	}
	specs := got.GetMetricSpecs()
	if len(specs) != 1 {
		t.Fatalf("metric specs = %d, want 1", len(specs))
	}
	if want := "envoy-rps-demo-webapp"; specs[0].GetMetricName() != want {
		t.Errorf("metric name = %q, want %q", specs[0].GetMetricName(), want)
	}
	if specs[0].GetTargetSizeFloat() != 25 {
		t.Errorf("targetSizeFloat = %v, want 25", specs[0].GetTargetSizeFloat())
	}
	if specs[0].GetTargetSize() != 25 {
		t.Errorf("targetSize = %v, want 25", specs[0].GetTargetSize())
	}
}

func TestGetMetricSpecDefaultTarget(t *testing.T) {
	srv := testServer(activity.New(time.Minute))

	got, err := srv.GetMetricSpec(context.Background(), ref(nil))
	if err != nil {
		t.Fatalf("GetMetricSpec: %v", err)
	}
	if got.GetMetricSpecs()[0].GetTargetSizeFloat() != DefaultTargetRPS {
		t.Errorf("targetSizeFloat = %v, want %v", got.GetMetricSpecs()[0].GetTargetSizeFloat(), DefaultTargetRPS)
	}
}

func TestGetMetrics(t *testing.T) {
	store := activity.New(10 * time.Second)
	srv := testServer(store)
	traffic(store, activity.Key{Namespace: "demo", Name: "webapp"})

	got, err := srv.GetMetrics(context.Background(), &pb.GetMetricsRequest{
		ScaledObjectRef: ref(nil),
		MetricName:      "envoy-rps-demo-webapp",
	})
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	values := got.GetMetricValues()
	if len(values) != 1 {
		t.Fatalf("metric values = %d, want 1", len(values))
	}
	if want := 1.0; values[0].GetMetricValueFloat() != want {
		t.Errorf("metricValueFloat = %v, want %v", values[0].GetMetricValueFloat(), want)
	}
	if want := "envoy-rps-demo-webapp"; values[0].GetMetricName() != want {
		t.Errorf("metric name = %q, want %q", values[0].GetMetricName(), want)
	}
}

func TestGetMetricsIdleWorkload(t *testing.T) {
	srv := testServer(activity.New(time.Minute))

	got, err := srv.GetMetrics(context.Background(), &pb.GetMetricsRequest{ScaledObjectRef: ref(nil)})
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if got.GetMetricValues()[0].GetMetricValueFloat() != 0 {
		t.Errorf("idle rate = %v, want 0", got.GetMetricValues()[0].GetMetricValueFloat())
	}
}

// captureStream records what the server pushes and lets the test end the call.
type captureStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan bool
}

func newCaptureStream(ctx context.Context) *captureStream {
	return &captureStream{ctx: ctx, sent: make(chan bool, 8)}
}

func (c *captureStream) Send(r *pb.IsActiveResponse) error {
	c.sent <- r.GetResult()
	return nil
}
func (c *captureStream) Context() context.Context     { return c.ctx }
func (c *captureStream) SetHeader(metadata.MD) error  { return nil }
func (c *captureStream) SendHeader(metadata.MD) error { return nil }
func (c *captureStream) SetTrailer(metadata.MD)       {}
func (c *captureStream) SendMsg(any) error            { return nil }
func (c *captureStream) RecvMsg(any) error            { return nil }

func recv(t *testing.T, ch <-chan bool) bool {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a stream message")
		return false
	}
}

// KEDA reopens the stream after any restart, so the current state must go out
// before any change is waited on.
func TestStreamIsActiveSendsInitialState(t *testing.T) {
	store := activity.New(time.Minute)
	srv := testServer(store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newCaptureStream(ctx)

	done := make(chan error, 1)
	go func() { done <- srv.StreamIsActive(ref(nil), stream) }()

	if recv(t, stream.sent) {
		t.Error("initial state = active, want inactive")
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("StreamIsActive: %v", err)
	}
}

func TestStreamIsActivePushesOnTraffic(t *testing.T) {
	store := activity.New(time.Minute)
	srv := testServer(store)
	key := activity.Key{Namespace: "demo", Name: "webapp"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newCaptureStream(ctx)

	done := make(chan error, 1)
	go func() { done <- srv.StreamIsActive(ref(nil), stream) }()

	if recv(t, stream.sent) {
		t.Fatal("initial state = active, want inactive")
	}

	traffic(store, key)

	if !recv(t, stream.sent) {
		t.Error("activation was not pushed after traffic")
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("StreamIsActive: %v", err)
	}
}

func TestStreamIsActiveRejectsBadMetadata(t *testing.T) {
	srv := testServer(activity.New(time.Minute))
	stream := newCaptureStream(context.Background())

	err := srv.StreamIsActive(ref(map[string]string{"workload": "nope"}), stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("error = %v, want InvalidArgument", err)
	}
}
