# Zarf Operator Helm Chart

Kubernetes operator for declarative Zarf package deployment and lifecycle management.

## Install

```bash
helm install zarf-operator oci://ghcr.io/enel1221/zarf-operator/chart --namespace zarf-operator-system --create-namespace
```

## Values

| Key | Default | Description |
|---|---|---|
| `controllerManager.replicas` | `1` | Number of operator replicas |
| `controllerManager.manager.image.repository` | `controller` | Manager image repository |
| `controllerManager.manager.image.tag` | `latest` | Manager image tag |
| `controllerManager.manager.resources.limits.cpu` | `500m` | Manager CPU limit |
| `controllerManager.manager.resources.limits.memory` | `128Mi` | Manager memory limit |
| `sidecar.image.repository` | `zarf-sidecar` | Sidecar image repository |
| `sidecar.image.tag` | `latest` | Sidecar image tag |
| `sidecar.resources.limits.cpu` | `1000m` | Sidecar CPU limit |
| `sidecar.resources.limits.memory` | `1Gi` | Sidecar memory limit |
| `sidecar.cache.sizeLimit` | `10Gi` | Zarf cache volume size |
| `pdb.enable` | `true` | Enable PodDisruptionBudget (requires replicas > 1) |
| `pdb.minAvailable` | `1` | Minimum available pods during disruption |
| `webhook.enable` | `false` | Enable admission webhooks |

## Uninstall

```bash
helm uninstall zarf-operator -n zarf-operator-system
```
