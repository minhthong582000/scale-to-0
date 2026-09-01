// Command scaler activates idle workloads from Envoy traffic.
//
// It runs two gRPC servers: one receives stats pushed by every Envoy proxy in
// the mesh, the other answers KEDA's external scaler API from what those stats
// show. Nothing is scraped and nothing is stored on disk.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	metricsv3 "github.com/envoyproxy/go-control-plane/envoy/service/metrics/v3"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/minhthong582000/scale-to-0/internal/activity"
	"github.com/minhthong582000/scale-to-0/internal/envoy"
	"github.com/minhthong582000/scale-to-0/internal/scaler"
	pb "github.com/minhthong582000/scale-to-0/pkg/externalscaler"
)

type options struct {
	metricsAddr  string
	scalerAddr   string
	requestRegex string
	window       time.Duration
	logLevel     string
}

func main() {
	var opts options
	flag.StringVar(&opts.metricsAddr, "metrics-addr", ":9001", "address for Envoy's metrics service stream")
	flag.StringVar(&opts.scalerAddr, "scaler-addr", ":9090", "address for KEDA's external scaler API")
	flag.StringVar(&opts.requestRegex, "request-metrics", envoy.DefaultRequestPattern, "regexp matching Envoy counters that mean a request was served")
	flag.DurationVar(&opts.window, "window", 30*time.Second, "window over which the request rate is averaged")
	flag.StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn or error")
	flag.Parse()

	if err := run(opts); err != nil {
		slog.Error("scaler stopped", "error", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	level, err := parseLevel(opts.logLevel)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	pattern, err := regexp.Compile(opts.requestRegex)
	if err != nil {
		return err
	}

	store := activity.New(opts.window)

	metricsSrv := grpc.NewServer(grpc.KeepaliveParams(keepalive.ServerParameters{
		Time:    30 * time.Second,
		Timeout: 10 * time.Second,
	}))
	metricsv3.RegisterMetricsServiceServer(metricsSrv, envoy.NewCollector(store, pattern, log))

	// KEDA holds StreamIsActive open and idle for long stretches; without
	// keepalive a half-open connection would silently never deliver a wake-up.
	scalerSrv := grpc.NewServer(grpc.KeepaliveParams(keepalive.ServerParameters{
		Time:    30 * time.Second,
		Timeout: 10 * time.Second,
	}))
	pb.RegisterExternalScalerServer(scalerSrv, scaler.NewServer(store, log))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error { return serve(metricsSrv, opts.metricsAddr, "envoy metrics", log) })
	group.Go(func() error { return serve(scalerSrv, opts.scalerAddr, "external scaler", log) })
	group.Go(func() error {
		<-ctx.Done()
		log.Info("shutting down")
		metricsSrv.GracefulStop()
		scalerSrv.GracefulStop()
		return nil
	})

	if err := group.Wait(); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

func serve(srv *grpc.Server, addr, name string, log *slog.Logger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Info("listening", "server", name, "addr", listener.Addr().String())
	return srv.Serve(listener)
}

func parseLevel(s string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return 0, err
	}
	return level, nil
}
