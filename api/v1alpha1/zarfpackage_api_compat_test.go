package v1alpha1

import (
	"encoding/json"
	"os"
	"testing"
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
}

func assertEqual[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
