# API Versioning Policy

## Current Version: v1alpha1

The Zarf Operator API follows Kubernetes API versioning conventions.

## Promotion Criteria

### v1alpha1 → v1beta1

All of the following must be met before promoting the API:

- **Stable API fields**: No breaking field changes in the last 2 months.
- **Test coverage**: Unit tests for all spec/status fields and webhook validation.
- **Golden file test**: API compatibility test passes against a frozen v1alpha1 golden file.
- **Production usage**: At least 2 months of production deployment by early adopters.
- **Documentation**: All spec fields documented in README and CRD descriptions.

### v1beta1 → v1

- **No breaking changes** for at least 6 months after v1beta1 release.
- **Conversion webhooks** tested for v1alpha1 → v1beta1 → v1 round-trip.
- **Broad adoption**: Multiple production deployments with diverse workloads.

## Deprecation Policy

| Event | Timeline |
|---|---|
| v1beta1 released | v1alpha1 deprecated (warning added to webhook responses) |
| v1beta1 + 2 releases | v1alpha1 removed; conversion webhook no longer served |
| v1 released | v1beta1 deprecated |
| v1 + 3 releases | v1beta1 removed |

## Breaking vs Non-Breaking Changes

**Non-breaking** (allowed within a version):
- Adding new optional spec fields with defaults
- Adding new status fields
- Adding new condition types
- Widening validation (accepting more values)

**Breaking** (requires version bump):
- Removing or renaming a spec/status field
- Changing a field's type
- Tightening validation (rejecting previously valid values)
- Changing default values for existing fields

## Maintaining Compatibility

A golden file test in `api/v1alpha1/testdata/zarfpackage_v1alpha1.golden.json` ensures
that the serialized form of `ZarfPackage` does not change unexpectedly. If a field is
intentionally added or renamed, update the golden file and document the change in release notes.
