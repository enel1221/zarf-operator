package interceptors

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// UnaryLogging returns a gRPC unary server interceptor that logs every request.
func UnaryLogging(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		st, _ := status.FromError(err)
		logger.Info("grpc request",
			"method", info.FullMethod,
			"duration", duration,
			"code", st.Code().String(),
			"error", st.Message(),
		)
		return resp, err
	}
}
