package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/enel1221/zarf-operator/cmd/sidecar/interceptors"
	"github.com/enel1221/zarf-operator/cmd/sidecar/server"
	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
	"github.com/zarf-dev/zarf/src/pkg/logger"
)

var (
	Version = "dev" // set at build time
)

func main() {
	port := flag.Int("port", 50051, "gRPC server port")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "console", "Log format: console, json, dev, none")
	noColor := flag.Bool("no-color", false, "Disable colored log output")
	flag.Parse()

	level, err := logger.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid log level: %v\n", err)
		os.Exit(1)
	}

	baseConfig := logger.Config{
		Level:       level,
		Format:      logger.Format(*logFormat),
		Destination: logger.DestinationDefault,
		Color:       logger.Color(!*noColor),
	}

	baseLogger, err := logger.New(baseConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	logger.SetDefault(baseLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	addr := fmt.Sprintf(":%d", *port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		baseLogger.Error("failed to listen", "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			interceptors.UnaryLogging(baseLogger),
			interceptors.UnaryMetrics(),
		),
	)

	// Register Zarf service
	zarfServer := server.NewZarfServer(baseLogger, baseConfig, Version)
	zarfv1.RegisterZarfServiceServer(grpcServer, zarfServer)

	// Register health check
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("zarf.v1.ZarfService", grpc_health_v1.HealthCheckResponse_SERVING)

	// Enable reflection for debugging
	reflection.Register(grpcServer)

	baseLogger.Info("zarf sidecar listening", "address", addr, "logLevel", level.String(), "logFormat", baseConfig.Format)

	// Serve Prometheus metrics on a separate HTTP port
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{Addr: ":9090", Handler: metricsMux}
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			baseLogger.Error("metrics server failed", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		metricsServer.Close()
		grpcServer.GracefulStop()
	}()

	if err := grpcServer.Serve(lis); err != nil {
		baseLogger.Error("failed to serve", "error", err)
		os.Exit(1)
	}
}
