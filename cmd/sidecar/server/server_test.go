package server

import (
	"context"
	"log/slog"
	"strings"
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

func TestDeployReturnsResourceExhaustedWhenLockHeld(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")
	s.deployMu.Lock()
	defer s.deployMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.Deploy(ctx, &zarfv1.DeployRequest{Source: "oci://example.com/pkg:v1"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (%v)", status.Code(err), err)
	}
}

func TestClassifyDeployError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{
			name: "helm operation in progress",
			err: assertErr(
				`unable to deploy component "helm": ` +
					`unable to install chart another operation (install/upgrade/rollback) is in progress`,
			),
			want: codes.FailedPrecondition,
		},
		{
			name: "deadline exceeded",
			err:  assertErr(`context deadline exceeded`),
			want: codes.DeadlineExceeded,
		},
		{
			name: "fallback internal",
			err:  assertErr(`unknown deploy error`),
			want: codes.Internal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDeployError(tc.err)
			if got != tc.want {
				t.Fatalf("classifyDeployError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseDeployError(t *testing.T) {
	tests := []struct {
		name          string
		msg           string
		wantComponent string
		wantChart     string
	}{
		{
			name:          "component and chart extracted",
			msg:           `unable to deploy component "helm": unable to install chart argo-cd: boom`,
			wantComponent: "helm",
			wantChart:     "argo-cd",
		},
		{
			name:          "component only",
			msg:           `unable to deploy component "images": something`,
			wantComponent: "images",
			wantChart:     "",
		},
		{
			name:          "no match",
			msg:           `network timeout`,
			wantComponent: "",
			wantChart:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotComponent, gotChart := parseDeployError(tc.msg)
			if gotComponent != tc.wantComponent || gotChart != tc.wantChart {
				t.Fatalf("parseDeployError() = (%q,%q), want (%q,%q)",
					gotComponent, gotChart, tc.wantComponent, tc.wantChart)
			}
		})
	}
}

func TestCapturingHandlerKeepsLastNLines(t *testing.T) {
	base := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	handler := newCapturingHandler(base.Handler(), 2)
	log := slog.New(handler)

	log.Info("first")
	log.Info("second")
	log.Info("third")

	lines := handler.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "second") || !strings.Contains(lines[1], "third") {
		t.Fatalf("unexpected captured lines: %v", lines)
	}
}

func TestRemoveReturnsResourceExhaustedWhenLockHeld(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")
	s.deployMu.Lock()
	defer s.deployMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.Remove(ctx, &zarfv1.RemoveRequest{PackageName: "pkg"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (%v)", status.Code(err), err)
	}
}
