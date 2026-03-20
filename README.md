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
- Kubernetes v1.25+
- Helm v3.14+
- (Optional) Zarf CLI for airgapped deployments

### Install via Helm

```bash
helm install zarf-operator oci://ghcr.io/enel1221/charts/zarf-operator --version 0.1.0 \
  --create-namespace --namespace zarf-operator-system
```

### Install via Zarf (airgapped)

```bash
zarf package deploy oci://ghcr.io/enel1221/packages/zarf-operator:0.1.0-amd64
```

### Create a ZarfPackage

```yaml
apiVersion: ops.d0s.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: my-package
spec:
  source: "oci://ghcr.io/example/my-package:v1.0.0"
  components:
    - component-a
  syncPolicy: Detect
```

```bash
kubectl apply -f my-package.yaml
kubectl get zarfpackages -w
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
| `suspend` | bool | false | Pause all reconciliation |
| `yolo` | bool | false | Deploy without `zarf init` (connected only) |
| `ociConcurrency` | int | 6 | Concurrent OCI layer downloads |
| `shasum` | string | | Expected SHA256 of the package |
| `set` | []string | | Package variable overrides (`KEY=VALUE`) |
| `architecture` | string | | Target architecture override |
| `skipSignatureValidation` | bool | false | Skip package signature check |
| `insecureSkipTLSVerify` | bool | false | Skip TLS verification for OCI registry |

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

### Force Redeploy

Change any deployment-affecting field (source tag, components, etc.) or add/modify an annotation to trigger a new reconciliation.

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

