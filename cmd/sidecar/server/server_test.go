package server

import (
	"context"
	"testing"
	"time"

	"github.com/zarf-dev/zarf/src/pkg/logger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
)

func TestClassifyOCIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{
			name: "nil error defaults internal",
			err:  nil,
			want: codes.Internal,
		},
		{
			name: "401 unauthorized",
			err:  assertErr("response status code 401 unauthorized"),
			want: codes.Unauthenticated,
		},
		{
			name: "credential not found treated auth failure",
			err:  assertErr("basic credential not found"),
			want: codes.Unauthenticated,
		},
		{
			name: "403 forbidden",
			err:  assertErr("response status code 403: denied: access forbidden"),
			want: codes.PermissionDenied,
		},
		{
			name: "signature verification failed",
			err:  assertErr("signature verification failed: package is not signed"),
			want: codes.InvalidArgument,
		},
		{
			name: "manifest missing 404",
			err:  assertErr("manifests/1.0.0: response status code 404: not found"),
			want: codes.NotFound,
		},
		{
			name: "fallback internal",
			err:  assertErr("tls handshake timeout"),
			want: codes.Internal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyOCIError(tc.err)
			if got != tc.want {
				t.Fatalf("classifyOCIError() = %v, want %v", got, tc.want)
			}
		})
	}
}

type assertErr string

func (e assertErr) Error() string {
	return string(e)
}

func TestDeployReturnsDeadlineExceededWhenWaitingForLock(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")
	s.deployMu.Lock()

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := s.Deploy(ctx, &zarfv1.DeployRequest{Source: "oci://example.com/pkg:v1"})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	s.deployMu.Unlock()

	select {
	case err := <-errCh:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded, got %v (%v)", status.Code(err), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Deploy to return")
	}
}

func TestRemoveReturnsDeadlineExceededWhenWaitingForLock(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")
	s.deployMu.Lock()

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := s.Remove(ctx, &zarfv1.RemoveRequest{PackageName: "pkg"})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	s.deployMu.Unlock()

	select {
	case err := <-errCh:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded, got %v (%v)", status.Code(err), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Remove to return")
	}
}
