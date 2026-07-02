package v1alpha1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestAPICompatibilityGoldenFile(t *testing.T) {
	golden, err := os.ReadFile("testdata/zarfpackage_v1alpha1.golden.json")
	if err != nil {
		t.Fatalf("failed to read golden file: %v", err)
	}

	var pkg ZarfPackage
	if err := json.Unmarshal(golden, &pkg); err != nil {
		t.Fatalf("failed to unmarshal golden file: %v", err)
	}

	// Verify all spec fields deserialize correctly
	spec := pkg.Spec
	assertEqual(t, "Source", spec.Source, "oci://example.com/pkg:v1")
	assertEqual(t, "DependsOn length", len(spec.DependsOn), 2)
	assertEqual(t, "DependsOn[0].Name", spec.DependsOn[0].Name, "base-package")
	assertEqual(t, "DependsOn[0].Namespace", spec.DependsOn[0].Namespace, "")
	assertEqual(t, "DependsOn[1].Name", spec.DependsOn[1].Name, "cross-ns-package")
	assertEqual(t, "DependsOn[1].Namespace", spec.DependsOn[1].Namespace, "platform")
	assertEqual(t, "AdoptExistingResources", spec.AdoptExistingResources, true)
	assertEqual(t, "Components length", len(spec.Components), 2)
	assertEqual(t, "Components[0]", spec.Components[0], "comp-a")
	assertEqual(t, "Namespace", spec.Namespace, "target-ns")
	assertEqual(t, "Retries", spec.Retries, 3)
	assertEqual(t, "MaxRetries", spec.MaxRetries, int32(5))
	assertEqual(t, "Set[0]", spec.Set[0], "KEY=val")
	assertEqual(t, "Shasum", spec.Shasum, "abc123")
	assertEqual(t, "Timeout", spec.Timeout, "15m")
	assertEqual(t, "Architecture", spec.Architecture, "amd64")
	assertEqual(t, "Features[0]", spec.Features[0], "feat-a")
	assertEqual(t, "Key", spec.Key, "/path/to/key")
	assertEqual(t, "LogFormat", spec.LogFormat, "json")
	assertEqual(t, "LogLevel", spec.LogLevel, "info")
	assertEqual(t, "NoColor", spec.NoColor, true)
	assertEqual(t, "OciConcurrency", spec.OciConcurrency, 6)
	assertEqual(t, "RegistryCredentialSecretRef", spec.RegistryCredentialSecretRef, "my-registry-secret")
	assertEqual(t, "SyncPolicy", string(spec.SyncPolicy), "Detect")
	assertEqual(t, "Tmpdir", spec.Tmpdir, "/tmp/zarf")
	assertEqual(t, "ZarfCache", spec.ZarfCache, "/cache")
	if spec.UpgradePolicy != nil {
		t.Fatalf("UpgradePolicy: got populated policy, want nil for old-object compatibility golden")
	}

	// Verify round-trip: marshal and unmarshal again should match
	data, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var roundTrip ZarfPackage
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("failed to unmarshal round-trip: %v", err)
	}
	assertEqual(t, "round-trip Source", roundTrip.Spec.Source, pkg.Spec.Source)
	assertEqual(t, "round-trip SyncPolicy", string(roundTrip.Spec.SyncPolicy), string(pkg.Spec.SyncPolicy))
	if roundTrip.Spec.UpgradePolicy != nil {
		t.Fatalf("round-trip UpgradePolicy: got populated policy, want nil for old-object compatibility golden")
	}
}

