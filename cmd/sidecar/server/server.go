package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/logger"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/packager/filters"
	"github.com/zarf-dev/zarf/src/pkg/state"
	"github.com/zarf-dev/zarf/src/pkg/zoci"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	zarfv1 "github.com/enel1221/zarf-operator/pkg/zarf/v1"
)

type ZarfServer struct {
	zarfv1.UnimplementedZarfServiceServer
	baseLogger *slog.Logger
	baseConfig logger.Config
	version    string
	cachePath  string
}

func NewZarfServer(baseLogger *slog.Logger, baseConfig logger.Config, version string) *ZarfServer {
	if baseLogger == nil {
		baseLogger = logger.Default()
	}

	cachePath := os.Getenv("ZARF_CACHE_PATH")
	if cachePath == "" {
		cachePath = "/tmp/cache"
	}

	return &ZarfServer{
		baseLogger: baseLogger,
		baseConfig: baseConfig,
		version:    version,
		cachePath:  cachePath,
	}
}

func (s *ZarfServer) loggerForRequest(ctx context.Context, req *zarfv1.DeployRequest) (*slog.Logger, context.Context) {
	cfg := s.baseConfig
	if req != nil {
		if req.LogLevel != "" {
			level, err := logger.ParseLevel(req.LogLevel)
			if err != nil {
				s.baseLogger.Warn("invalid log level override", "level", req.LogLevel, "error", err)
			} else if level < cfg.Level {
				cfg.Level = level
			}
		}
		if req.LogFormat != "" {
			cfg.Format = logger.Format(req.LogFormat)
		}
		if req.NoColor {
			cfg.Color = false
		}
	}

	log, err := logger.New(cfg)
	if err != nil {
		s.baseLogger.Warn("failed to create request logger, using base logger", "error", err)
		log = s.baseLogger
	}

	ctx = logger.WithContext(ctx, log)
	return log, ctx
}

func (s *ZarfServer) baseLoggerWithContext(ctx context.Context) (*slog.Logger, context.Context) {
	log := s.baseLogger
	if log == nil {
		log = logger.Default()
	}
	ctx = logger.WithContext(ctx, log)
	return log, ctx
}

