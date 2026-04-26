/*
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
*/

package v1alpha1

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deploymentAffectingSpec contains only the fields that require redeployment when changed
type deploymentAffectingSpec struct {
	Source                 string       `json:"source"`
	Components             []string     `json:"components,omitempty"`
	Namespace              string       `json:"namespace,omitempty"`
	Set                    []string     `json:"set,omitempty"`
	Shasum                 string       `json:"shasum,omitempty"`
	Features               []string     `json:"features,omitempty"`
	Architecture           string       `json:"architecture,omitempty"`
	AdoptExistingResources bool         `json:"adoptExistingResources,omitempty"`
	ClusterSecretRef       string       `json:"clusterSecretRef,omitempty"`
	InitOptions            *InitOptions `json:"initOptions,omitempty"`
}

// DeploymentHash returns a SHA256 hash of the deployment-affecting spec fields.
// This follows the Flux pattern (lastAttemptedConfigDigest) for detecting meaningful spec changes.
func (s *ZarfPackageSpec) DeploymentHash() string {
	das := deploymentAffectingSpec{
		Source:                 s.Source,
		Components:             s.Components,
		Namespace:              s.Namespace,
		Set:                    s.Set,
		Shasum:                 s.Shasum,
		Features:               s.Features,
		Architecture:           s.Architecture,
		AdoptExistingResources: s.AdoptExistingResources,
		ClusterSecretRef:       s.ClusterSecretRef,
		InitOptions:            s.InitOptions,
	}
	data, _ := json.Marshal(das)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", hash[:8]) // 16 hex chars, sufficient for comparison
}

