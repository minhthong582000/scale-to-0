// Package scaler serves KEDA's external scaler API from observed traffic.
//
// KEDA is always the gRPC client: it dials this server, so nothing here needs
// to know where KEDA runs. StreamIsActive is a server-streaming call, which is
// what lets an activation be pushed the moment a request arrives instead of
// waiting for the next poll.
package scaler

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/minhthong582000/scale-to-0/internal/activity"
	pb "github.com/minhthong582000/scale-to-0/pkg/externalscaler"
)

// idleCheckInterval bounds how long a workload can look active after its last
// request. Activation is pushed immediately; only deactivation waits for this.
const idleCheckInterval = time.Second

// Server implements externalscaler.ExternalScalerServer.
type Server struct {
	pb.UnimplementedExternalScalerServer

	store *activity.Store
	log   *slog.Logger
}

// NewServer returns a scaler backed by store.
func NewServer(store *activity.Store, log *slog.Logger) *Server {
	return &Server{store: store, log: log}
}

// IsActive answers whether the workload should run at all. Returning false is
// what allows KEDA to scale it to zero.
func (s *Server) IsActive(_ context.Context, ref *pb.ScaledObjectRef) (*pb.IsActiveResponse, error) {
	cfg, err := parseTrigger(ref)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &pb.IsActiveResponse{Result: s.store.Active(cfg.workload, cfg.idle)}, nil
}

// StreamIsActive pushes activation changes for as long as KEDA holds the stream.
func (s *Server) StreamIsActive(ref *pb.ScaledObjectRef, stream pb.ExternalScaler_StreamIsActiveServer) error {
	cfg, err := parseTrigger(ref)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	traffic, cancel := s.store.Subscribe(cfg.workload)
	defer cancel()

	// KEDA knows nothing when it opens a stream, and it reopens one after every
	// restart or dropped connection, so the current state goes out first.
	active := s.store.Active(cfg.workload, cfg.idle)
	if err := stream.Send(&pb.IsActiveResponse{Result: active}); err != nil {
		return err
	}
	s.log.Debug("stream opened", "workload", cfg.workload, "active", active)

	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-traffic:
		case <-ticker.C:
		}

		if current := s.store.Active(cfg.workload, cfg.idle); current != active {
			active = current
			if err := stream.Send(&pb.IsActiveResponse{Result: active}); err != nil {
				return err
			}
			s.log.Info("activation changed", "workload", cfg.workload, "active", active)
		}
	}
}

// GetMetricSpec reports the per-replica request rate KEDA should aim for.
func (s *Server) GetMetricSpec(_ context.Context, ref *pb.ScaledObjectRef) (*pb.GetMetricSpecResponse, error) {
	cfg, err := parseTrigger(ref)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	return &pb.GetMetricSpecResponse{
		MetricSpecs: []*pb.MetricSpec{{
			MetricName:      cfg.metricName(),
			TargetSize:      int64(cfg.targetRPS),
			TargetSizeFloat: cfg.targetRPS,
		}},
	}, nil
}

// GetMetrics reports the workload's current request rate. The HPA divides it by
// the target from GetMetricSpec to pick a replica count.
func (s *Server) GetMetrics(_ context.Context, req *pb.GetMetricsRequest) (*pb.GetMetricsResponse, error) {
	cfg, err := parseTrigger(req.GetScaledObjectRef())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	rps := s.store.RequestsPerSecond(cfg.workload)
	return &pb.GetMetricsResponse{
		MetricValues: []*pb.MetricValue{{
			MetricName:       cfg.metricName(),
			MetricValue:      int64(rps),
			MetricValueFloat: rps,
		}},
	}, nil
}
