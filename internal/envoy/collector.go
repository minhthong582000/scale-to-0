// Package envoy receives Envoy's stats over gRPC.
//
// Every proxy in the mesh streams its stats here rather than being scraped, so
// the cost of knowing whether a workload is idle does not grow with the number
// of workloads.
package envoy

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"sync"

	metricsv3 "github.com/envoyproxy/go-control-plane/envoy/service/metrics/v3"
	dto "github.com/prometheus/client_model/go"

	"github.com/minhthong582000/scale-to-0/internal/activity"
)

// DefaultRequestPattern matches counters interpreted as "a request was served":
// Istio's telemetry counter and Envoy's inbound listener counter. If both are
// present in a batch, their counts are summed.
const DefaultRequestPattern = `istio_requests_total|downstream_rq_total`

// unknownNameLogLimit caps how many distinct unmatched metric names are logged
// per collector, so a debug run stays readable.
const unknownNameLogLimit = 50

// Collector implements Envoy's MetricsService and feeds an activity store.
type Collector struct {
	metricsv3.UnimplementedMetricsServiceServer

	store   *activity.Store
	pattern *regexp.Regexp
	log     *slog.Logger

	mu      sync.Mutex
	unknown map[string]struct{}
}

// NewCollector returns a collector that credits counters matching pattern.
func NewCollector(store *activity.Store, pattern *regexp.Regexp, log *slog.Logger) *Collector {
	return &Collector{
		store:   store,
		pattern: pattern,
		log:     log,
		unknown: make(map[string]struct{}),
	}
}

// StreamMetrics consumes one proxy's stat stream until it closes.
func (c *Collector) StreamMetrics(stream metricsv3.MetricsService_StreamMetricsServer) error {
	var (
		key      activity.Key
		nodeID   string
		resolved bool
	)

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// The identifier is only sent on the first message of a stream, so it
		// is held for the life of the connection.
		if node := msg.GetIdentifier().GetNode(); node != nil {
			if k, ok := WorkloadFromNode(node); ok {
				key, nodeID, resolved = k, node.GetId(), true
				c.log.Debug("proxy connected", "workload", key, "node", nodeID)
			} else {
				c.log.Warn("proxy sent unusable node identity", "node", node.GetId())
			}
		}
		if !resolved {
			continue
		}

		if total, ok := c.requestTotal(msg.GetEnvoyMetrics()); ok {
			c.store.Observe(key, nodeID, total)
		}
	}
}

// requestTotal sums every counter in the batch whose name marks a served
// request. Envoy reports cumulative values; the store turns them into deltas.
func (c *Collector) requestTotal(families []*dto.MetricFamily) (float64, bool) {
	var total float64
	var matched bool

	for _, fam := range families {
		name := fam.GetName()
		if !c.pattern.MatchString(name) {
			c.noteUnknown(name)
			continue
		}
		for _, m := range fam.GetMetric() {
			total += m.GetCounter().GetValue()
			matched = true
		}
	}
	return total, matched
}

func (c *Collector) noteUnknown(name string) {
	if !c.log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, seen := c.unknown[name]; seen || len(c.unknown) >= unknownNameLogLimit {
		return
	}
	c.unknown[name] = struct{}{}
	c.log.Debug("unmatched metric", "name", name)
}