// DependsOnReference references another ZarfPackage that must be deployed first.
type DependsOnReference struct {
	// Name of the ZarfPackage dependency.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the dependency. Defaults to the same namespace as this package when omitted.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// RegistryInfoOptions configures the target cluster's container registry for
// an init package deployment. See InitOptions for overall semantics.
type RegistryInfoOptions struct {
	// Address overrides the registry address recorded in the referenced
	// zarf-state secret (registryInfo.address). Use this when the secret's
	// address is not reachable from the target cluster — for example, when
	// initializing a vcluster that pulls from a host-cluster registry exposed
	// via service replication, the zarf-state secret on the host records an
	// address like "127.0.0.1:31999" which does not resolve from the vcluster.
	// +optional
	Address string `json:"address,omitempty"`

	// NodePort overrides the registry NodePort recorded in the referenced
	// zarf-state secret (registryInfo.nodePort). Only meaningful when zarf's
	// internal registry is deployed; ignored once an external Address is set.
	// +optional
	NodePort int32 `json:"nodePort,omitempty"`

	// CredentialsSecretRef is the name of a Secret in the same namespace as
	// the ZarfPackage that matches the shape zarf itself writes under
	// zarf/zarf-state. The controller parses the Secret's ".data.state" JSON
	// payload and extracts "registryInfo" (address, nodePort, secret,
	// pushUsername, pushPassword, pullUsername, pullPassword). Users can sync
	// the source cluster's zarf-state secret into the ZarfPackage's namespace
	// rather than authoring a custom secret.
	// +optional
	CredentialsSecretRef string `json:"credentialsSecretRef,omitempty"`
}

// InitOptions configures init-package-specific deployment parameters such as
// the target registry. Zarf's SDK honors these fields only when the package's
// "kind" is "ZarfInitConfig"; other packages silently ignore them. When
// RegistryInfo.Address is non-empty, zarf automatically skips the in-cluster
// registry components (zarf-injector, zarf-seed-registry, zarf-registry) —
// useful for initializing a vcluster that reuses the host cluster's registry.
type InitOptions struct {
	// RegistryInfo configures the target cluster's zarf registry.
	// +optional
	RegistryInfo *RegistryInfoOptions `json:"registryInfo,omitempty"`
}

// ZarfPackageSpec defines the desired state of ZarfPackage.
type ZarfPackageSpec struct {
	// Source is the location of the Zarf package OCI
	// +kubebuilder:validation:Required
	Source string `json:"source"`

	// DependsOn specifies ZarfPackages that must be in Deployed phase before this package deploys.
	// +optional
	DependsOn []DependsOnReference `json:"dependsOn,omitempty"`

	// AdoptExistingResources indicates whether to adopt any pre-existing K8s resources into the Helm charts managed by Zarf.
	// +optional
	AdoptExistingResources bool `json:"adoptExistingResources,omitempty"`

	// Components is a list of components to deploy. Adding this field will skip the prompts for selected components.
	// +optional
	Components []string `json:"components,omitempty"`

	// Namespace is the Kubernetes namespace to deploy the Zarf package into.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Retries is the number of times to retry deploying the Zarf package in case of failure.
	// +optional
	Retries int `json:"retries,omitempty"`

	// MaxRetries is the maximum number of consecutive failures before permanently marking
	// the package as Failed. 0 means unlimited retries.
	// +optional
	// +kubebuilder:default=0
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// Set is a list of key-value pairs as package variables.
	// +optional
	Set []string `json:"set,omitempty"`

	// Shasum is the SHA256 checksum of the Zarf package.
	// +optional
	Shasum string `json:"shasum,omitempty"`

	// SkipSignatureValidation indicates whether to skip signature validation for the Zarf package.
	// +optional
	SkipSignatureValidation bool `json:"skipSignatureValidation,omitempty"`

	// Timeout is the maximum duration to wait for the Zarf package deployment.
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// Architecture is the target architecture for the Zarf package.
	// +optional
	Architecture string `json:"architecture,omitempty"`

	// Features is a list of features to enable in the Zarf package.
	// +optional
	Features []string `json:"features,omitempty"`

	// InsecureSkipTLSVerify indicates whether to skip TLS verification for the Zarf package.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`

	// Key is the key used for authentication with the Zarf package.
	// +optional
	Key string `json:"key,omitempty"`

	// LogFormat is the format of the logs for the Zarf package.
	// +optional
	LogFormat string `json:"logFormat,omitempty"`

	// LogLevel is the level of logging for the Zarf package.
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// HelmDebugEnabled forces Helm debug logs to be captured for this package deployment.
	// +optional
	// +kubebuilder:default=false
	HelmDebugEnabled bool `json:"helmDebugEnabled,omitempty"`

	// NoColor indicates whether to disable colored output in logs.
	// +optional
	NoColor bool `json:"noColor,omitempty"`

	// OciConcurrency is the number of concurrent OCI operations for the Zarf package.
	// +optional
	OciConcurrency int `json:"ociConcurrency,omitempty"`

	// PlainHTTP indicates whether to use plain HTTP instead of HTTPS for the Zarf package.
	// +optional
	PlainHTTP bool `json:"plainHTTP,omitempty"`

	// RegistryCredentialSecretRef is the name of a kubernetes.io/dockerconfigjson Secret
	// in the same namespace as the ZarfPackage. The controller reads the Secret's
	// .dockerconfigjson key and injects it as the Docker credential store for the
	// OCI pull, so the sidecar can authenticate to private registries.
	// +optional
	RegistryCredentialSecretRef string `json:"registryCredentialSecretRef,omitempty"`

	// ClusterSecretRef is the name of a Secret in the same namespace as the
	// ZarfPackage that provides credentials for deploying to a remote
	// Kubernetes cluster. When omitted, the package is deployed to the cluster
	// the operator is running in (default behavior).
	//
	// Two Secret shapes are supported, keyed on the "config" entry:
	//   - vcluster-style: "config" contains a full kubeconfig YAML document.
	//   - Argo CD cluster-secret style: "config" contains a JSON object with a
	//     "tlsClientConfig" (caData/certData/keyData) or "bearerToken", paired
	//     with sibling "server" and (optionally) "name" keys.
	//
	// Changing, setting, or clearing this field triggers a redeploy. The
	// operator does NOT uninstall the previous deployment when the target
	// cluster changes; any resources on the old cluster are orphaned and must
	// be cleaned up out-of-band.
	// +optional
	ClusterSecretRef string `json:"clusterSecretRef,omitempty"`

	// Tmpdir is the temporary directory for the Zarf package.
	// +optional
	Tmpdir string `json:"tmpdir,omitempty"`

	// ZarfCache is the cache directory for the Zarf package.
	// +optional
	ZarfCache string `json:"zarfCache,omitempty"`

	// SyncPolicy defines how the operator handles drift between desired and actual state.
	// Ignore: Do not check for drift (default)
	// Detect: Check for drift and report in status, but do not remediate
	// Remediate: Check for drift and automatically redeploy to fix it
	// +kubebuilder:validation:Enum=Ignore;Detect;Remediate
	// +kubebuilder:default=Ignore
	// +optional
	SyncPolicy SyncPolicy `json:"syncPolicy,omitempty"`

	// Yolo enables YOLO mode - deploy without requiring 'zarf init'.
	// Images are pulled directly from upstream registries instead of the internal Zarf registry.
	// Only use in connected environments where upstream registries are accessible.
	// +optional
	Yolo bool `json:"yolo,omitempty"`

	// Suspend stops reconciliation for this ZarfPackage when set to true.
	// The controller will not perform any deploy, remove, or drift check operations.
	// +optional
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`

	// InitOptions configures init-package-specific deployment parameters.
	// Silently ignored by zarf for non-init packages. Changes here trigger a
	// redeploy.
	// +optional
	InitOptions *InitOptions `json:"initOptions,omitempty"`
}

// SyncPolicy defines how the operator handles drift between desired and actual state
// +kubebuilder:validation:Enum=Ignore;Detect;Remediate
type SyncPolicy string

const (
	// SyncPolicyIgnore does not check for drift (default behavior)
	SyncPolicyIgnore SyncPolicy = "Ignore"

	// SyncPolicyDetect checks for drift and reports it in status/conditions but does not remediate
	SyncPolicyDetect SyncPolicy = "Detect"

	// SyncPolicyRemediate checks for drift and automatically redeploys to fix it
	SyncPolicyRemediate SyncPolicy = "Remediate"
)

// ZarfPackagePhase represents the phase of the ZarfPackage deployment
// +kubebuilder:validation:Enum=Pending;Deploying;Deployed;Failed;Removing
type ZarfPackagePhase string

const (
	ZarfPackagePhasePending   ZarfPackagePhase = "Pending"
	ZarfPackagePhaseDeploying ZarfPackagePhase = "Deploying"
	ZarfPackagePhaseDeployed  ZarfPackagePhase = "Deployed"
	ZarfPackagePhaseFailed    ZarfPackagePhase = "Failed"
	ZarfPackagePhaseRemoving  ZarfPackagePhase = "Removing"
)

// ComponentStatus tracks the status of a single deployed component
type ComponentStatus struct {
	// Name of the component
	Name string `json:"name"`

	// Status of the component (Succeeded, Failed, Deploying, Removing)
	Status string `json:"status"`

	// InstalledCharts lists the Helm charts deployed by this component
	InstalledCharts []InstalledChartStatus `json:"installCharts,omitempty"`

	// ObservedGeneration is the generation of the package when this component was deployed
	ObservedGeneration int `json:"observedGeneration,omitempty"`
}

// InstalledChartStatus tracks a Helm chart installed by a component
type InstalledChartStatus struct {
	// Namespace where the chart is installed
	Namespace string `json:"namespace"`

	// ChartName is the name of the Helm release
	ChartName string `json:"chartName"`

	// Status of the chart (Succeeded, Failed)
	Status string `json:"status"`
}

// ZarfPackageConditionType represents a condition type
type ZarfPackageConditionType string

const (
	// ConditionTypeReady indicates the package is ready
	ConditionTypeReady ZarfPackageConditionType = "Ready"

	// ConditionTypeProgressing indicates deployment is in progress
	ConditionTypeProgressing ZarfPackageConditionType = "Progressing"

	// ConditionTypeDriftDetected indicates drift was detected between expected and actual state
	ConditionTypeDriftDetected ZarfPackageConditionType = "DriftDetected"

	// ConditionTypeSuspended indicates reconciliation is suspended
	ConditionTypeSuspended ZarfPackageConditionType = "Suspended"

	// ConditionTypeStalled indicates reconciliation is blocked on user action or terminal retries.
	ConditionTypeStalled ZarfPackageConditionType = "Stalled"

	// ConditionTypeDependenciesMet indicates whether all declared dependencies are deployed.
	ConditionTypeDependenciesMet ZarfPackageConditionType = "DependenciesMet"
)

// Reason constants used when setting status conditions.
const (
	ReasonDependenciesNotMet = "DependenciesNotMet"
	ReasonDependenciesMet    = "DependenciesMet"
)

// ZarfPackageStatus defines the observed state of ZarfPackage.
type ZarfPackageStatus struct {
	// Phase is the current phase of the deployment
	Phase ZarfPackagePhase `json:"phase,omitempty"`

	// Conditions represent the latest observations
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DeployedVersion is the version of the deployed package
	DeployedVersion string `json:"deployedVersion,omitempty"`

	// DeployedGeneration is the Zarf deployment generation
	DeployedGeneration int `json:"deployedGeneration,omitempty"`

	// ComponentStatuses tracks the status of each deployed component
	ComponentStatuses []ComponentStatus `json:"componentStatuses,omitempty"`

	// LastReconcileTime is when the package was last reconciled
	LastReconcileTime metav1.Time `json:"lastReconcileTime,omitempty"`

	// ObservedGeneration is the generation observed by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Source is the resolved source of the package
	Source string `json:"source,omitempty"`

	// PackageName is the name from the package metadata
	PackageName string `json:"packageName,omitempty"`

	// DriftInfo contains details about detected drift (when SyncPolicy is Detect or Remedeiate)
	DriftInfo *DriftInfo `json:"driftInfo,omitempty"`

	// DeployedSpecHash is the hash of deployment-affecting spec fields
	// Used to detect when a redeployment is needed (follows Flux's lastAttemptedConfigDigest pattern)
	DeployedSpecHash string `json:"deployedSpecHash,omitempty"`

	// LastAttemptedRevision is the source revision last attempted by the controller.
	LastAttemptedRevision string `json:"lastAttemptedRevision,omitempty"`

	// LastAttemptError is the most recent deployment error message.
	LastAttemptError string `json:"lastAttemptError,omitempty"`

	// FailureCount tracks consecutive reconciliation failures for backoff calculation.
	// +optional
	FailureCount int32 `json:"failureCount,omitempty"`

	// LastFailureTime is when the last failure occurred.
	// +optional
	LastFailureTime *metav1.Time `json:"lastFailureTime,omitempty"`
}

type DriftInfo struct {
	// Detected indicates whether drift was detected
	Detected bool `json:"detected"`

	// LastCheckTime is when drift was last checked
	LastCheckTime metav1.Time `json:"lastCheckTime,omitempty"`

	// MissingReleases lists Helm releases that should exist but don't
	MissingReleases []string `json:"missingReleases,omitempty"`

	// Message provides human-readable drift details
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={zarf},shortName=zp
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.deployedVersion`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ZarfPackage is the Schema for the zarfpackages API.
type ZarfPackage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ZarfPackageSpec   `json:"spec,omitempty"`
	Status ZarfPackageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ZarfPackageList contains a list of ZarfPackage.
type ZarfPackageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZarfPackage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ZarfPackage{}, &ZarfPackageList{})
}
