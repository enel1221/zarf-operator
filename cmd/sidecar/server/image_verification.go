package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/images"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
	"github.com/zarf-dev/zarf/src/pkg/state"
	"github.com/zarf-dev/zarf/src/pkg/transform"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

var registryResolveResponseHeaderTimeout = 10 * time.Second

type expectedImageRef struct {
	component string
	ref       string
}

type imageVerificationError struct {
	component string
	ref       string
	err       error
}

func (e *imageVerificationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("missing target image %s: %v", e.ref, e.err)
}

func (e *imageVerificationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func verifyDeployedPackageImages(ctx context.Context, pkgLayout *layout.PackageLayout, opts packager.DeployOptions) error {
	if pkgLayout == nil || pkgLayout.Pkg.Metadata.YOLO || !pkgLayout.Pkg.HasImages() {
		return nil
	}

	registryInfo, clusterClient, err := registryInfoForImageVerification(ctx, opts)
	if err != nil {
		return err
	}

	registryAddress := registryInfo.Address
	var tunnel *cluster.Tunnel
	if clusterClient != nil {
		registryAddress, tunnel, err = clusterClient.ConnectToZarfRegistryEndpoint(ctx, registryInfo)
		if err != nil {
			return fmt.Errorf("connect to target registry: %w", err)
		}
		if tunnel != nil {
			defer tunnel.Close()
		}
	}

	refs, err := expectedDeployedImageRefs(registryAddress, pkgLayout, registryInfo)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		resolve := func() error {
			return resolveRegistryImage(ctx, ref.ref, registryInfo, opts)
		}
		if tunnel != nil {
			resolve = func() error {
				return tunnel.Wrap(func() error {
					return resolveRegistryImage(ctx, ref.ref, registryInfo, opts)
				})
			}
		}
		if err := resolve(); err != nil {
			return &imageVerificationError{
				component: ref.component,
				ref:       ref.ref,
				err:       err,
			}
		}
	}
	return nil
}

func registryInfoForImageVerification(
	ctx context.Context,
	opts packager.DeployOptions,
) (state.RegistryInfo, *cluster.Cluster, error) {
	clusterClient, err := cluster.New(ctx)
	if err != nil {
		if strings.TrimSpace(opts.RegistryInfo.Address) == "" {
			return state.RegistryInfo{}, nil, fmt.Errorf("connect to cluster for target registry verification: %w", err)
		}
		return opts.RegistryInfo, nil, nil
	}

	zarfState, stateErr := clusterClient.LoadState(ctx)
	if stateErr == nil && zarfState != nil && strings.TrimSpace(zarfState.RegistryInfo.Address) != "" {
		return zarfState.RegistryInfo, clusterClient, nil
	}
	if strings.TrimSpace(opts.RegistryInfo.Address) != "" {
		return opts.RegistryInfo, clusterClient, nil
	}
	if stateErr != nil {
		return state.RegistryInfo{}, clusterClient, fmt.Errorf("load zarf state for target registry verification: %w", stateErr)
	}
	return state.RegistryInfo{}, clusterClient, fmt.Errorf("zarf state did not include target registry information")
}

func expectedDeployedImageRefs(
	registryAddress string,
	pkgLayout *layout.PackageLayout,
	registryInfo state.RegistryInfo,
) ([]expectedImageRef, error) {
	if strings.TrimSpace(registryAddress) == "" {
		return nil, fmt.Errorf("target registry address is required for image verification")
	}

	seen := map[string]struct{}{}
	var refs []expectedImageRef
	for _, component := range pkgLayout.Pkg.Components {
		if skipImageVerificationForComponent(pkgLayout, component.Name, registryInfo) {
			continue
		}
		for _, image := range component.GetImages() {
			noChecksumRef, err := transform.ImageTransformHostWithoutChecksum(registryAddress, image)
			if err != nil {
				return nil, fmt.Errorf("build no-checksum target ref for %s: %w", image, err)
			}
			refs = appendUniqueRef(refs, seen, expectedImageRef{component: component.Name, ref: noChecksumRef})

			if component.Name == "zarf-agent" {
				continue
			}
			checksumRef, err := transform.ImageTransformHost(registryAddress, image)
			if err != nil {
				return nil, fmt.Errorf("build checksum target ref for %s: %w", image, err)
			}
			refs = appendUniqueRef(refs, seen, expectedImageRef{component: component.Name, ref: checksumRef})
		}
	}
	return refs, nil
}

func skipImageVerificationForComponent(pkgLayout *layout.PackageLayout, componentName string, registryInfo state.RegistryInfo) bool {
	if !pkgLayout.Pkg.IsInitConfig() {
		return false
	}
	if componentName == "zarf-seed-registry" {
		return true
	}
	if !registryInfo.IsInternal() && (componentName == "zarf-injector" || componentName == "zarf-registry") {
		return true
	}
	return false
}

func appendUniqueRef(refs []expectedImageRef, seen map[string]struct{}, ref expectedImageRef) []expectedImageRef {
	if _, ok := seen[ref.ref]; ok {
		return refs
	}
	seen[ref.ref] = struct{}{}
	return append(refs, ref)
}

func resolveRegistryImage(
	ctx context.Context,
	ref string,
	registryInfo state.RegistryInfo,
	opts packager.DeployOptions,
) error {
	parsed, err := registry.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("parse target image ref: %w", err)
	}

	httpClient, err := registryResolveHTTPClient(opts)
	if err != nil {
		return err
	}
	authClient := &auth.Client{
		Client: httpClient,
		Cache:  auth.NewCache(),
	}
	if registryInfo.PullUsername != "" || registryInfo.PullPassword != "" {
		authClient.Credential = auth.StaticCredential(parsed.Host(), auth.Credential{
			Username: registryInfo.PullUsername,
			Password: registryInfo.PullPassword,
		})
	}
	plainHTTP := opts.RemoteOptions.PlainHTTP
	if isLocalRegistryHost(parsed.Host()) && !plainHTTP {
		plainHTTP, err = images.ShouldUsePlainHTTP(ctx, parsed.Host(), authClient)
		if err != nil {
			return fmt.Errorf("determine target registry protocol: %w", err)
		}
	}

	repo := &remote.Repository{
		PlainHTTP: plainHTTP,
		Client:    authClient,
		Reference: parsed,
	}
	_, err = oras.Resolve(ctx, repo, ref, oras.DefaultResolveOptions)
	return err
}

func registryResolveHTTPClient(opts packager.DeployOptions) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is not an *http.Transport")
	}
	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: opts.RemoteOptions.InsecureSkipTLSVerify} //nolint:gosec // user-requested registry mode.
	transport.ResponseHeaderTimeout = registryResolveResponseHeaderTimeout
	return &http.Client{Transport: retry.NewTransport(transport)}, nil
}

func isLocalRegistryHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func failedImageComponent(err error) string {
	var verificationErr *imageVerificationError
	if errors.As(err, &verificationErr) {
		return verificationErr.component
	}
	return ""
}
