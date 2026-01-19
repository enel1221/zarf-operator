# Zarf Operator

A Kubernetes operator for managing Zarf package deployments with integrated security scanning and lifecycle management.

## Description

The Zarf Operator automates the deployment, management, and security scanning of Zarf packages in Kubernetes environments. Built with Kubebuilder, it provides a cloud-native approach to managing air-gapped application deployments with comprehensive vulnerability scanning using Grype.

**Key Features:**
- 🚀 **Automated Package Management**: Deploy, update, and remove Zarf packages declaratively
- 🔒 **Integrated Security Scanning**: Built-in Grype vulnerability scanning with historical tracking
- 📊 **Production Ready**: Circuit breaker patterns, comprehensive logging, and robust error handling
- 🔄 **Lifecycle Management**: Proper finalizer handling for clean resource cleanup
- 📈 **Observability**: Full integration with Kubernetes events and status reporting
- 🏢 **Enterprise Features**: RBAC integration, multi-tenancy support, and operational monitoring

## Getting Started

### Prerequisites
- go version v1.24.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/zarf-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/zarf-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/zarf-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/zarf-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing

We welcome contributions to the Zarf Operator! Here's how you can help:

### Development Setup

1. **Fork and Clone**:
   ```bash
   git clone https://github.com/your-username/zarf-operator.git
   cd zarf-operator
   ```

2. **Install Dependencies**:
   ```bash
   make install-deps
   ```

3. **Run Tests**:
   ```bash
   make test
   make test-e2e
   ```

### Contribution Guidelines

- **Issues**: Use GitHub Issues for bug reports and feature requests
- **Pull Requests**: Follow the standard GitHub flow with descriptive commit messages
- **Code Style**: Run `make fmt` and `make vet` before submitting
- **Testing**: Ensure all tests pass and add tests for new features
- **Documentation**: Update relevant documentation for any changes

### Code of Conduct

This project follows the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/master/code-of-conduct.md).

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

