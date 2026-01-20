package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/enel1221/zarf-operator/cmd/sidecar/server"
	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
)

func main() {
	port := flag.Int("port", 50051, "gRPC server port")
	flag.Parse()

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
		fmt.Fprintf(os.Stderr, "failed to listen: %v\n", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()

	// Register Zarf service
	zarfServer := server.NewZarfServer()
	zarfv1.RegisterZarfServiceServer(grpcServer, zarfServer)

	// Register health check
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("zarf.v1.ZarfService", grpc_health_v1.HealthCheckResponse_SERVING)

	// Enable reflection for debugging
	reflection.Register(grpcServer)

	fmt.Printf("Zarf sidecar listening on %s\n", addr)

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

	if err := grpcServer.Serve(lis); err != nil {
		fmt.Fprintf(os.Stderr, "failed to serve: %v\n", err)
		os.Exit(1)
	}
}