func (s *ZarfServer) Deploy(ctx context.Context, req *zarfv1.DeployRequest) (*zarfv1.DeployResponse, error) {
	log, ctx := s.loggerForRequest(ctx, req)

	if strings.TrimSpace(req.Source) == "" {
		return nil, status.Error(codes.InvalidArgument, "source is required")
	}

	// Apply Zarf defaults for headless operation
	if req.Retries == 0 {
		req.Retries = 3
	}
	if req.Timeout == nil || req.Timeout.AsDuration() == 0 {
		req.Timeout = durationpb.New(15 * time.Minute)
	}
	if req.OciConcurrency == 0 {
		req.OciConcurrency = 6
	}

	// Auto-detect architecture from cluster node labels
	if req.Architecture == "" {
		arch, err := s.getClusterArchitecture(ctx)
		if err != nil {
			log.Warn("failed to detect cluster arch, using runtime", "error", err)
			req.Architecture = runtime.GOARCH
		} else {
			req.Architecture = arch
			log.Info("auto-detected cluster arch", "arch", arch)
		}
	}

	log.Info("deploy request received",
		"source", req.Source,
		"components", req.Components,
		"retries", req.Retries,
		"namespace", req.NamespaceOverride,
		"timeout", req.Timeout.AsDuration(),
		"ociConcurrency", req.OciConcurrency,
	)

	// Inject per-request registry credentials if provided.
	// Writes a temp Docker config so Zarf's ORAS layer picks it up via $DOCKER_CONFIG.
	if len(req.RegistryCredentialJson) > 0 {
		tmpDir, err := os.MkdirTemp("", "zarf-docker-config-*")
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create temp docker config dir: %v", err)
		}
		defer func() {
			if err := os.RemoveAll(tmpDir); err != nil {
				log.Warn("failed to remove temp docker config dir", "error", err)
			}
		}()

		if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), req.RegistryCredentialJson, 0600); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to write temp docker config: %v", err)
		}

		origDockerConfig, hadEnv := os.LookupEnv("DOCKER_CONFIG")
		if err := os.Setenv("DOCKER_CONFIG", tmpDir); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to set DOCKER_CONFIG: %v", err)
		}
		defer func() {
			if hadEnv {
				_ = os.Setenv("DOCKER_CONFIG", origDockerConfig)
			} else {
				_ = os.Unsetenv("DOCKER_CONFIG")
			}
		}()
		log.Info("configured registry credentials from secret", "dockerConfigDir", tmpDir)
	}

	// Load the package
	loadOpts := packager.LoadOptions{
		Shasum:         req.Shasum,
		Architecture:   req.Architecture,
		PublicKeyPath:  req.PublicKeyPath,
		Verify:         !req.SkipSignatureValidation,
		OCIConcurrency: int(req.OciConcurrency),
		Filter:         filters.Empty(),
		LayersSelector: zoci.AllLayers,
		CachePath:      s.cachePath,
		RemoteOptions: packager.RemoteOptions{
			PlainHTTP:             req.PlainHttp,
			InsecureSkipTLSVerify: req.InsecureSkipTlsVerify,
		},
	}
	log.Debug("loading package", "source", req.Source, "architecture", req.Architecture)

	pkgLayout, err := packager.LoadPackage(ctx, req.Source, loadOpts)
	if err != nil {
		log.Error("failed to load package", "error", err, "source", req.Source)
		return nil, status.Errorf(codes.Internal, "failed to load package: %v", err)
	}
	defer func() {
		if err := pkgLayout.Cleanup(); err != nil {
			log.Error("failed to cleanup package", "error", err)
		}
	}()

	// Apply component filter if components were specified
	if len(req.Components) > 0 {
		componentFilter := filters.Combine(
			filters.ByLocalOS(runtime.GOOS),
			filters.ForDeploy(strings.Join(req.Components, ","), false),
		)
		pkgLayout.Pkg.Components, err = componentFilter.Apply(pkgLayout.Pkg)
		if err != nil {
			log.Error("failed to filter components", "error", err, "components", req.Components)
			return nil, status.Errorf(codes.InvalidArgument, "failed to filter components: %v", err)
		}
		log.Info("filtered components", "requested", req.Components, "selected", len(pkgLayout.Pkg.Components))
	}

	// Handle YOLO mode - deploy without zarf init, pull images from upstream
	if req.YoloMode {
		pkgLayout.Pkg.Metadata.YOLO = true
		log.Info("YOLO mode enabled - deploying without zarf init")
	}

	// Deploy the package
	deployOpts := packager.DeployOptions{
		SetVariables:           req.SetVariables,
		AdoptExistingResources: req.AdoptExistingResources,
		Timeout:                req.Timeout.AsDuration(),
		Retries:                int(req.Retries),
		NamespaceOverride:      req.NamespaceOverride,
		OCIConcurrency:         int(req.OciConcurrency),
		IsInteractive:          false,
		SkipVersionCheck:       req.SkipVersionCheck,
		RemoteOptions: packager.RemoteOptions{
			PlainHTTP:             req.PlainHttp,
			InsecureSkipTLSVerify: req.InsecureSkipTlsVerify,
		},
	}
	log.Debug("deploying package", "package", pkgLayout.Pkg.Metadata.Name, "version", pkgLayout.Pkg.Metadata.Version)

	result, err := packager.Deploy(ctx, pkgLayout, deployOpts)
	if err != nil {
		log.Error("deployment failed", "error", err, "package", pkgLayout.Pkg.Metadata.Name)
		return nil, status.Errorf(codes.Internal, "deployment failed: %v", err)
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

	log.Info(
		"deployment completed",
		"package", pkgLayout.Pkg.Metadata.Name,
		"version", pkgLayout.Pkg.Metadata.Version,
		"components", len(components),
	)

	return &zarfv1.DeployResponse{
		PackageName:        pkgLayout.Pkg.Metadata.Name,
		Version:            pkgLayout.Pkg.Metadata.Version,
		Generation:         1, // Will be set from cluster state
		DeployedComponents: components,
	}, nil
}

