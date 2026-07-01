package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	zarfapi "github.com/zarf-dev/zarf/src/api/v1alpha1"
	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/logger"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
	"github.com/zarf-dev/zarf/src/pkg/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/tools/clientcmd"

	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
)

const registryV2Path = "/v2/"

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

func TestDeployFailsWhenPostDeployImageVerificationFails(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")
	s.loadPkg = func(
		context.Context,
		string,
		packager.LoadOptions,
	) (*layout.PackageLayout, error) {
		return &layout.PackageLayout{
			Pkg: zarfapi.ZarfPackage{
				Metadata: zarfapi.ZarfMetadata{
					Name:    "crossplane-extras",
					Version: "1.10.25",
				},
				Components: []zarfapi.ZarfComponent{
					{
						Name:   "crossplane-functions",
						Images: []string{"registry.jadeuc.com/internal/jade-crossplane/schema-server:2.5.0"},
					},
				},
			},
		}, nil
	}
	s.deployPkg = func(
		context.Context,
		*layout.PackageLayout,
		packager.DeployOptions,
	) (packager.DeployResult, error) {
		return packager.DeployResult{}, nil
	}
	s.verifyImgs = func(context.Context, *layout.PackageLayout, packager.DeployOptions) error {
		return &imageVerificationError{
			component: "crossplane-functions",
			ref:       "sedptestregistry.azurecr.us/internal/jade-crossplane/schema-server:2.5.0",
			err:       fmt.Errorf("not found"),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := s.Deploy(ctx, &zarfv1.DeployRequest{
		Source:                  "oci://registry.jadeuc.com/internal/jade-packages/crossplane-extras:1.10.25",
		Architecture:            "amd64",
		SkipSignatureValidation: true,
	})

	if err == nil {
		t.Fatalf("expected deploy to fail when post-deploy image verification fails")
	}
	if resp != nil {
		t.Fatalf("expected nil response on verification failure, got %#v", resp)
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("expected NotFound for missing target image, got %v (%v)", got, err)
	}
	st := status.Convert(err)
	var detail *zarfv1.DeployErrorDetail
	for _, d := range st.Details() {
		if typed, ok := d.(*zarfv1.DeployErrorDetail); ok {
			detail = typed
			break
		}
	}
	if detail == nil {
		t.Fatalf("expected DeployErrorDetail in verification failure")
		return
	}
	if detail.FailedComponent != "crossplane-functions" {
		t.Fatalf("FailedComponent = %q, want crossplane-functions", detail.FailedComponent)
	}
	if !strings.Contains(detail.ErrorMessage, "schema-server:2.5.0") {
		t.Fatalf("ErrorMessage should include missing image, got %q", detail.ErrorMessage)
	}
}

func TestDeployReportsActualFailedImageComponent(t *testing.T) {
	s := NewZarfServer(nil, logger.Config{}, "test")
	s.loadPkg = func(
		context.Context,
		string,
		packager.LoadOptions,
	) (*layout.PackageLayout, error) {
		return &layout.PackageLayout{
			Pkg: zarfapi.ZarfPackage{
				Metadata: zarfapi.ZarfMetadata{Name: "multi-image", Version: "1.0.0"},
				Components: []zarfapi.ZarfComponent{
					{
						Name:   "first-images",
						Images: []string{"ghcr.io/example/first:v1"},
					},
					{
						Name:   "schema-server",
						Images: []string{"registry.jadeuc.com/internal/jade-crossplane/schema-server:2.5.0"},
					},
				},
			},
		}, nil
	}
	s.deployPkg = func(
		context.Context,
		*layout.PackageLayout,
		packager.DeployOptions,
	) (packager.DeployResult, error) {
		return packager.DeployResult{}, nil
	}
	s.verifyImgs = func(context.Context, *layout.PackageLayout, packager.DeployOptions) error {
		return &imageVerificationError{
			component: "schema-server",
			ref:       "sedptestregistry.azurecr.us/internal/jade-crossplane/schema-server:2.5.0",
			err:       fmt.Errorf("not found"),
		}
	}

	_, err := s.Deploy(context.Background(), &zarfv1.DeployRequest{
		Source:                  "oci://registry.jadeuc.com/internal/jade-packages/crossplane-extras:1.10.25",
		Architecture:            "amd64",
		SkipSignatureValidation: true,
	})
	if err == nil {
		t.Fatalf("expected deploy to fail")
	}

	st := status.Convert(err)
	var detail *zarfv1.DeployErrorDetail
	for _, d := range st.Details() {
		if typed, ok := d.(*zarfv1.DeployErrorDetail); ok {
			detail = typed
			break
		}
	}
	if detail == nil {
		t.Fatalf("expected DeployErrorDetail in verification failure")
		return
	}
	if detail.FailedComponent != "schema-server" {
		t.Fatalf("FailedComponent = %q, want schema-server", detail.FailedComponent)
	}
}

func TestExpectedDeployedImageRefsIncludesRuntimeAndChecksumRefs(t *testing.T) {
	refs, err := expectedDeployedImageRefs("registry.example.com/project", &layout.PackageLayout{
		Pkg: zarfapi.ZarfPackage{
			Components: []zarfapi.ZarfComponent{
				{
					Name: "images",
					Images: []string{
						"ghcr.io/example/schema-server:2.5.0",
						"ghcr.io/example/schema-server:2.5.0",
					},
				},
				{
					Name:   "zarf-agent",
					Images: []string{"ghcr.io/example/zarf-agent:v1"},
				},
			},
		},
	}, state.RegistryInfo{Address: "registry.example.com/project"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(refs) != 3 {
		t.Fatalf("expected 3 unique refs, got %d: %v", len(refs), refs)
	}
	if refs[0].component != "images" {
		t.Fatalf("component for runtime ref = %q", refs[0].component)
	}
	if refs[0].ref != "registry.example.com/project/example/schema-server:2.5.0" {
		t.Fatalf("runtime ref = %q", refs[0])
	}
	if refs[1].component != "images" {
		t.Fatalf("component for checksum ref = %q", refs[1].component)
	}
	if !strings.HasPrefix(refs[1].ref, "registry.example.com/project/example/schema-server:2.5.0-zarf-") {
		t.Fatalf("checksum ref = %q", refs[1])
	}
	if refs[2].component != "zarf-agent" {
		t.Fatalf("component for agent ref = %q", refs[2].component)
	}
	if refs[2].ref != "registry.example.com/project/example/zarf-agent:v1" {
		t.Fatalf("agent ref = %q", refs[2])
	}
}

func TestExpectedDeployedImageRefsSkipsExternalInitBootstrapImages(t *testing.T) {
	refs, err := expectedDeployedImageRefs("registry.example.com/project", &layout.PackageLayout{
		Pkg: zarfapi.ZarfPackage{
			Kind: zarfapi.ZarfInitConfig,
			Components: []zarfapi.ZarfComponent{
				{Name: "zarf-seed-registry", Images: []string{"ghcr.io/example/seed:v1"}},
				{Name: "zarf-injector", Images: []string{"ghcr.io/example/injector:v1"}},
				{Name: "zarf-registry", Images: []string{"ghcr.io/example/registry:v1"}},
			},
		},
	}, state.RegistryInfo{
		Address:      "registry.example.com/project",
		RegistryMode: state.RegistryModeExternal,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected external init bootstrap images to be skipped, got %v", refs)
	}
}

func TestResolveRegistryImageFailsWhenTargetRegistryLacksImage(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(missingImageRegistryHandler))
	defer registry.Close()

	ref := strings.TrimPrefix(registry.URL, "http://") + "/internal/jade-crossplane/schema-server:2.5.0"
	err := resolveRegistryImage(context.Background(), ref, state.RegistryInfo{}, packager.DeployOptions{
		RemoteOptions: packager.RemoteOptions{PlainHTTP: true},
	})
	if err == nil {
		t.Fatalf("expected missing image resolve to fail")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestResolveRegistryImageProbesLocalHTTPSBeforePlainHTTP(t *testing.T) {
	registry := httptest.NewTLSServer(http.HandlerFunc(missingImageRegistryHandler))
	defer registry.Close()

	ref := strings.TrimPrefix(registry.URL, "https://") + "/internal/jade-crossplane/schema-server:2.5.0"
	err := resolveRegistryImage(context.Background(), ref, state.RegistryInfo{}, packager.DeployOptions{
		RemoteOptions: packager.RemoteOptions{InsecureSkipTLSVerify: true},
	})
	if err == nil {
		t.Fatalf("expected missing image resolve to fail")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected HTTPS registry to be probed before plain HTTP, got %v", err)
	}
}

func TestResolveRegistryImageUsesPullCredentials(t *testing.T) {
	authSeen := false
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		if r.URL.Path == registryV2Path {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			username, password, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="test-registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if username != "pull-user" || password != "pull-pass" {
				http.Error(w, "bad credentials", http.StatusForbidden)
				return
			}
			authSeen = true
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer registry.Close()

	ref := strings.TrimPrefix(registry.URL, "http://") + "/internal/jade-crossplane/schema-server:2.5.0"
	err := resolveRegistryImage(context.Background(), ref, state.RegistryInfo{
		PullUsername: "pull-user",
		PullPassword: "pull-pass",
	}, packager.DeployOptions{
		RemoteOptions: packager.RemoteOptions{PlainHTTP: true},
	})
	if err == nil {
		t.Fatalf("expected missing image resolve to fail")
	}
	if !authSeen {
		t.Fatalf("expected resolver to retry manifest request with pull credentials")
	}
	if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
		t.Fatalf("expected credentials to be accepted before missing image error, got %v", err)
	}
}

func missingImageRegistryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if r.URL.Path == registryV2Path {
		w.WriteHeader(http.StatusOK)
		return
	}
	if strings.Contains(r.URL.Path, "/manifests/") {
		http.NotFound(w, r)
		return
	}
	http.NotFound(w, r)
}

func TestResolveRegistryImageTimesOutWaitingForHeaders(t *testing.T) {
	oldTimeout := registryResolveResponseHeaderTimeout
	registryResolveResponseHeaderTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		registryResolveResponseHeaderTimeout = oldTimeout
	})

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer registry.Close()

	ref := strings.TrimPrefix(registry.URL, "http://") + "/internal/jade-crossplane/schema-server:2.5.0"
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := resolveRegistryImage(ctx, ref, state.RegistryInfo{}, packager.DeployOptions{
		RemoteOptions: packager.RemoteOptions{PlainHTTP: true},
	})
	if err == nil {
		t.Fatalf("expected stalled registry resolve to fail")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("expected resolve to fail quickly from response header timeout, took %s: %v", elapsed, err)
	}
	if !strings.Contains(err.Error(), "timeout awaiting response headers") &&
		!strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected timeout-style error, got %v", err)
	}
}

func TestVerifyDeployedPackageImagesAgainstK3DExternalRegistryMissingImage(t *testing.T) {
	if os.Getenv("ZARF_OPERATOR_K3D_REGISTRY_TEST") != "1" {
		t.Skip("set ZARF_OPERATOR_K3D_REGISTRY_TEST=1 and ZARF_OPERATOR_K3D_CONTEXT with a disposable k3d context to run")
	}
	requireDisposableK3DContext(t)

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		if r.URL.Path == registryV2Path {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer registry.Close()

	pkgLayout := &layout.PackageLayout{
		Pkg: zarfapi.ZarfPackage{
			Metadata: zarfapi.ZarfMetadata{Name: "crossplane-extras", Version: "1.10.25"},
			Components: []zarfapi.ZarfComponent{
				{
					Name:   "crossplane-functions",
					Images: []string{"registry.jadeuc.com/internal/jade-crossplane/schema-server:2.5.0"},
				},
			},
		},
	}

	err := verifyDeployedPackageImages(context.Background(), pkgLayout, packager.DeployOptions{
		RegistryInfo: state.RegistryInfo{
			Address:      strings.TrimPrefix(registry.URL, "http://"),
			RegistryMode: state.RegistryModeExternal,
		},
		RemoteOptions: packager.RemoteOptions{PlainHTTP: true},
	})
	if err == nil {
		t.Fatalf("expected missing target image verification failure")
	}
	var verificationErr *imageVerificationError
	if !errors.As(err, &verificationErr) {
		t.Fatalf("expected imageVerificationError, got %T: %v", err, err)
	}
	if verificationErr.component != "crossplane-functions" {
		t.Fatalf("component = %q, want crossplane-functions", verificationErr.component)
	}
	if !strings.Contains(verificationErr.ref, "schema-server:2.5.0") {
		t.Fatalf("missing image ref should name schema-server:2.5.0, got %q", verificationErr.ref)
	}
}

func requireDisposableK3DContext(t *testing.T) {
	t.Helper()

	wantContext := os.Getenv("ZARF_OPERATOR_K3D_CONTEXT")
	if strings.TrimSpace(wantContext) == "" {
		t.Fatalf("ZARF_OPERATOR_K3D_CONTEXT must name the disposable k3d context")
	}
	if !strings.HasPrefix(wantContext, "k3d-") {
		t.Fatalf("ZARF_OPERATOR_K3D_CONTEXT must be a k3d context, got %q", wantContext)
	}

	config, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		t.Fatalf("load kubeconfig: %v", err)
	}
	if config.CurrentContext != wantContext {
		t.Fatalf("current kube context = %q, want disposable test context %q", config.CurrentContext, wantContext)
	}

	clusterClient, err := cluster.New(context.Background())
	if err != nil {
		t.Fatalf("connect to disposable k3d context %q: %v", wantContext, err)
	}
	if _, err := clusterClient.LoadState(context.Background()); err == nil {
		t.Fatalf("current context %q already has zarf-state; use an empty disposable k3d cluster", wantContext)
	}
}

func TestIsLocalRegistryHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{host: "127.0.0.1:5000", want: true},
		{host: "[::1]:5000", want: true},
		{host: "localhost", want: true},
		{host: "registry.example.com", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			if got := isLocalRegistryHost(tc.host); got != tc.want {
				t.Fatalf("isLocalRegistryHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
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

func TestBuildRegistryInfo(t *testing.T) {
	tests := []struct {
		name       string
		in         *zarfv1.InitOptions
		wantAddr   string
		wantPort   int
		wantPushUN string
		wantSecret string
	}{
		{
			name:     "nil input yields zero value",
			in:       nil,
			wantAddr: "",
			wantPort: 0,
		},
		{
			name: "internal registry (nodePort-only)",
			in: &zarfv1.InitOptions{
				RegistryNodePort:     31999,
				RegistrySecret:       "agent-signing-secret",
				RegistryPushUsername: "zarf-push",
				RegistryPushPassword: "pw1",
				RegistryPullUsername: "zarf-pull",
				RegistryPullPassword: "pw2",
			},
			wantAddr:   "",
			wantPort:   31999,
			wantPushUN: "zarf-push",
			wantSecret: "agent-signing-secret",
		},
		{
			name: "external registry (address + creds, no nodePort)",
			in: &zarfv1.InitOptions{
				RegistryAddress:      "registry.example.com",
				RegistrySecret:       "agent-secret",
				RegistryPushUsername: "write",
				RegistryPushPassword: "pw-write",
				RegistryPullUsername: "read",
				RegistryPullPassword: "pw-read",
			},
			wantAddr:   "registry.example.com",
			wantPort:   0,
			wantPushUN: "write",
			wantSecret: "agent-secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRegistryInfo(tc.in)
			if got.Address != tc.wantAddr {
				t.Errorf("Address: got %q, want %q", got.Address, tc.wantAddr)
			}
			if got.NodePort != tc.wantPort {
				t.Errorf("NodePort: got %d, want %d", got.NodePort, tc.wantPort)
			}
			if got.PushUsername != tc.wantPushUN {
				t.Errorf("PushUsername: got %q, want %q", got.PushUsername, tc.wantPushUN)
			}
			if got.Secret != tc.wantSecret {
				t.Errorf("Secret: got %q, want %q", got.Secret, tc.wantSecret)
			}
		})
	}
}
