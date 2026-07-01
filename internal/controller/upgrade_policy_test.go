package controller

import (
	"context"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
)

const (
	testSemverBase      = "1.0.0"
	testSemverPatch     = "1.0.1"
	testSemverMinor     = "1.1.0"
	testSemverMinorVTag = "v1.1.0"
	testOCIRepository   = "registry.example.com/team/pkg"
	testOCISource       = "oci://" + testOCIRepository + ":" + testSemverBase
)

func TestSelectLatestSemverUpgradeIgnoresNonSemverAndPrereleaseByDefault(t *testing.T) {
	candidate, ok, err := selectLatestSemverUpgrade(testSemverBase, []string{
		"latest",
		"dev",
		testSemverBase,
		testSemverPatch,
		"2.0.0-alpha.1",
		testSemverMinorVTag,
	}, "")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if !ok {
		t.Fatal("selectLatestSemverUpgrade() did not find an upgrade")
	}
	if candidate.Tag != testSemverMinorVTag {
		t.Fatalf("candidate.Tag = %q, want %q", candidate.Tag, testSemverMinorVTag)
	}
	if candidate.Version != testSemverMinor {
		t.Fatalf("candidate.Version = %q, want %q", candidate.Version, testSemverMinor)
	}
}

func TestSelectLatestSemverUpgradeHonorsConstraint(t *testing.T) {
	candidate, ok, err := selectLatestSemverUpgrade(testSemverBase, []string{
		testSemverPatch,
		testSemverMinor,
		"1.2.0",
	}, "~1.0")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if !ok {
		t.Fatal("selectLatestSemverUpgrade() did not find an upgrade")
	}
	if candidate.Tag != testSemverPatch {
		t.Fatalf("candidate.Tag = %q, want %q", candidate.Tag, testSemverPatch)
	}
}

func TestSelectLatestSemverUpgradeRejectsCoercedTags(t *testing.T) {
	candidate, ok, err := selectLatestSemverUpgrade(testSemverBase, []string{
		"1.2",
		testSemverMinor,
	}, "")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if !ok {
		t.Fatal("selectLatestSemverUpgrade() did not find an upgrade")
	}
	if candidate.Tag != testSemverMinor {
		t.Fatalf("candidate.Tag = %q, want %q", candidate.Tag, testSemverMinor)
	}

	_, ok, err = selectLatestSemverUpgrade(testSemverBase, []string{"1.2"}, "")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if ok {
		t.Fatal("selectLatestSemverUpgrade() accepted a coerced semver tag")
	}
}

func TestSelectLatestSemverUpgradeRejectsOCIIncompatibleSemverTags(t *testing.T) {
	candidate, ok, err := selectLatestSemverUpgrade(testSemverBase, []string{
		"V1.2.0",
		testSemverMinor + "+build.1",
		testSemverPatch,
	}, "")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if !ok {
		t.Fatal("selectLatestSemverUpgrade() did not find an upgrade")
	}
	if candidate.Tag != testSemverPatch {
		t.Fatalf("candidate.Tag = %q, want %q", candidate.Tag, testSemverPatch)
	}

	_, ok, err = selectLatestSemverUpgrade(testSemverBase, []string{"V1.1.0", testSemverMinor + "+build.1"}, "")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if ok {
		t.Fatal("selectLatestSemverUpgrade() accepted an OCI-incompatible semver tag")
	}
}

func TestSelectLatestSemverUpgradeDoesNotDowngradeOrRedeploySameVersion(t *testing.T) {
	_, ok, err := selectLatestSemverUpgrade(testSemverMinor, []string{
		"0.9.9",
		testSemverBase,
		testSemverMinorVTag,
	}, "")
	if err != nil {
		t.Fatalf("selectLatestSemverUpgrade() error = %v", err)
	}
	if ok {
		t.Fatal("selectLatestSemverUpgrade() found an upgrade when only older or equal versions exist")
	}
}

func TestParseOCISourceForTagListRequiresTaggedOCISource(t *testing.T) {
	ref, err := parseOCISourceForTagList(testOCISource)
	if err != nil {
		t.Fatalf("parseOCISourceForTagList() error = %v", err)
	}
	if ref.Repository != testOCIRepository {
		t.Fatalf("Repository = %q, want %q", ref.Repository, testOCIRepository)
	}
	if ref.Tag != testSemverBase {
		t.Fatalf("Tag = %q, want %q", ref.Tag, testSemverBase)
	}

	if _, err := parseOCISourceForTagList("https://" + testOCIRepository + ":" + testSemverBase); err == nil {
		t.Fatal("parseOCISourceForTagList() accepted a non-OCI source")
	}
	if _, err := parseOCISourceForTagList("oci://" + testOCIRepository + "@sha256:abcd"); err == nil {
		t.Fatal("parseOCISourceForTagList() accepted a digest source")
	}
}

func TestDockerConfigJSONKeychainUsesInlineAuthOnly(t *testing.T) {
	keychain, err := newDockerConfigJSONKeychain([]byte(`{"auths":{"registry.example.com":{"username":"user","password":"pass"}}}`))
	if err != nil {
		t.Fatalf("newDockerConfigJSONKeychain() error = %v", err)
	}

	authenticator, err := keychain.Resolve(testAuthResource{full: "registry.example.com/team/pkg", registry: "registry.example.com"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	cfg, err := authn.Authorization(context.Background(), authenticator)
	if err != nil {
		t.Fatalf("Authorization() error = %v", err)
	}
	if cfg.Username != "user" || cfg.Password != "pass" {
		t.Fatalf("resolved auth = %#v, want inline username/password", cfg)
	}

	if _, err := newDockerConfigJSONKeychain([]byte(`{"credsStore":"desktop","auths":{"registry.example.com":{"auth":"dGVzdA=="}}}`)); err == nil {
		t.Fatal("newDockerConfigJSONKeychain() accepted credsStore")
	}
	if _, err := newDockerConfigJSONKeychain([]byte(`{"credHelpers":{"registry.example.com":"osxkeychain"},"auths":{"registry.example.com":{"auth":"dGVzdA=="}}}`)); err == nil {
		t.Fatal("newDockerConfigJSONKeychain() accepted credHelpers")
	}
}

type testAuthResource struct {
	full     string
	registry string
}

func (r testAuthResource) String() string {
	return r.full
}

func (r testAuthResource) RegistryStr() string {
	return r.registry
}
