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
	Source                 string   `json:"source"`
	Components             []string `json:"components,omitempty"`
	Namespace              string   `json:"namespace,omitempty"`
	Set                    []string `json:"set,omitempty"`
	Shasum                 string   `json:"shasum,omitempty"`
	Features               []string `json:"features,omitempty"`
	Architecture           string   `json:"architecture,omitempty"`
	AdoptExistingResources bool     `json:"adoptExistingResources,omitempty"`
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
	}
	data, _ := json.Marshal(das)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", hash[:8]) // 16 hex chars, sufficient for comparison
}

// ZarfPackageSpec defines the desired state of ZarfPackage.
type ZarfPackageSpec struct {
	// Source is the location of the Zarf package OCI
	// +kubebuilder:validation:Required
	Source string `json:"source"`

	// AdoptExistingResources indicates whether to adopt any pre-existing K8s resources into the Helm charts managed by Zarf.
	// +optional
	AdoptExistingResources bool `json:"adoptExistingResources,omitempty"`

	// Components is a list of components to deploy. Adding this field will skip the prompts for selected components.
	// +optional
	Components []string `json:"components,omitempty"`

	// No reason to have confirm inside an operator
	// Confirm bool `json:"confirm,omitempty"`

	// Namespace is the Kubernetes namespace to deploy the Zarf package into.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Retries is the number of times to retry deploying the Zarf package in case of failure.
	// +optional
	Retries int `json:"retries,omitempty"`

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

	// NoColor indicates whether to disable colored output in logs.
	// +optional
	NoColor bool `json:"noColor,omitempty"`

	// OciConcurrency is the number of concurrent OCI operations for the Zarf package.
	// +optional
	OciConcurrency int `json:"ociConcurrency,omitempty"`

	// PlainHTTP indicates whether to use plain HTTP instead of HTTPS for the Zarf package.
	// +optional
	PlainHTTP bool `json:"plainHTTP,omitempty"`

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

	// ConditionTypeSynced indicates the package is synced with the cluster
	ConditionTypeSynced ZarfPackageConditionType = "Synced"

	// ConditionTypeProgressing indicates deployment is in progress
	ConditionTypeProgressing ZarfPackageConditionType = "Progressing"

	// ConditionTypeDriftDetected indicates drift was detected between expected and actual state
	ConditionTypeDriftDetected ZarfPackageConditionType = "DriftDetected"
)

// ZarfPackageCondition represents a condition of the ZarfPackage
type ZarfPackageCondition struct {
	// Type of condition
	Type ZarfPackageConditionType `json:"type"`

	// Status of the condition (True, False, Unknown)
	Status metav1.ConditionStatus `json:"status"`

	// Reason is a machine-readable reason for the condition
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable description
	Message string `json:"message,omitempty"`

	// LastTransitionTime is when the condition last changed
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// ZarfPackageStatus defines the observed state of ZarfPackage.
type ZarfPackageStatus struct {
	// Package string `json:"package,omitempty"`
	// NamespaceOverride string `json:"namespaceOverride,omitempty"`
	// Version string `json:"version,omitempty"`
	// Components []string `json:"components,omitempty"`

	// Phase is the current phase of the deployment
	Phase ZarfPackagePhase `json:"phase,omitempty"`

	// Conditions represent the latest observations
	Conditions []ZarfPackageCondition `json:"conditions,omitempty"`

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
