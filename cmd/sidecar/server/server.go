package server

import (
	"context"
	"fmt"

	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/state"

	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
)

type ZarfServer struct {
	zarfv1.UnimplementedZarfServiceServer
}

func NewZarfServer() *ZarfServer {
	return &ZarfServer{}
}

func (s *ZarfServer) Deploy(ctx context.Context, req *zarfv1.DeployRequest) (*zarfv1.DeployResponse, error) {
	// Load the package
	loadOpts := packager.LoadOptions{
		Shasum:         req.Shasum,
		Architecture:   req.Architecture,
		PublicKeyPath:  req.PublicKeyPath,
		Verify:         !req.SkipSignatureValidation,
		OCIConcurrency: int(req.OciConcurrency),
	}

	pkgLayout, err := packager.LoadPackage(ctx, req.Source, loadOpts)
	if err != nil {
		return &zarfv1.DeployResponse{Error: fmt.Sprintf("failed to load package: %v", err)}, nil
	}
	defer pkgLayout.Cleanup()

	// Deploy the package
	deployOpts := packager.DeployOptions{
		SetVariables:           req.SetVariables,
		AdoptExistingResources: req.AdoptExistingResources,
		Timeout:                req.Timeout.AsDuration(),
		Retries:                int(req.Retries),
		NamespaceOverride:      req.NamespaceOverride,
		OCIConcurrency:         int(req.OciConcurrency),
	}

	result, err := packager.Deploy(ctx, pkgLayout, deployOpts)
	if err != nil {
		return &zarfv1.DeployResponse{Error: fmt.Sprintf("deployment failed: %v", err)}, nil
	}

	// Convert result
	components := make([]*zarfv1.DeployedComponent, 0, len(result.DeployedComponents))
	for _, c := range result.DeployedComponents {
		charts := make([]*zarfv1.InstalledChart, 0, len(c.InstalledCharts))
		for _, ch := range c.InstalledCharts {
			charts = append(charts, &zarfv1.InstalledChart{
				Namespace: ch.Namespace,
				ChartName: ch.ChartName,
				Status:    string(ch.Status),
			})
		}
		components = append(components, &zarfv1.DeployedComponent{
			Name:               c.Name,
			Status:             string(c.Status),
			InstalledCharts:    charts,
			ObservedGeneration: int32(c.ObservedGeneration),
		})
	}

	return &zarfv1.DeployResponse{
		PackageName:        pkgLayout.Pkg.Metadata.Name,
		Version:            pkgLayout.Pkg.Metadata.Version,
		Generation:         1, // Will be set from cluster state
		DeployedComponents: components,
	}, nil
}

func (s *ZarfServer) GetDeployedPackage(ctx context.Context, req *zarfv1.GetDeployedPackageRequest) (*zarfv1.GetDeployedPackageResponse, error) {
	c, err := cluster.New(ctx)
	if err != nil {
		return &zarfv1.GetDeployedPackageResponse{Error: fmt.Sprintf("failed to connect to cluster: %v", err)}, nil
	}

	deployedPkg, err := c.GetDeployedPackage(ctx, req.PackageName)
	if err != nil {
		return &zarfv1.GetDeployedPackageResponse{Error: fmt.Sprintf("failed to get package: %v", err)}, nil
	}

	return &zarfv1.GetDeployedPackageResponse{
		Package: convertPackageInfo(deployedPkg),
	}, nil
}

func (s *ZarfServer) ListDeployedPackages(ctx context.Context, req *zarfv1.ListDeployedPackagesRequest) (*zarfv1.ListDeployedPackagesResponse, error) {
	c, err := cluster.New(ctx)
	if err != nil {
		return &zarfv1.ListDeployedPackagesResponse{Error: fmt.Sprintf("failed to connect to cluster: %v", err)}, nil
	}

	packages, err := c.GetDeployedZarfPackages(ctx)
	if err != nil {
		return &zarfv1.ListDeployedPackagesResponse{Error: fmt.Sprintf("failed to list packages: %v", err)}, nil
	}

	result := make([]*zarfv1.PackageInfo, 0, len(packages))
	for _, pkg := range packages {
		result = append(result, convertPackageInfo(&pkg))
	}

	return &zarfv1.ListDeployedPackagesResponse{Packages: result}, nil
}

func (s *ZarfServer) Remove(ctx context.Context, req *zarfv1.RemoveRequest) (*zarfv1.RemoveResponse, error) {
	// Implementation uses packager.Remove
	// Similar pattern to Deploy
	return &zarfv1.RemoveResponse{}, nil
}

func (s *ZarfServer) GetPackageMetadata(ctx context.Context, req *zarfv1.GetPackageMetadataRequest) (*zarfv1.GetPackageMetadataResponse, error) {
	// Load package metadata without deploying
	loadOpts := packager.LoadOptions{}
	pkgLayout, err := packager.LoadPackage(ctx, req.Source, loadOpts)
	if err != nil {
		return &zarfv1.GetPackageMetadataResponse{Error: fmt.Sprintf("failed to load: %v", err)}, nil
	}
	defer pkgLayout.Cleanup()

	components := make([]string, 0, len(pkgLayout.Pkg.Components))
	for _, c := range pkgLayout.Pkg.Components {
		components = append(components, c.Name)
	}

	return &zarfv1.GetPackageMetadataResponse{
		Metadata: &zarfv1.PackageMetadata{
			Name:         pkgLayout.Pkg.Metadata.Name,
			Version:      pkgLayout.Pkg.Metadata.Version,
			Description:  pkgLayout.Pkg.Metadata.Description,
			Components:   components,
			Architecture: pkgLayout.Pkg.Build.Architecture,
		},
	}, nil
}

func (s *ZarfServer) Health(ctx context.Context, req *zarfv1.HealthRequest) (*zarfv1.HealthResponse, error) {
	return &zarfv1.HealthResponse{
		Healthy: true,
		Version: "1.0.0",
	}, nil
}

func convertPackageInfo(pkg *state.DeployedPackage) *zarfv1.PackageInfo {
	if pkg == nil {
		return nil
	}

	components := make([]*zarfv1.DeployedComponent, 0, len(pkg.DeployedComponents))
	for _, c := range pkg.DeployedComponents {
		charts := make([]*zarfv1.InstalledChart, 0, len(c.InstalledCharts))
		for _, ch := range c.InstalledCharts {
			charts = append(charts, &zarfv1.InstalledChart{
				Namespace: ch.Namespace,
				ChartName: ch.ChartName,
				Status:    string(ch.Status),
			})
		}
		components = append(components, &zarfv1.DeployedComponent{
			Name:               c.Name,
			Status:             string(c.Status),
			InstalledCharts:    charts,
			ObservedGeneration: int32(c.ObservedGeneration),
		})
	}

	return &zarfv1.PackageInfo{
		Name:               pkg.Name,
		Version:            pkg.Data.Metadata.Version,
		Generation:         int32(pkg.Generation),
		CliVersion:         pkg.CLIVersion,
		DeployedComponents: components,
		NamespaceOverride:  pkg.NamespaceOverride,
	}
}
