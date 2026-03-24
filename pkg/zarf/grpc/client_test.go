package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	zarfpkg "github.com/enel1221/zarf-operator/pkg/zarf"
	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockZarfServiceClient struct {
	deployFn func(ctx context.Context, in *zarfv1.DeployRequest) (*zarfv1.DeployResponse, error)
}

func (m *mockZarfServiceClient) Deploy(
	ctx context.Context,
	in *zarfv1.DeployRequest,
	_ ...grpc.CallOption,
) (*zarfv1.DeployResponse, error) {
	return m.deployFn(ctx, in)
}

func (*mockZarfServiceClient) Remove(
	context.Context,
	*zarfv1.RemoveRequest,
	...grpc.CallOption,
) (*zarfv1.RemoveResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (*mockZarfServiceClient) GetDeployedPackage(
	context.Context,
	*zarfv1.GetDeployedPackageRequest,
	...grpc.CallOption,
) (*zarfv1.GetDeployedPackageResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (*mockZarfServiceClient) ListDeployedPackages(
	context.Context,
	*zarfv1.ListDeployedPackagesRequest,
	...grpc.CallOption,
) (*zarfv1.ListDeployedPackagesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (*mockZarfServiceClient) GetPackageMetadata(
	context.Context,
	*zarfv1.GetPackageMetadataRequest,
	...grpc.CallOption,
) (*zarfv1.GetPackageMetadataResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (*mockZarfServiceClient) Health(
	context.Context,
	*zarfv1.HealthRequest,
	...grpc.CallOption,
) (*zarfv1.HealthResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func TestDeployExtractsDeployErrorDetail(t *testing.T) {
	st, err := status.New(codes.Internal, "deployment failed").WithDetails(&zarfv1.DeployErrorDetail{
		FailedComponent: "helm",
		FailedChart:     "argo-cd",
		ErrorMessage:    "boom",
		DeployLogs:      []string{"line1", "line2"},
	})
	if err != nil {
		t.Fatalf("failed to build status details: %v", err)
	}

	c := &Client{
		client: &mockZarfServiceClient{
			deployFn: func(context.Context, *zarfv1.DeployRequest) (*zarfv1.DeployResponse, error) {
				return nil, st.Err()
			},
		},
	}

	_, deployErr := c.Deploy(context.Background(), zarfpkg.DeployOptions{
		Source:  "oci://example.com/pkg:v1",
		Timeout: time.Minute,
	})
	if deployErr == nil {
		t.Fatal("expected deploy error")
	}

	var derr *zarfpkg.DeployError
	if ok := errors.As(deployErr, &derr); !ok {
		t.Fatalf("expected *zarf.DeployError, got %T", deployErr)
	}
	if derr.FailedComponent != "helm" || derr.FailedChart != "argo-cd" {
		t.Fatalf("unexpected details: component=%q chart=%q", derr.FailedComponent, derr.FailedChart)
	}
	if len(derr.DeployLogs) != 2 {
		t.Fatalf("expected logs to be copied, got %v", derr.DeployLogs)
	}
}

func TestDeploySuccess(t *testing.T) {
	c := &Client{
		client: &mockZarfServiceClient{
			deployFn: func(context.Context, *zarfv1.DeployRequest) (*zarfv1.DeployResponse, error) {
				return &zarfv1.DeployResponse{
					PackageName: "pkg",
					Version:     "1.0.0",
					Generation:  1,
				}, nil
			},
		},
	}

	res, err := c.Deploy(context.Background(), zarfpkg.DeployOptions{
		Source:  "oci://example.com/pkg:v1",
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if res.PackageName != "pkg" || res.Version != "1.0.0" {
		t.Fatalf("unexpected deploy result: %+v", res)
	}
}
