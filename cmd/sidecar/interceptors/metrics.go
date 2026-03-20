package interceptors

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

var (
	grpcRequestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "grpc_server_requests_total",
			Help: "Total number of gRPC requests",
		},
		[]string{"method", "code"},
	)
	grpcRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "grpc_server_request_duration_seconds",
			Help:    "gRPC request duration in seconds",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600, 900},
		},
		[]string{"method"},
	)
)

func init() {
	prometheus.MustRegister(grpcRequestCount, grpcRequestDuration)
}

// UnaryMetrics returns a gRPC unary server interceptor that records request count and duration.
func UnaryMetrics() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)

		st, _ := status.FromError(err)
		grpcRequestCount.WithLabelValues(info.FullMethod, st.Code().String()).Inc()
		grpcRequestDuration.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())

		return resp, err
	}
}