func TestAPICompatibilityUpgradePolicyGoldenFile(t *testing.T) {
	golden, err := os.ReadFile("testdata/zarfpackage_v1alpha1_upgrade_policy.golden.json")
	if err != nil {
		t.Fatalf("failed to read upgradePolicy golden file: %v", err)
	}

	var pkg ZarfPackage
	if err := json.Unmarshal(golden, &pkg); err != nil {
		t.Fatalf("failed to unmarshal upgradePolicy golden file: %v", err)
	}

	spec := pkg.Spec
	assertEqual(t, "Source", spec.Source, "oci://example.com/pkg:1.0.0")
	if spec.UpgradePolicy == nil {
		t.Fatal("UpgradePolicy: got nil, want populated policy")
	}
	assertEqual(t, "UpgradePolicy.Enabled", spec.UpgradePolicy.Enabled, true)
	assertEqual(t, "UpgradePolicy.Strategy", string(spec.UpgradePolicy.Strategy), "SemVer")
	assertEqual(t, "UpgradePolicy.Interval", spec.UpgradePolicy.Interval, "1m")
	assertEqual(t, "UpgradePolicy.SemverConstraint", spec.UpgradePolicy.SemverConstraint, "~1.0")

	data, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("failed to marshal upgradePolicy package: %v", err)
	}
	var roundTrip ZarfPackage
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("failed to unmarshal upgradePolicy round-trip: %v", err)
	}
	if roundTrip.Spec.UpgradePolicy == nil {
		t.Fatal("round-trip UpgradePolicy: got nil, want populated policy")
	}
	assertEqual(t, "round-trip UpgradePolicy.Strategy", string(roundTrip.Spec.UpgradePolicy.Strategy), string(spec.UpgradePolicy.Strategy))
}

func TestZarfPackageCRDEnforcesUpgradePolicyWithoutWebhook(t *testing.T) {
	for _, path := range []string{
		"config/crd/bases/zarf.dev_zarfpackages.yaml",
		"dist/chart/templates/crd/zarf.dev_zarfpackages.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			specSchema := loadZarfPackageSpecSchema(t, path)
			rules := formatValidationRules(specSchema.XValidations)

			assertContains(t, rules, "upgradePolicy.enabled requires spec.source")
			assertContains(t, rules, "self.source.startsWith('oci://')")
			assertContains(t, rules, "!self.source.contains('@')")
			assertContains(t, rules, "self.source.matches")
			assertContains(t, rules, "upgradePolicy.interval must be empty or a valid duration at least 1 minute")
			assertContains(t, rules, "duration(self.upgradePolicy.interval) >= duration('1m')")

			upgradePolicySchema := specSchema.Properties["upgradePolicy"]
			strategySchema := upgradePolicySchema.Properties["strategy"]
			if len(strategySchema.AllOf) != 0 {
				t.Fatalf("upgradePolicy.strategy should not use duplicate allOf enum output: %#v", strategySchema.AllOf)
			}
			if len(strategySchema.Enum) != 1 || string(strategySchema.Enum[0].Raw) != `"SemVer"` {
				t.Fatalf("upgradePolicy.strategy enum = %#v, want single SemVer enum", strategySchema.Enum)
			}
		})
	}
}

func loadZarfPackageSpecSchema(t *testing.T, crdPath string) apiextensionsv1.JSONSchemaProps {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(crdPath)))
	if err != nil {
		t.Fatalf("failed to read CRD %s: %v", crdPath, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("failed to unmarshal CRD %s: %v", crdPath, err)
	}
	for _, version := range crd.Spec.Versions {
		if version.Name != "v1alpha1" {
			continue
		}
		if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			t.Fatalf("CRD %s v1alpha1 has no OpenAPI schema", crdPath)
		}
		specSchema, ok := version.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			t.Fatalf("CRD %s v1alpha1 schema has no spec property", crdPath)
		}
		return specSchema
	}
	t.Fatalf("CRD %s has no v1alpha1 version", crdPath)
	return apiextensionsv1.JSONSchemaProps{}
}

func formatValidationRules(rules []apiextensionsv1.ValidationRule) string {
	parts := make([]string, 0, len(rules)*2)
	for _, rule := range rules {
		parts = append(parts, rule.Rule, rule.Message)
	}
	return strings.Join(parts, "\n")
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected to find %q in:\n%s", want, got)
	}
}

func assertEqual[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
