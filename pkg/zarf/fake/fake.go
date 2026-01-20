package fake

import (
	"context"
	"sync"

	"github.com/enel1221/zarf-operator/pkg/zarf"
)

var _ zarf.Client = (*Client)(nil)

// Client is a fake zarf.Client for testing
type Client struct {
	mu sync.RWMutex

	DeployFn               func(ctx context.Context, opts zarf.DeployOptions) (*zarf.DeployResult, error)
	RemoveFn               func(ctx context.Context, opts zarf.RemoveOptions) error
	GetDeployedPackageFn   func(ctx context.Context, packageName string) (*zarf.PackageInfo, error)
	ListDeployedPackagesFn func(ctx context.Context) ([]zarf.PackageInfo, error)
	GetPackageMetadataFn   func(ctx context.Context, source string) (*zarf.PackageMetadata, error)

	deployedPackages map[string]*zarf.PackageInfo
}

// New creates a new fake client with sensible defaults
func New() *Client {
	c := &Client{
		deployedPackages: make(map[string]*zarf.PackageInfo),
	}

	// Set default implementations
	c.DeployFn = func(ctx context.Context, opts zarf.DeployOptions) (*zarf.DeployResult, error) {
		return &zarf.DeployResult{
			PackageName: "test-package",
			Version:     "0.1.0",
			Generation:  1,
		}, nil
	}

	c.RemoveFn = func(ctx context.Context, opts zarf.RemoveOptions) error {
		return nil
	}

	c.GetDeployedPackageFn = func(ctx context.Context, packageName string) (*zarf.PackageInfo, error) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		if pkg, ok := c.deployedPackages[packageName]; ok {
			return pkg, nil
		}
		return nil, nil
	}

	c.ListDeployedPackagesFn = func(ctx context.Context) ([]zarf.PackageInfo, error) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		packages := make([]zarf.PackageInfo, 0, len(c.deployedPackages))
		for _, pkg := range c.deployedPackages {
			packages = append(packages, *pkg)
		}
		return packages, nil
	}

	c.GetPackageMetadataFn = func(ctx context.Context, source string) (*zarf.PackageMetadata, error) {
		return &zarf.PackageMetadata{
			Name:    "test-package",
			Version: "0.1.0",
		}, nil
	}

	return c
}

// Builder pattern methods for test setup

// WithDeploy configures the Deploy behavior
func (c *Client) WithDeploy(result *zarf.DeployResult, err error) *Client {
	c.DeployFn = func(context.Context, zarf.DeployOptions) (*zarf.DeployResult, error) {
		return result, err
	}
	return c
}

// WithDeployFunc configures a custom Deploy function
func (c *Client) WithDeployFunc(fn func(context.Context, zarf.DeployOptions) (*zarf.DeployResult, error)) *Client {
	c.DeployFn = fn
	return c
}

// WithRemove configures the Remove behavior
func (c *Client) WithRemove(err error) *Client {
	c.RemoveFn = func(context.Context, zarf.RemoveOptions) error {
		return err
	}
	return c
}

// WithGetDeployedPackage configures the GetDeployedPackage behavior
func (c *Client) WithGetDeployedPackage(pkg *zarf.PackageInfo, err error) *Client {
	c.GetDeployedPackageFn = func(context.Context, string) (*zarf.PackageInfo, error) {
		return pkg, err
	}
	return c
}

// WithPackageMetadata configures the GetPackageMetadata behavior
func (c *Client) WithPackageMetadata(meta *zarf.PackageMetadata, err error) *Client {
	c.GetPackageMetadataFn = func(context.Context, string) (*zarf.PackageMetadata, error) {
		return meta, err
	}
	return c
}

// AddDeployedPackage adds a package to the fake state
func (c *Client) AddDeployedPackage(pkg *zarf.PackageInfo) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deployedPackages[pkg.Name] = pkg
	return c
}

// Reset clears all state
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deployedPackages = make(map[string]*zarf.PackageInfo)
}

// Interface implementation

func (c *Client) Deploy(ctx context.Context, opts zarf.DeployOptions) (*zarf.DeployResult, error) {
	return c.DeployFn(ctx, opts)
}

func (c *Client) Remove(ctx context.Context, opts zarf.RemoveOptions) error {
	return c.RemoveFn(ctx, opts)
}

func (c *Client) GetDeployedPackage(ctx context.Context, packageName string) (*zarf.PackageInfo, error) {
	return c.GetDeployedPackageFn(ctx, packageName)
}

func (c *Client) ListDeployedPackages(ctx context.Context) ([]zarf.PackageInfo, error) {
	return c.ListDeployedPackagesFn(ctx)
}

func (c *Client) GetPackageMetadata(ctx context.Context, source string) (*zarf.PackageMetadata, error) {
	return c.GetPackageMetadataFn(ctx, source)
}

func (c *Client) Close() error {
	return nil
}
