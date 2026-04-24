package server

import (
	"context"
	"log/slog"
	"os"
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

func TestDeployReturnsResourceExhaustedWhenRemoveActive(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")

	// Simulate an active remove operation.
	s.mu.Lock()
	s.active = &activeOperation{
		cancel: func() {},
		done:   make(chan struct{}),
		kind:   opRemove,
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.Deploy(ctx, &zarfv1.DeployRequest{Source: "oci://example.com/pkg:v1"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (%v)", status.Code(err), err)
	}
}

func TestDeployCancelsPreviousDeploy(t *testing.T) {
	s, cancelled := simulateActiveDeploy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Deploy(ctx, &zarfv1.DeployRequest{Source: "oci://example.com/pkg:v1"})

	assertCancelled(t, cancelled, err)
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

func TestRemoveReturnsResourceExhaustedWhenRemoveActive(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")

	// Simulate an active remove operation.
	s.mu.Lock()
	s.active = &activeOperation{
		cancel: func() {},
		done:   make(chan struct{}),
		kind:   opRemove,
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.Remove(ctx, &zarfv1.RemoveRequest{PackageName: "pkg"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (%v)", status.Code(err), err)
	}
}

func TestRemoveCancelsPreviousDeploy(t *testing.T) {
	s, cancelled := simulateActiveDeploy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Remove(ctx, &zarfv1.RemoveRequest{PackageName: "pkg"})

	assertCancelled(t, cancelled, err)
}

// simulateActiveDeploy creates a ZarfServer with a fake in-progress deploy
// that will clean up when cancelled. Returns the server and a channel that
// closes when the cancel func is invoked.
func simulateActiveDeploy(t *testing.T) (*ZarfServer, <-chan struct{}) {
	t.Helper()
	s := NewZarfServer(nil, logger.Config{}, "test")

	cancelled := make(chan struct{})
	done := make(chan struct{})

	s.mu.Lock()
	s.active = &activeOperation{
		cancel: func() { close(cancelled) },
		done:   done,
		kind:   opDeploy,
	}
	s.mu.Unlock()

	go func() {
		<-cancelled
		s.mu.Lock()
		s.active = nil
		s.mu.Unlock()
		close(done)
	}()

	return s, cancelled
}

// assertCancelled verifies the previous operation was cancelled and the
// returned error is not ResourceExhausted (meaning the caller got past
// slot acquisition).
func assertCancelled(t *testing.T, cancelled <-chan struct{}, err error) {
	t.Helper()
	select {
	case <-cancelled:
	default:
		t.Fatal("previous deploy was not cancelled")
	}
	if status.Code(err) == codes.ResourceExhausted {
		t.Fatalf(
			"should not get ResourceExhausted after cancelling previous deploy: %v",
			err,
		)
	}
}

func TestApplyKubeconfig_EmptyBytesNoOp(t *testing.T) {
	orig, hadOrig := os.LookupEnv("KUBECONFIG")
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("KUBECONFIG", orig)
		} else {
			_ = os.Unsetenv("KUBECONFIG")
		}
	})
	_ = os.Unsetenv("KUBECONFIG")

	cleanup, err := applyKubeconfig(slog.Default(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := os.LookupEnv("KUBECONFIG"); ok {
		t.Fatalf("KUBECONFIG should remain unset when bytes are empty")
	}
	cleanup()
	if _, ok := os.LookupEnv("KUBECONFIG"); ok {
		t.Fatalf("KUBECONFIG should remain unset after cleanup when bytes are empty")
	}
}

func TestApplyKubeconfig_SetsAndRestoresEnv(t *testing.T) {
	origValue, hadOrig := os.LookupEnv("KUBECONFIG")
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("KUBECONFIG", origValue)
		} else {
			_ = os.Unsetenv("KUBECONFIG")
		}
	})
	_ = os.Unsetenv("KUBECONFIG")

	payload := []byte("placeholder-kubeconfig-content")
	cleanup, err := applyKubeconfig(slog.Default(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := os.Getenv("KUBECONFIG")
	if path == "" {
		t.Fatalf("KUBECONFIG was not set")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("failed to read kubeconfig tmp file: %v", readErr)
	}
	if string(got) != string(payload) {
		t.Errorf("kubeconfig contents mismatch: got %q want %q", string(got), string(payload))
	}

	cleanup()

	if _, ok := os.LookupEnv("KUBECONFIG"); ok {
		t.Fatalf("KUBECONFIG should be unset after cleanup (was unset before apply)")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected kubeconfig tmp file %q to be removed after cleanup, stat err=%v", path, err)
	}
}

func TestApplyKubeconfig_RestoresPreexistingEnv(t *testing.T) {
	const original = "/tmp/does-not-exist-kubeconfig"
	prev, hadPrev := os.LookupEnv("KUBECONFIG")
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("KUBECONFIG", prev)
		} else {
			_ = os.Unsetenv("KUBECONFIG")
		}
	})
	if err := os.Setenv("KUBECONFIG", original); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	cleanup, err := applyKubeconfig(slog.Default(), []byte("x"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if os.Getenv("KUBECONFIG") == original {
		t.Fatalf("KUBECONFIG was not overridden while applied")
	}
	cleanup()

	if got := os.Getenv("KUBECONFIG"); got != original {
		t.Errorf("KUBECONFIG not restored after cleanup: got %q want %q", got, original)
	}
}
