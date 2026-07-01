package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type ociTagResolveOptions struct {
	PlainHTTP              bool
	InsecureSkipTLSVerify  bool
	RegistryCredentialJSON []byte
}

type ociTagResolver interface {
	ListTags(ctx context.Context, source string, opts ociTagResolveOptions) ([]string, error)
}

type defaultOCITagResolver struct{}

const ociTagListTimeout = 30 * time.Second

func (defaultOCITagResolver) ListTags(ctx context.Context, source string, opts ociTagResolveOptions) ([]string, error) {
	sourceRef, err := parseOCISourceForTagList(source)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ociTagListTimeout)
	defer cancel()

	nameOpts := []name.Option{name.WeakValidation}
	if opts.PlainHTTP {
		nameOpts = append(nameOpts, name.Insecure)
	}
	repo, err := name.NewRepository(sourceRef.Repository, nameOpts...)
	if err != nil {
		return nil, fmt.Errorf("parse OCI repository: %w", err)
	}

	remoteOpts := []remote.Option{remote.WithContext(ctx)}
	if len(opts.RegistryCredentialJSON) > 0 {
		keychain, err := newDockerConfigJSONKeychain(opts.RegistryCredentialJSON)
		if err != nil {
			return nil, err
		}
		remoteOpts = append(remoteOpts, remote.WithAuthFromKeychain(keychain))
	}
	if opts.InsecureSkipTLSVerify {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// The user explicitly opted out of TLS verification for this source.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		remoteOpts = append(remoteOpts, remote.WithTransport(transport))
	}

	tags, err := remote.List(repo, remoteOpts...)
	if err != nil {
		return nil, fmt.Errorf("list OCI tags for %s: %w", sourceRef.Repository, err)
	}
	return tags, nil
}

type ociSourceReference struct {
	Repository string
	Tag        string
}

func parseOCISourceForTagList(source string) (ociSourceReference, error) {
	if !strings.HasPrefix(source, "oci://") {
		return ociSourceReference{}, fmt.Errorf("source must start with oci://")
	}
	trimmed := strings.TrimPrefix(source, "oci://")
	if strings.Contains(trimmed, "@") {
		return ociSourceReference{}, fmt.Errorf("source must use a tag, not a digest")
	}
	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	if lastColon <= lastSlash || lastColon == len(trimmed)-1 {
		return ociSourceReference{}, fmt.Errorf("source must include an explicit tag")
	}

	ref, err := name.ParseReference(trimmed, name.WeakValidation)
	if err != nil {
		return ociSourceReference{}, fmt.Errorf("parse OCI source: %w", err)
	}
	tag, ok := ref.(name.Tag)
	if !ok {
		return ociSourceReference{}, fmt.Errorf("source must use a tag, not a digest")
	}

	return ociSourceReference{
		Repository: tag.Context().Name(),
		Tag:        tag.TagStr(),
	}, nil
}

type semverUpgradeCandidate struct {
	Tag     string
	Version string
	version *semver.Version
}

var strictSemverTagPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)(-[0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?$`)

func strictSemverVersion(tag string) (*semver.Version, error) {
	if !strictSemverTagPattern.MatchString(tag) {
		return nil, fmt.Errorf("tag must be a full semantic version")
	}
	version, err := semver.NewVersion(tag)
	if err != nil {
		return nil, err
	}
	return version, nil
}

func selectLatestSemverUpgrade(currentTag string, tags []string, constraintExpr string) (semverUpgradeCandidate, bool, error) {
	currentVersion, err := strictSemverVersion(currentTag)
	if err != nil {
		return semverUpgradeCandidate{}, false, fmt.Errorf("parse current semantic version %q: %w", currentTag, err)
	}

	var constraint *semver.Constraints
	if strings.TrimSpace(constraintExpr) != "" {
		constraint, err = semver.NewConstraint(constraintExpr)
		if err != nil {
			return semverUpgradeCandidate{}, false, fmt.Errorf("parse semantic version constraint %q: %w", constraintExpr, err)
		}
	}

	var best semverUpgradeCandidate
	for _, tag := range tags {
		version, err := strictSemverVersion(tag)
		if err != nil {
			continue
		}
		if version.Prerelease() != "" {
			continue
		}
		if !version.GreaterThan(currentVersion) {
			continue
		}
		if constraint != nil && !constraint.Check(version) {
			continue
		}
		if best.version == nil || version.GreaterThan(best.version) {
			best = semverUpgradeCandidate{
				Tag:     tag,
				Version: version.String(),
				version: version,
			}
		}
	}

	if best.version == nil {
		return semverUpgradeCandidate{}, false, nil
	}
	return best, true, nil
}

type dockerConfigJSONKeychain struct {
	auths map[string]authn.AuthConfig
}

func newDockerConfigJSONKeychain(data []byte) (authn.Keychain, error) {
	var cfg struct {
		Auths             map[string]authn.AuthConfig `json:"auths"`
		CredentialStore   string                      `json:"credsStore"`
		CredentialHelpers map[string]string           `json:"credHelpers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse registry credential docker config: %w", err)
	}
	if strings.TrimSpace(cfg.CredentialStore) != "" || len(cfg.CredentialHelpers) > 0 {
		return nil, fmt.Errorf("registry credential docker config must use inline auths; credential helpers are not supported")
	}
	return dockerConfigJSONKeychain{auths: cfg.Auths}, nil
}

func (k dockerConfigJSONKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	for _, key := range []string{target.String(), target.RegistryStr()} {
		if key == name.DefaultRegistry {
			key = authn.DefaultAuthKey
		}
		if cfg, ok := k.authForKey(key); ok {
			return authn.FromConfig(cfg), nil
		}
	}
	return authn.Anonymous, nil
}

func (k dockerConfigJSONKeychain) authForKey(key string) (authn.AuthConfig, bool) {
	for _, candidate := range dockerConfigAuthKeys(key) {
		cfg, ok := k.auths[candidate]
		if !ok {
			continue
		}
		if authConfigEmpty(cfg) {
			continue
		}
		return cfg, true
	}
	return authn.AuthConfig{}, false
}

func dockerConfigAuthKeys(key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	trimmed := strings.TrimRight(key, "/")
	keys := []string{key}
	if trimmed != key {
		keys = append(keys, trimmed)
	}
	keys = append(keys, "https://"+trimmed, "https://"+trimmed+"/", "http://"+trimmed, "http://"+trimmed+"/")
	return keys
}

func authConfigEmpty(cfg authn.AuthConfig) bool {
	return cfg.Username == "" &&
		cfg.Password == "" &&
		cfg.Auth == "" &&
		cfg.IdentityToken == "" &&
		cfg.RegistryToken == ""
}
