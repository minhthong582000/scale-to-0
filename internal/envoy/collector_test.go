package envoy

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"testing"
	"time"

	metricsv3 "github.com/envoyproxy/go-control-plane/envoy/service/metrics/v3"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/minhthong582000/scale-to-0/internal/activity"
)

// fakeStream replays a fixed set of messages, then EOF.
type fakeStream struct {
	grpc.ServerStream
	msgs []*metricsv3.StreamMetricsMessage
	i    int
}

func (f *fakeStream) Recv() (*metricsv3.StreamMetricsMessage, error) {
	if f.i >= len(f.msgs) {
		return nil, io.EOF
	}
	msg := f.msgs[f.i]
	f.i++
	return msg, nil
}

func (f *fakeStream) SendAndClose(*metricsv3.StreamMetricsResponse) error { return nil }
func (f *fakeStream) Context() context.Context                            { return context.Background() }
func (f *fakeStream) SetHeader(metadata.MD) error                         { return nil }
func (f *fakeStream) SendHeader(metadata.MD) error                        { return nil }
func (f *fakeStream) SetTrailer(metadata.MD)                              {}
func (f *fakeStream) SendMsg(any) error                                   { return nil }
func (f *fakeStream) RecvMsg(any) error                                   { return nil }

func counter(name string, value float64) *dto.MetricFamily {
	return &dto.MetricFamily{
		Name:   &name,
		Metric: []*dto.Metric{{Counter: &dto.Counter{Value: &value}}},
	}
}

func identifier() *metricsv3.StreamMetricsMessage_Identifier {
	return &metricsv3.StreamMetricsMessage_Identifier{
		Node: node("sidecar~10.244.0.5~webapp-abc.demo~demo.svc.cluster.local",
			map[string]string{"NAMESPACE": "demo", "WORKLOAD_NAME": "webapp"}),
	}
}

func testCollector(store *activity.Store) *Collector {
	return NewCollector(store, regexp.MustCompile(DefaultRequestPattern),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestStreamMetricsRecordsRequests(t *testing.T) {
	store := activity.New(time.Minute)
	key := activity.Key{Namespace: "demo", Name: "webapp"}

	stream := &fakeStream{msgs: []*metricsv3.StreamMetricsMessage{
		{
			Identifier:   identifier(),
			EnvoyMetrics: []*dto.MetricFamily{counter("istio_requests_total", 10)},
		},
		// The identifier is only sent once, so later messages carry none.
		{EnvoyMetrics: []*dto.MetricFamily{counter("istio_requests_total", 14)}},
	}}

	if err := testCollector(store).StreamMetrics(stream); err != nil {
		t.Fatalf("StreamMetrics: %v", err)
	}

	if !store.Active(key, time.Minute) {
		t.Error("workload is not active after the stream reported requests")
	}
	if got, want := store.RequestsPerSecond(key), 4.0/60.0; got != want {
		t.Errorf("RequestsPerSecond = %v, want %v", got, want)
	}
}

func TestStreamMetricsIgnoresUnmatchedCounters(t *testing.T) {
	store := activity.New(time.Minute)
	key := activity.Key{Namespace: "demo", Name: "webapp"}

	stream := &fakeStream{msgs: []*metricsv3.StreamMetricsMessage{
		{Identifier: identifier(), EnvoyMetrics: []*dto.MetricFamily{counter("cluster.upstream_cx_total", 5)}},
		{EnvoyMetrics: []*dto.MetricFamily{counter("cluster.upstream_cx_total", 90)}},
	}}

	if err := testCollector(store).StreamMetrics(stream); err != nil {
		t.Fatalf("StreamMetrics: %v", err)
	}
	if store.Active(key, time.Minute) {
		t.Error("connection counters were treated as requests")
	}
}

// Without an identity there is no workload to credit, so the batch is dropped
// rather than attributed to the wrong workload.
func TestStreamMetricsSkipsMessagesBeforeIdentity(t *testing.T) {
	store := activity.New(time.Minute)

	stream := &fakeStream{msgs: []*metricsv3.StreamMetricsMessage{
		{EnvoyMetrics: []*dto.MetricFamily{counter("istio_requests_total", 10)}},
		{EnvoyMetrics: []*dto.MetricFamily{counter("istio_requests_total", 20)}},
	}}

	if err := testCollector(store).StreamMetrics(stream); err != nil {
		t.Fatalf("StreamMetrics: %v", err)
	}
	if _, ok := store.LastRequest(activity.Key{Namespace: "demo", Name: "webapp"}); ok {
		t.Error("metrics were recorded without a node identity")
	}
}

func TestStreamMetricsSumsMatchingFamilies(t *testing.T) {
	store := activity.New(time.Minute)
	key := activity.Key{Namespace: "demo", Name: "webapp"}

	stream := &fakeStream{msgs: []*metricsv3.StreamMetricsMessage{
		{Identifier: identifier(), EnvoyMetrics: []*dto.MetricFamily{
			counter("istio_requests_total", 0),
			counter("http.inbound_0.0.0.0_9898.downstream_rq_total", 0),
		}},
		{EnvoyMetrics: []*dto.MetricFamily{
			counter("istio_requests_total", 3),
			counter("http.inbound_0.0.0.0_9898.downstream_rq_total", 3),
		}},
	}}

	if err := testCollector(store).StreamMetrics(stream); err != nil {
		t.Fatalf("StreamMetrics: %v", err)
	}
	if got, want := store.RequestsPerSecond(key), 6.0/60.0; got != want {
		t.Errorf("RequestsPerSecond = %v, want %v", got, want)
	}
}
