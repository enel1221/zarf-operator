package zarf

import (
	"context"
	"time"
)

// DeployOptions contains options for deploying a Zarf package
type DeployOptions struct {
	Source                  string
	Components              []string
	SetVariables            map[string]string
	AdoptExistingResources  bool
	Timeout                 time.Duration
	Retries                 int
	NamespaceOverride       string
	Shasum                  string
	SkipSignatureValidation bool
	Architecture            string
	OCIConcurrency          int
	PublicKeyPath           string
	LogLevel                string
	LogFormat               string
	NoColor                 bool
	PlainHTTP				bool
	InsecureSkipTLSVerify	bool
	SkipVersionCheck		bool
}

// DeployResult contains the result of a deployment
type DeployResult struct {
	PackageName        string
	Version            string
	Generation         int
	DeployedComponents []DeployedComponent
}

// DeployedComponent represents a deployed component
type DeployedComponent struct {
	Name               string
	Status             ComponentStatus
	InstalledCharts    []InstalledChart
	ObservedGeneration int
}

// ComponentStatus represents the status of a component
type ComponentStatus string

const (
	ComponentStatusSucceeded ComponentStatus = "Succeeded"
	ComponentStatusFailed    ComponentStatus = "Failed"
	ComponentStatusDeploying ComponentStatus = "Deploying"
	ComponentStatusRemoving  ComponentStatus = "Removing"
)

// InstalledChart represents an installed Helm Chart
type InstalledChart struct {
	Namespace string
	ChartName string
	Status    ChartStatus
}

// ChartStatus represents the status of a chart
type ChartStatus string

const (
	ChartStatusSucceeded ChartStatus = "Succeeded"
	ChartStatusFailed    ChartStatus = "Failed"
)

// PackageInfo contains information about a deployed package
type PackageInfo struct {
	Name               string
	Version            string
	Generation         int
	CLIVersion         string
	DeployedComponents []DeployedComponent
	NamespaceOverride  string
}

// RemoveOptions contains options for removing a Zarf package
type RemoveOptions struct {
	PackageName       string
	Components        []string
	Timeout		      time.Duration
	NamespaceOverride string
	SkipVersionCheck  bool
}

// Client defines the interface for interacting with Zarf
type Client interface {
	// Deploy deploys a Zarf package
	Deploy(ctx context.Context, opts DeployOptions) (*DeployResult, error)

	// Remove removes a deployed Zarf package
	Remove(ctx context.Context, opts RemoveOptions) error

	// GetDeployedPackage returns information about a deployed package
	GetDeployedPackage(ctx context.Context, packageName string) (*PackageInfo, error)

	// ListDeployedPackages returns all deployed packages
	ListDeployedPackages(ctx context.Context) ([]PackageInfo, error)

	// GetPackageMetadata fetches metadata from a package source without deploying
	GetPackageMetadata(ctx context.Context, source string) (*PackageMetadata, error)

	// Close closes the client connection
	Close() error
}

type PackageMetadata struct {
	Name         string
	Version      string
	Description  string
	Components   []string
	Architecture string
}
