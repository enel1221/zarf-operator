package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/enel1221/zarf-operator/pkg/zarf"
	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/durationpb"
)

var _ zarf.Client = (*Client)(nil)

// Client implements zarf.Client using gRPC
type Client struct {
	conn   *grpc.ClientConn
	client zarfv1.ZarfServiceClient
}

// NewClient creates a new gRPC client
func NewClient(ctx context.Context, address string) (*Client, error) {
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  1 * time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   30 * time.Second,
			},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to zarf sidecar: %w", err)
	}
	conn.Connect()

	client := &Client{
		conn:   conn,
		client: zarfv1.NewZarfServiceClient(conn),
	}
	if err := client.HealthCheck(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sidecar health check failed: %w", err)
	}
	return client, nil
}

// HealthCheck verifies sidecar connectivity and readiness.
func (c *Client) HealthCheck(ctx context.Context) error {
	resp, err := c.client.Health(ctx, &zarfv1.HealthRequest{})
	if err != nil {
		return err
	}
	if !resp.GetHealthy() {
		return fmt.Errorf("sidecar unhealthy: %s", resp.GetMessage())
	}
	return nil
}

// Deploy deploys a Zarf package
func (c *Client) Deploy(ctx context.Context, opts zarf.DeployOptions) (*zarf.DeployResult, error) {
	req := &zarfv1.DeployRequest{
		Source:                  opts.Source,
		Components:              opts.Components,
		SetVariables:            opts.SetVariables,
		AdoptExistingResources:  opts.AdoptExistingResources,
		Timeout:                 durationpb.New(opts.Timeout),
		Retries:                 int32(opts.Retries),
		NamespaceOverride:       opts.NamespaceOverride,
		Shasum:                  opts.Shasum,
		SkipSignatureValidation: opts.SkipSignatureValidation,
		Architecture:            opts.Architecture,
		OciConcurrency:          int32(opts.OCIConcurrency),
		PublicKeyPath:           opts.PublicKeyPath,
		LogLevel:                opts.LogLevel,
		LogFormat:               opts.LogFormat,
		NoColor:                 opts.NoColor,
		PlainHttp:               opts.PlainHTTP,
		InsecureSkipTlsVerify:   opts.InsecureSkipTLSVerify,
		SkipVersionCheck:        opts.SkipVersionCheck,
		YoloMode:                opts.YoloMode,
		RegistryCredentialJson:  opts.RegistryCredentialJSON,
	}

	resp, err := c.client.Deploy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("deploy failed: %w", err)
	}

	return &zarf.DeployResult{
		PackageName:        resp.PackageName,
		Version:            resp.Version,
		Generation:         int(resp.Generation),
		DeployedComponents: convertComponents(resp.DeployedComponents),
	}, nil
}

// GetDeployedPackage returns information about a deployed package
func (c *Client) GetDeployedPackage(ctx context.Context, packageName string) (*zarf.PackageInfo, error) {
	resp, err := c.client.GetDeployedPackage(ctx, &zarfv1.GetDeployedPackageRequest{
		PackageName: packageName,
	})
	if err != nil {
		return nil, fmt.Errorf("get deployed package failed: %w", err)
	}

	return convertPackageInfo(resp.Package), nil
}

// ListDeployedPackages returns all deployed packages
func (c *Client) ListDeployedPackages(ctx context.Context) ([]zarf.PackageInfo, error) {
	resp, err := c.client.ListDeployedPackages(ctx, &zarfv1.ListDeployedPackagesRequest{})
	if err != nil {
		return nil, fmt.Errorf("list deployed packages failed: %w", err)
	}

	packages := make([]zarf.PackageInfo, 0, len(resp.Packages))
	for _, pkg := range resp.Packages {
		packages = append(packages, *convertPackageInfo(pkg))
	}
	return packages, nil
}

// Remove removes a deployed package
func (c *Client) Remove(ctx context.Context, opts zarf.RemoveOptions) error {
	req := &zarfv1.RemoveRequest{
		PackageName:       opts.PackageName,
		Components:        opts.Components,
		Timeout:           durationpb.New(opts.Timeout),
		NamespaceOverride: opts.NamespaceOverride,
		SkipVersionCheck:  opts.SkipVersionCheck,
	}

	_, err := c.client.Remove(ctx, req)
	if err != nil {
		return fmt.Errorf("remove failed: %w", err)
	}
	return nil
}

// GetPackageMetadata fetches metadata from a package source
func (c *Client) GetPackageMetadata(ctx context.Context, source string) (*zarf.PackageMetadata, error) {
	resp, err := c.client.GetPackageMetadata(ctx, &zarfv1.GetPackageMetadataRequest{
		Source: source,
	})
	if err != nil {
		return nil, fmt.Errorf("get package metadata failed: %w", err)
	}

	return &zarf.PackageMetadata{
		Name:         resp.Metadata.Name,
		Version:      resp.Metadata.Version,
		Description:  resp.Metadata.Description,
		Components:   resp.Metadata.Components,
		Architecture: resp.Metadata.Architecture,
	}, nil
}

// Close closes the gRPC connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// Helper functions for type conversion
func convertComponents(comps []*zarfv1.DeployedComponent) []zarf.DeployedComponent {
	result := make([]zarf.DeployedComponent, 0, len(comps))
	for _, c := range comps {
		result = append(result, zarf.DeployedComponent{
			Name:               c.Name,
			Status:             zarf.ComponentStatus(c.Status),
			InstalledCharts:    convertCharts(c.InstalledCharts),
			ObservedGeneration: int(c.ObservedGeneration),
		})
	}
	return result
}

func convertCharts(charts []*zarfv1.InstalledChart) []zarf.InstalledChart {
	result := make([]zarf.InstalledChart, 0, len(charts))
	for _, c := range charts {
		result = append(result, zarf.InstalledChart{
			Namespace: c.Namespace,
			ChartName: c.ChartName,
			Status:    zarf.ChartStatus(c.Status),
		})
	}
	return result
}

func convertPackageInfo(pkg *zarfv1.PackageInfo) *zarf.PackageInfo {
	if pkg == nil {
		return nil
	}
	return &zarf.PackageInfo{
		Name:               pkg.Name,
		Version:            pkg.Version,
		Generation:         int(pkg.Generation),
		CLIVersion:         pkg.CliVersion,
		DeployedComponents: convertComponents(pkg.DeployedComponents),
		NamespaceOverride:  pkg.NamespaceOverride,
	}
}