func (s *ZarfServer) GetDeployedPackage(
	ctx context.Context,
	req *zarfv1.GetDeployedPackageRequest,
) (*zarfv1.GetDeployedPackageResponse, error) {
	log, ctx := s.baseLoggerWithContext(ctx)
	log.Debug("get deployed package", "package", req.PackageName)

	c, err := cluster.New(ctx)
	if err != nil {
		log.Error("failed to connect to cluster", "error", err)
		return nil, status.Errorf(codes.Unavailable, "failed to connect to cluster: %v", err)
	}

	deployedPkg, err := c.GetDeployedPackage(ctx, req.PackageName)
	if err != nil {
		log.Error("failed to get package", "error", err, "package", req.PackageName)
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, status.Errorf(codes.NotFound, "package %q not found: %v", req.PackageName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get package: %v", err)
	}
	log.Info("retrieved deployed package", "package", req.PackageName)

	return &zarfv1.GetDeployedPackageResponse{
		Package: convertPackageInfo(deployedPkg),
	}, nil
}

func (s *ZarfServer) ListDeployedPackages(
	ctx context.Context,
	req *zarfv1.ListDeployedPackagesRequest,
) (*zarfv1.ListDeployedPackagesResponse, error) {
	log, ctx := s.baseLoggerWithContext(ctx)
	log.Debug("list deployed packages")

	c, err := cluster.New(ctx)
	if err != nil {
		log.Error("failed to connect to cluster", "error", err)
		return nil, status.Errorf(codes.Unavailable, "failed to connect to cluster: %v", err)
	}

	packages, err := c.GetDeployedZarfPackages(ctx)
	if err != nil {
		log.Error("failed to list packages", "error", err)
		return nil, status.Errorf(codes.Internal, "failed to list packages: %v", err)
	}
	log.Info("listed deployed packages", "count", len(packages))

	result := make([]*zarfv1.PackageInfo, 0, len(packages))
	for _, pkg := range packages {
		result = append(result, convertPackageInfo(&pkg))
	}

	return &zarfv1.ListDeployedPackagesResponse{Packages: result}, nil
}

func (s *ZarfServer) Remove(ctx context.Context, req *zarfv1.RemoveRequest) (*zarfv1.RemoveResponse, error) {
	log, ctx := s.baseLoggerWithContext(ctx)
	log.Info("remove request received", "package", req.PackageName, "components", req.Components)

	if strings.TrimSpace(req.PackageName) == "" {
		return nil, status.Error(codes.InvalidArgument, "package_name is required")
	}

	// Connect to cluster
	c, err := cluster.New(ctx)
	if err != nil {
		log.Error("failed to connect to cluster", "error", err)
		return nil, status.Errorf(codes.Unavailable, "failed to connect to cluster: %v", err)
	}

	// Get deployed package to retrieve the full package definition
	deployedPkg, err := c.GetDeployedPackage(ctx, req.PackageName)
	if err != nil {
		log.Error("failed to get deployed package", "error", err, "package", req.PackageName)
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, status.Errorf(codes.NotFound, "package %q not found: %v", req.PackageName, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get deployed package: %v", err)
	}

	// Set timeout default
	timeout := 15 * time.Minute
	if req.Timeout != nil && req.Timeout.AsDuration() > 0 {
		timeout = req.Timeout.AsDuration()
	}

	// Build remove options
	removeOpts := packager.RemoveOptions{
		Cluster:           c,
		Timeout:           timeout,
		NamespaceOverride: req.NamespaceOverride,
		SkipVersionCheck:  req.SkipVersionCheck,
	}

	// Perform removal
	if err := packager.Remove(ctx, deployedPkg.Data, removeOpts); err != nil {
		log.Error("removal failed", "error", err, "package", req.PackageName)
		return nil, status.Errorf(codes.Internal, "removal failed: %v", err)
	}

	log.Info("package removed successfully", "package", req.PackageName)
	return &zarfv1.RemoveResponse{}, nil
}

func (s *ZarfServer) GetPackageMetadata(
	ctx context.Context,
	req *zarfv1.GetPackageMetadataRequest,
) (*zarfv1.GetPackageMetadataResponse, error) {
	log, ctx := s.baseLoggerWithContext(ctx)
	log.Debug("get package metadata", "source", req.Source)

	// Load package metadata without deploying
	if strings.TrimSpace(req.Source) == "" {
		return nil, status.Error(codes.InvalidArgument, "source is required")
	}

	loadOpts := packager.LoadOptions{}
	pkgLayout, err := packager.LoadPackage(ctx, req.Source, loadOpts)
	if err != nil {
		log.Error("failed to load package metadata", "error", err, "source", req.Source)
		return nil, status.Errorf(codes.Internal, "failed to load package metadata: %v", err)
	}
	defer func() {
		if err := pkgLayout.Cleanup(); err != nil {
			log.Error("failed to cleanup package", "error", err)
		}
	}()

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
	log, _ := s.baseLoggerWithContext(ctx)
	log.Debug("health check")

	// Validate cluster connectivity
	healthy := true
	message := ""

	c, err := cluster.New(ctx)
	if err != nil {
		healthy = false
		message = fmt.Sprintf("cluster connectivity failed: %v", err)
		log.Warn("health check failed", "error", err)
	} else {
		// Connectivity test
		_, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			healthy = false
			message = fmt.Sprintf("cluster API test failed: %v", err)
		}
	}

	return &zarfv1.HealthResponse{
		Healthy: healthy,
		Version: s.version,
		Message: message,
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

// getClusterArchitecture queries the cluster to determine the node architecture
func (s *ZarfServer) getClusterArchitecture(ctx context.Context) (string, error) {
	c, err := cluster.New(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to connect to cluster: %w", err)
	}

	nodeList, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodeList.Items) == 0 {
		return runtime.GOARCH, nil
	}

	arch := nodeList.Items[0].Status.NodeInfo.Architecture
	if arch == "" {
		return runtime.GOARCH, nil
	}

	return arch, nil
}
