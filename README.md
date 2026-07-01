# zarf-operator

A Kubernetes operator that manages the lifecycle of [Zarf](https://zarf.dev) packages as custom resources. It enables declarative, GitOps-compatible deployments of Zarf packages in both connected and airgapped environments.

## Description

The Zarf Operator runs two containers in a single pod:

- **Manager** — A controller-runtime reconciler that watches `ZarfPackage` custom resources and drives them through deploy, drift-detect, and removal workflows.
- **Sidecar** — A gRPC server wrapping the Zarf Go library for headless package operations (deploy, remove, inspect).

Key capabilities:
- **Declarative deploys** — Create a `ZarfPackage` CR and the operator handles the rest.
- **Drift detection** — Compares deployed Helm releases against desired state via `SyncPolicy` (Ignore / Detect / Remediate).
- **Retry & backoff** — Configurable retries with exponential backoff on transient failures.
- **Suspend/resume** — Pause reconciliation without deleting the CR.
- **Admission webhooks** — Validates and defaults `ZarfPackage` specs at creation and update time.
- **Airgap-native** — Ships as a Zarf package for fully disconnected environments.

## Quick Start

### Prerequisites

- Kubernetes v1.25+ cluster
- `kubectl` configured to talk to your cluster
- **Option A** requires [Helm](https://helm.sh) v3.14+
- **Option B** requires the [Zarf](https://zarf.dev) CLI

---

### Option A — Install with Helm

```bash
helm install zarf-operator oci://ghcr.io/enel1221/charts/zarf-operator \
  --version 0.3.2 \
  --create-namespace \
  --namespace zarf-operator-system
```

### Option B — Install with Zarf (airgapped)

```bash
zarf package deploy oci://ghcr.io/enel1221/packages/zarf-operator:0.3.2
```

> Replace `amd64` with `arm64` if deploying to an ARM cluster.

---

### Deploy Your First Package

Once the operator is running, deploy a Zarf package by creating a `ZarfPackage` CR.

#### Example 1 — Podinfo via Flux

This deploys [podinfo](https://github.com/stefanprodan/podinfo) using Flux's Helm OCI integration, packaged as a Zarf package:

```bash
kubectl apply -f - <<'EOF'
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: podinfo
spec:
  source: "oci://ghcr.io/enel1221/podinfo-flux/podinfo-flux:1.0.0"
  components:
    - flux
    - podinfo-via-flux-helm-oci
  skipSignatureValidation: true
EOF
```

#### Example 2 — DOS Games Arcade

A fun quick test — deploys a retro DOS games arcade into your cluster:

```bash
kubectl apply -f - <<'EOF'
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: dos-games
spec:
  source: "oci://ghcr.io/zarf-dev/packages/dos-games:1.2.0"
  skipSignatureValidation: true
EOF
```

#### Watch the deployment

```bash
kubectl get zarfpackages -w
```

To clean up:

```bash
kubectl delete zarfpackage podinfo dos-games
```

## Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `source` | string | *required* | OCI reference or file path to the Zarf package |
| `components` | []string | all | Components to deploy (skip prompt) |
| `namespace` | string | | Target namespace for deployment |
| `retries` | int | 3 | Number of deploy retries on failure |
| `maxRetries` | int32 | 0 | Max consecutive failures before permanent Failed (0=unlimited) |
| `timeout` | string | 15m | Max duration for deployment |
| `syncPolicy` | enum | Ignore | Drift handling: Ignore, Detect, Remediate |
| `upgradePolicy.enabled` | bool | false | Poll the OCI repository for newer semantic-version tags and deploy them automatically |
| `upgradePolicy.strategy` | enum | SemVer | Upgrade discovery strategy. Currently only SemVer is supported |
| `upgradePolicy.interval` | string | controller requeue interval | Poll interval for checking OCI tags. When set, it must be a valid duration of at least `1m`, for example `5m` |
| `upgradePolicy.semverConstraint` | string | | Optional semver constraint such as `~1.0` or `>=1.0.0 <2.0.0` |
| `suspend` | bool | false | Pause all reconciliation |
| `yolo` | bool | false | Deploy without `zarf init` (connected only) |
| `ociConcurrency` | int | 6 | Concurrent OCI layer downloads |
| `shasum` | string | | Expected SHA256 of the package |
| `set` | []string | | Package variable overrides (`KEY=VALUE`) |
| `architecture` | string | | Target architecture override |
| `skipSignatureValidation` | bool | false | Skip package signature check |
| `insecureSkipTLSVerify` | bool | false | Skip TLS verification for OCI registry |
| `registryCredentialSecretRef` | string | | Name of a `kubernetes.io/dockerconfigjson` Secret for private registry auth |

## Operations Guide

### Drift Detection

| SyncPolicy | Behavior |
|------------|----------|
| **Ignore** | No drift checks (default) |
| **Detect** | Checks Helm releases on each reconcile; reports drift in status conditions but does not fix |
| **Remediate** | Detects drift and automatically redeploys to restore desired state |

### Suspend / Resume

```bash
# Suspend reconciliation
kubectl patch zarfpackage my-package --type merge -p '{"spec":{"suspend":true}}'

# Resume
kubectl patch zarfpackage my-package --type merge -p '{"spec":{"suspend":false}}'
```

### Automatic SemVer Upgrades

For tagged semantic-versioned OCI package sources, enable `upgradePolicy` to
let the operator poll the same OCI repository and deploy newer stable semver
tags. When `upgradePolicy.enabled` is true, `spec.source` must be an OCI source
with an explicit semantic-version tag, not a file path, digest, or `latest`.
The operator does not mutate `spec.source`; it records the deployed resolved
source in `status.source`. `status.availableSource` and
`status.availableVersion` are pending-upgrade fields and are cleared after the
candidate is deployed or invalidated. Custom poll intervals must be valid
durations of at least `1m`.

```yaml
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: my-package
spec:
  source: "oci://registry.example.com/my-org/my-package:1.0.0"
  registryCredentialSecretRef: private-registry-auth
  upgradePolicy:
    enabled: true
    strategy: SemVer
    interval: 5m
    semverConstraint: ">=1.0.0 <2.0.0"
```

Only tags newer than the currently deployed semantic version are eligible.
Prerelease tags are ignored by default, and non-semver tags such as `latest` are
ignored.

Disabling `upgradePolicy` stops future polling without rolling back an already
deployed auto-upgrade. To intentionally redeploy the pinned `spec.source`,
disable the policy, set `spec.source` to the target tag, and use the redeploy
annotation below.

### Force Redeploy

Annotate a `ZarfPackage` to trigger a redeploy without changing the spec. The controller detects the annotation, deploys the package, then automatically removes the annotation (no reconcile loop).

```bash
kubectl annotate zarfpackage my-package zarf.dev/redeploy=true --overwrite
```

### Registry Credentials

To pull packages from private OCI registries, create a `kubernetes.io/dockerconfigjson` Secret and reference it:

```bash
kubectl create secret docker-registry private-registry-auth \
  --docker-server=registry.example.com \
  --docker-username=myuser \
  --docker-password=mytoken \
  -n zarf
```

```yaml
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: my-private-package
  namespace: zarf
spec:
  source: "oci://registry.example.com/my-org/my-package:1.0.0"
  registryCredentialSecretRef: private-registry-auth
```

The operator reads the Secret, passes the credentials to the sidecar over gRPC, and Zarf's OCI layer authenticates automatically. The Secret must exist in the same namespace as the ZarfPackage.

The annotation value can be anything (`true`, a timestamp, etc.). After a successful deploy, the operator:
1. Records a `RedeployRequested` event ("Redeploy triggered via annotation")
2. Deploys the package
3. Removes the `zarf.dev/redeploy` annotation
4. Records a follow-up event ("Redeploy annotation cleared after successful deploy")

You can also set the annotation directly in a manifest:

```yaml
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: my-package
  annotations:
    zarf.dev/redeploy: "true"
spec:
  source: "oci://ghcr.io/example/my-package:1.0.0"
```

### Troubleshooting

```bash
# Check conditions
kubectl get zarfpackage my-package -o jsonpath='{.status.conditions}' | jq .

# Check events
kubectl describe zarfpackage my-package

# Check operator logs
kubectl logs -n zarf-operator-system deploy/zarf-operator-controller-manager -c manager
kubectl logs -n zarf-operator-system deploy/zarf-operator-controller-manager -c sidecar
```

### Resource Sizing

| Package Size | Sidecar Memory Limit | Cache Size |
|-------------|---------------------|------------|
| Small (<100MB) | 512Mi | 2Gi |
| Medium (<1GB) | 1Gi | 10Gi |
| Large (>1GB) | 2Gi | 20Gi |

## Development

### Prerequisites
- Go 1.23+
- Docker 17.03+
- kubectl v1.25+
- Access to a Kubernetes v1.25+ cluster

### Build and Deploy

```sh
make docker-build docker-push IMG=<some-registry>/zarf-operator:tag
make install
make deploy IMG=<some-registry>/zarf-operator:tag
```

### Testing

```sh
make test       # Unit tests
make test-e2e   # E2E tests (requires kind)
```

### Uninstall

```sh
kubectl delete -k config/samples/
make uninstall
make undeploy
```

## Contributing

1. **Prerequisites**: Go 1.23+, Docker, kind, Helm
2. **Run tests**: `make test` (unit) or `make test-e2e` (end-to-end with kind)
3. **Adding features**: Modify types in `api/v1alpha1/`, run `make generate && make manifests`, write tests
4. **PR conventions**: One logical change per PR, include tests, pass CI

Run `make help` for more information on all potential `make` targets.

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
