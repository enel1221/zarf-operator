package grpc

import (
	"context"
	"fmt"

	"github.com/enel1221/zarf-operator/pkg/zarf"
	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	conn, err := grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to zarf sidecar: %w", err)
	}

	return &Client{
		conn:   conn,
		client: zarfv1.NewZarfServiceClient(conn),
	}, nil
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
	}

	resp, err := c.client.Deploy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("deploy failed: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("deploy error: %s", resp.Error)
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
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("get deployed package error: %s", resp.Error)
	}

	return convertPackageInfo(resp.Package), nil
}

// ListDeployedPackages returns all deployed packages
func (c *Client) ListDeployedPackages(ctx context.Context) ([]zarf.PackageInfo, error) {
	resp, err := c.client.ListDeployedPackages(ctx, &zarfv1.ListDeployedPackagesRequest{})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("list deployed packages error: %s", resp.Error)
	}

	packages := make([]zarf.PackageInfo, 0, len(resp.Packages))
	for _, pkg := range resp.Packages {
		packages = append(packages, *convertPackageInfo(pkg))
	}
	return packages, nil
}

// Remove removes a deployed package
func (c *Client) Remove(ctx context.Context, opts zarf.RemoveOptions) error {
	resp, err := c.client.Remove(ctx, &zarfv1.RemoveRequest{
		PackageName: opts.PackageName,
		Components:  opts.Components,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("remove error: %s", resp.Error)
	}
	return nil
}

// GetPackageMetadata fetches metadata from a package source
func (c *Client) GetPackageMetadata(ctx context.Context, source string) (*zarf.PackageMetadata, error) {
	resp, err := c.client.GetPackageMetadata(ctx, &zarfv1.GetPackageMetadataRequest{
		Source: source,
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("get package metadata error: %s", resp.Error)
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
