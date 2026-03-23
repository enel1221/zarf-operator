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

package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	opsv1alpha1 "github.com/enel1221/zarf-operator/api/v1alpha1"
	"github.com/enel1221/zarf-operator/pkg/zarf"
)

// Finalizer
const (
	ZarfPackageFinalizer = "zarfpackage.zarf.dev/finalizer"
	AnnotationRedeploy   = "zarf.dev/redeploy"
	DefaultRequeueAfter  = 5 * time.Minute
	backoffBaseInterval  = 10 * time.Second
	backoffMaxInterval   = 5 * time.Minute
	backoffMultiplier    = 2.0
	backoffMaxJitter     = 5 * time.Second
)

// Condition reasons
const (
	ReasonDeploying          = "Deploying"
	ReasonDeployed           = "Deployed"
	ReasonDeployFailed       = "DeployFailed"
	ReasonRemoving           = "Removing"
	ReasonRemoveFailed       = "RemoveFailed"
	ReasonSourceNotFound     = "SourceNotFound"
	ReasonReconciling        = "Reconciling"
	ReasonDriftDetected      = "DriftDetected"
	ReasonDriftResolved      = "DriftResolved"
	ReasonSidecarUnavailable = "SidecarUnavailable"
	ReasonMaxRetriesExceeded = "MaxRetriesExceeded"
	ReasonRedeployRequested  = "RedeployRequested"
	ReasonSecretNotFound     = "SecretNotFound"
)

// Error definitions
var (
	ErrDeployFailed   = errors.New("deployment failed")
	ErrRemoveFailed   = errors.New("removal failed")
	ErrClientNotReady = errors.New("zarf client not ready")
	tracer            = otel.Tracer("zarfpackage-controller")
)

// ZarfPackageReconciler reconciles a ZarfPackage object
type ZarfPackageReconciler struct {
	client.Client
	Log                 logr.Logger
	Scheme              *runtime.Scheme
	ZarfClient          zarf.Client
	RequeueInterval     time.Duration
	recorder            record.EventRecorder
	helmReleaseExistsFn func(ctx context.Context, releaseName, namespace string) (bool, error)
}

// +kubebuilder:rbac:groups=zarf.dev,resources=zarfpackages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=zarf.dev,resources=zarfpackages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=zarf.dev,resources=zarfpackages/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *ZarfPackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	ctx, span := tracer.Start(ctx, "Reconcile", trace.WithAttributes(
		attribute.String("zarfpackage", req.NamespacedName.String()),
	))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())
		}
		span.End()
	}()
	// _ = logf.FromContext(ctx)
	log := r.Log.WithValues("zarfpackage", req.NamespacedName)
	start := time.Now()

	// Fetch the ZarfPackage
	zarfPkg := &opsv1alpha1.ZarfPackage{}
	if err := r.Get(ctx, req.NamespacedName, zarfPkg); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Capture current status for comparison
	currentStatus := zarfPkg.Status.DeepCopy()

	// Defer status update
	defer func() {
		if !equality.Semantic.DeepEqual(currentStatus, &zarfPkg.Status) {
			zarfPkg.Status.LastReconcileTime = metav1.Now()
			zarfPkg.Status.ObservedGeneration = zarfPkg.Generation

			if updateErr := r.Status().Update(ctx, zarfPkg); updateErr != nil {
				if apierrors.IsConflict(updateErr) {
					log.V(1).Info("conflict while updating status, will requeue")
					result = ctrl.Result{Requeue: true}
					return
				}
				log.Error(updateErr, "failed to update status")
				if err == nil {
					err = updateErr
				}
			}
		}
		log.Info("reconcile complete", "duration", time.Since(start))
	}()

	// Handle deletion
	if !zarfPkg.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, log, zarfPkg)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(zarfPkg, ZarfPackageFinalizer) {
		patch := client.MergeFrom(zarfPkg.DeepCopy())
		controllerutil.AddFinalizer(zarfPkg, ZarfPackageFinalizer)
		if err := r.Patch(ctx, zarfPkg, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if zarfPkg.Spec.Suspend {
		log.Info("reconciliation suspended for resource")
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeSuspended, metav1.ConditionTrue,
			"Suspended", "Reconciliation is suspended")
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeNormal, "Suspended", "Reconciliation suspended by user")
		}
		return ctrl.Result{}, nil
	}

	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeSuspended, metav1.ConditionFalse,
		"Active", "Reconciliation is active")

	// Check if zarf client is available
	if r.ZarfClient == nil {
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			ReasonReconciling, "Waiting for Zarf sidecar to be ready")
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonSidecarUnavailable,
				"Zarf sidecar is not available, waiting for reconnection")
		}
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhasePending
		r.recordFailure(zarfPkg)
		if r.maxRetriesExceeded(zarfPkg) {
			r.markMaxRetriesExceeded(zarfPkg, "waiting for Zarf sidecar")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
	}

	// Perform reconciliation
	return r.reconcile(ctx, log, zarfPkg)

	// return ctrl.Result{}, nil
}

func (r *ZarfPackageReconciler) reconcile(ctx context.Context, log logr.Logger, zarfPkg *opsv1alpha1.ZarfPackage) (ctrl.Result, error) {
	// Ensure client is available
	if r.ZarfClient == nil {
		log.Info("zarf sidecar not available, requeueing")
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			ReasonSidecarUnavailable, "Zarf sidecar is not available - ensure sidecar is running")
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonSidecarUnavailable,
				"Zarf sidecar is not available, waiting for reconnection")
		}
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhasePending
		r.recordFailure(zarfPkg)
		if r.maxRetriesExceeded(zarfPkg) {
			r.markMaxRetriesExceeded(zarfPkg, "waiting for Zarf sidecar")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
	}

	// Check current deployment state
	deployedPkg, err := r.ZarfClient.GetDeployedPackage(ctx, zarfPkg.Status.PackageName)
	if err != nil {
		log.Error(err, "failed to get deployed package state")
	}

	// Determine if we need to deploy or update
	needsDeploy := r.needsDeploy(ctx, zarfPkg, deployedPkg)

	if needsDeploy {
		return r.deploy(ctx, log, zarfPkg)
	}

	// Package is deployed and in sync
	r.syncStatusFromDeployed(zarfPkg, deployedPkg)
	r.resetFailures(zarfPkg)
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionTrue,
		ReasonDeployed, "Package deployed successfully")
	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeployed

	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

func (r *ZarfPackageReconciler) needsDeploy(ctx context.Context, zarfPkg *opsv1alpha1.ZarfPackage, deployedPkg *zarf.PackageInfo) bool {
	// Not deployed yet
	if deployedPkg == nil {
		return true
	}

	// Check for redeploy annotation
	if _, ok := zarfPkg.Annotations[AnnotationRedeploy]; ok {
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonRedeployRequested,
				"Redeploy triggered via annotation")
		}
		return true
	}

	// Check for failed components that need retry
	for _, comp := range deployedPkg.DeployedComponents {
		if comp.Status == zarf.ComponentStatusFailed {
			return true
		}
	}

	// Check if deployment-affecting fields changed (not syncPolicy, logLevel, etc.)
	if r.deploymentSpecChanged(zarfPkg) {
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeNormal, "SpecChanged",
				"Deployment-affecting spec fields changed, triggering redeploy")
		}
		return true
	}

	// Check for drift if SyncPolicy is Detect or Remediate
	if zarfPkg.Spec.SyncPolicy == opsv1alpha1.SyncPolicyDetect || zarfPkg.Spec.SyncPolicy == opsv1alpha1.SyncPolicyRemediate {
		driftDetected, missingReleases := r.checkHelmDrift(ctx, deployedPkg)
		if driftDetected {
			zarfPkg.Status.DriftInfo = &opsv1alpha1.DriftInfo{
				Detected:        true,
				LastCheckTime:   metav1.Now(),
				MissingReleases: missingReleases,
				Message:         fmt.Sprintf("Missing Helm releases: %v", missingReleases),
			}
			if r.recorder != nil {
				r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonDriftDetected,
					fmt.Sprintf("Drift detected: %d missing Helm releases: %v", len(missingReleases), missingReleases))
			}
			r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeDriftDetected, metav1.ConditionTrue,
				ReasonDriftDetected, fmt.Sprintf("Drift detected: %d missing releases", len(missingReleases)))

			if zarfPkg.Spec.SyncPolicy == opsv1alpha1.SyncPolicyRemediate {
				return true // Trigger redeploy
			}
		} else if zarfPkg.Status.DriftInfo != nil && zarfPkg.Status.DriftInfo.Detected {
			// Clear drift if no longer detected
			zarfPkg.Status.DriftInfo = nil
			if r.recorder != nil {
				r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonDriftResolved,
					"Drift resolved, all releases present")
			}
			r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeDriftDetected, metav1.ConditionFalse,
				ReasonDriftResolved, "No drift detected")
		}
	}
	return false
}

func (r *ZarfPackageReconciler) deploymentSpecChanged(zarfPkg *opsv1alpha1.ZarfPackage) bool {
	if zarfPkg.Status.DeployedSpecHash == "" {
		return true
	}
	return zarfPkg.Spec.DeploymentHash() != zarfPkg.Status.DeployedSpecHash
}

func (r *ZarfPackageReconciler) checkHelmDrift(ctx context.Context, deployedPkg *zarf.PackageInfo) (bool, []string) {
	if deployedPkg == nil {
		return false, nil
	}

	var missingReleases []string
	for _, comp := range deployedPkg.DeployedComponents {
		for _, chart := range comp.InstalledCharts {
			exists, _ := r.releaseExists(ctx, chart.ChartName, chart.Namespace)
			if !exists {
				missingReleases = append(missingReleases, fmt.Sprintf("%s/%s", chart.Namespace, chart.ChartName))
			}
		}
	}
	return len(missingReleases) > 0, missingReleases
}

func (r *ZarfPackageReconciler) releaseExists(ctx context.Context, releaseName, namespace string) (bool, error) {
	if r.helmReleaseExistsFn != nil {
		return r.helmReleaseExistsFn(ctx, releaseName, namespace)
	}
	return r.helmReleaseExists(ctx, releaseName, namespace)
}

// calculateBackoff returns an exponentially increasing duration with jitter.
// Formula: min(maxInterval, baseInterval*2^failureCount) + random(0, maxJitter)
func calculateBackoff(failureCount int32) time.Duration {
	if failureCount <= 0 {
		return backoffBaseInterval
	}

	backoff := float64(backoffBaseInterval) * math.Pow(backoffMultiplier, float64(failureCount))
	if backoff > float64(backoffMaxInterval) {
		backoff = float64(backoffMaxInterval)
	}

	jitter := time.Duration(rand.Int63n(int64(backoffMaxJitter))) //nolint:gosec
	return time.Duration(backoff) + jitter
}

func (r *ZarfPackageReconciler) recordFailure(zarfPkg *opsv1alpha1.ZarfPackage) {
	zarfPkg.Status.FailureCount++
	now := metav1.Now()
	zarfPkg.Status.LastFailureTime = &now
}

func (r *ZarfPackageReconciler) resetFailures(zarfPkg *opsv1alpha1.ZarfPackage) {
	zarfPkg.Status.FailureCount = 0
	zarfPkg.Status.LastFailureTime = nil
}

func (r *ZarfPackageReconciler) maxRetriesExceeded(zarfPkg *opsv1alpha1.ZarfPackage) bool {
	return zarfPkg.Spec.MaxRetries > 0 && zarfPkg.Status.FailureCount >= zarfPkg.Spec.MaxRetries
}

func (r *ZarfPackageReconciler) markMaxRetriesExceeded(zarfPkg *opsv1alpha1.ZarfPackage, action string) {
	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseFailed
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
		ReasonMaxRetriesExceeded,
		fmt.Sprintf("Max retries (%d) exceeded while %s", zarfPkg.Spec.MaxRetries, action))
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonMaxRetriesExceeded,
			fmt.Sprintf("Operation failed after %d attempts", zarfPkg.Status.FailureCount))
	}
}

func (r *ZarfPackageReconciler) helmReleaseExists(ctx context.Context, releaseName, namespace string) (bool, error) {
	// Add timeout to prevent blocking the controller (following ESO pattern)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Create Helm action configuration using CLI flags for REST client getter
	actionConfig := new(action.Configuration)

	// Use ConfigFlags as RESTClientGetter - this uses in-cluster config automatically
	configFlags := genericclioptions.NewConfigFlags(true)
	configFlags.Namespace = &namespace

	if err := actionConfig.Init(
		configFlags,
		namespace,
		"secret",                                 // Helm stores releases in secrets by default
		func(format string, v ...interface{}) {}, // Silent logger
	); err != nil {
		return false, fmt.Errorf("failed to init helm action config: %w", err)
	}

	// Use History action to check if release exists
	historyClient := action.NewHistory(actionConfig)
	historyClient.Max = 1 // We only need to know if it exists

	_, err := historyClient.Run(releaseName)
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return false, nil
		}
		// Check for context timeout
		if ctx.Err() != nil {
			return false, fmt.Errorf("helm release check timed out: %w", ctx.Err())
		}
		return false, fmt.Errorf("failed to get release history: %w", err)
	}

	return true, nil
}

func (r *ZarfPackageReconciler) deploy(ctx context.Context, log logr.Logger, zarfPkg *opsv1alpha1.ZarfPackage) (ctrl.Result, error) {
	ctx, span := tracer.Start(ctx, "Deploy", trace.WithAttributes(
		attribute.String("source", zarfPkg.Spec.Source),
	))
	defer span.End()

	// Ensure client is available
	if r.ZarfClient == nil {
		log.Info("zarf sidecar not available for deployment, requeueing")
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			ReasonSidecarUnavailable, "Cannot deploy: Zarf sidecar is not available")
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonSidecarUnavailable,
				"Zarf sidecar is not available, waiting for reconnection")
		}
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhasePending
		r.recordFailure(zarfPkg)
		if r.maxRetriesExceeded(zarfPkg) {
			r.markMaxRetriesExceeded(zarfPkg, "waiting for Zarf sidecar")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
	}

	log.Info("deploying package", "source", zarfPkg.Spec.Source)

	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeploying
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeProgressing, metav1.ConditionTrue,
		ReasonDeploying, "Deployment in progress")
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonDeploying, "Starting package deployment")
	}

	// Build deploy options from spec
	timeout := 15 * time.Minute
	if zarfPkg.Spec.Timeout != "" {
		if parsed, err := time.ParseDuration(zarfPkg.Spec.Timeout); err == nil {
			timeout = parsed
		}
	}

	opts := zarf.DeployOptions{
		Source:                  zarfPkg.Spec.Source,
		Components:              zarfPkg.Spec.Components,
		SetVariables:            parseSetVariables(zarfPkg.Spec.Set),
		AdoptExistingResources:  zarfPkg.Spec.AdoptExistingResources,
		Timeout:                 timeout,
		Retries:                 zarfPkg.Spec.Retries,
		NamespaceOverride:       zarfPkg.Spec.Namespace,
		Shasum:                  zarfPkg.Spec.Shasum,
		SkipSignatureValidation: zarfPkg.Spec.SkipSignatureValidation,
		Architecture:            zarfPkg.Spec.Architecture,
		OCIConcurrency:          zarfPkg.Spec.OciConcurrency,
		PublicKeyPath:           zarfPkg.Spec.Key,
		LogLevel:                zarfPkg.Spec.LogLevel,
		LogFormat:               zarfPkg.Spec.LogFormat,
		NoColor:                 zarfPkg.Spec.NoColor,
		PlainHTTP:               zarfPkg.Spec.PlainHTTP,
		InsecureSkipTLSVerify:   zarfPkg.Spec.InsecureSkipTLSVerify,
		YoloMode:                zarfPkg.Spec.Yolo,
	}

	// Resolve registry credentials from referenced Secret
	if ref := zarfPkg.Spec.RegistryCredentialSecretRef; ref != "" {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: zarfPkg.Namespace, Name: ref}, &secret); err != nil {
			log.Error(err, "failed to get registry credential secret", "secret", ref)
			zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseFailed
			r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
				ReasonSecretNotFound, fmt.Sprintf("Registry credential secret %q not found: %v", ref, err))
			if r.recorder != nil {
				r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonSecretNotFound,
					fmt.Sprintf("Secret %q not found", ref))
			}
			return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
		}
		dockerCfg, ok := secret.Data[".dockerconfigjson"]
		if !ok {
			log.Error(nil, "registry credential secret missing .dockerconfigjson key", "secret", ref)
			zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseFailed
			r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
				ReasonSecretNotFound, fmt.Sprintf("Secret %q missing .dockerconfigjson key", ref))
			if r.recorder != nil {
				r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonSecretNotFound,
					fmt.Sprintf("Secret %q missing .dockerconfigjson key", ref))
			}
			return ctrl.Result{}, nil
		}
		opts.RegistryCredentialJSON = dockerCfg
		log.Info("resolved registry credentials from secret", "secret", ref)
	}

	result, err := r.ZarfClient.Deploy(ctx, opts)
	if err != nil {
		log.Error(err, "deployment failed")
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseFailed
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			ReasonDeployFailed, fmt.Sprintf("Deployment failed: %v", err))
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonDeployFailed, err.Error())
		}
		if isPermanentError(err) {
			log.Info("permanent deploy failure, not retrying", "error", err)
			return ctrl.Result{}, nil
		}
		r.recordFailure(zarfPkg)

		if r.maxRetriesExceeded(zarfPkg) {
			r.markMaxRetriesExceeded(zarfPkg, "deploying package")
			return ctrl.Result{}, nil
		}

		// Requeue for retry.
		return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
	}

	// Update status from result
	zarfPkg.Status.PackageName = result.PackageName
	zarfPkg.Status.DeployedVersion = result.Version
	zarfPkg.Status.DeployedGeneration = result.Generation
	zarfPkg.Status.Source = zarfPkg.Spec.Source
	zarfPkg.Status.ComponentStatuses = r.convertComponentStatuses(result.DeployedComponents)
	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeployed
	zarfPkg.Status.DeployedSpecHash = zarfPkg.Spec.DeploymentHash()
	r.resetFailures(zarfPkg)

	// Clear drift info after successful deploy (drift has been remediated)
	zarfPkg.Status.DriftInfo = nil
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeDriftDetected, metav1.ConditionFalse,
		ReasonDriftResolved, "No drift detected")
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionTrue,
		ReasonDeployed, "Package deployed successfully")
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeProgressing, metav1.ConditionFalse,
		ReasonDeployed, "Deployment complete")
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonDeployed,
			fmt.Sprintf("Package %s version %s deployed", result.PackageName, result.Version))
	}

	// Clear the redeploy annotation if present.
	// Use a MergeFrom patch on metadata only — this does not bump .metadata.generation
	// and will not re-trigger the reconcile loop.
	if _, ok := zarfPkg.Annotations[AnnotationRedeploy]; ok {
		patch := client.MergeFrom(zarfPkg.DeepCopy())
		delete(zarfPkg.Annotations, AnnotationRedeploy)
		if err := r.Patch(ctx, zarfPkg, patch); err != nil {
			log.Error(err, "failed to clear redeploy annotation")
			return ctrl.Result{}, err
		}
		log.Info("cleared redeploy annotation after successful deploy")
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonRedeployRequested,
				"Redeploy annotation cleared after successful deploy")
		}
	}

	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

func (r *ZarfPackageReconciler) handleDeletion(ctx context.Context, log logr.Logger, zarfPkg *opsv1alpha1.ZarfPackage) (ctrl.Result, error) {
	ctx, span := tracer.Start(ctx, "HandleDeletion", trace.WithAttributes(
		attribute.String("package", zarfPkg.Status.PackageName),
	))
	defer span.End()

	if !controllerutil.ContainsFinalizer(zarfPkg, ZarfPackageFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("removing package", "package", zarfPkg.Status.PackageName)
	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseRemoving
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonRemoving, "Removing package")
	}

	if zarfPkg.Status.PackageName != "" && r.ZarfClient != nil {
		if err := r.ZarfClient.Remove(ctx, zarf.RemoveOptions{
			PackageName: zarfPkg.Status.PackageName,
		}); err != nil {
			log.Error(err, "failed to remove package")
			r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
				ReasonRemoveFailed, fmt.Sprintf("Remove failed: %v", err))
			if r.recorder != nil {
				r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonRemoveFailed, err.Error())
			}
			if isPermanentError(err) {
				log.Info("permanent remove failure, not retrying", "error", err)
				return ctrl.Result{}, nil
			}
			r.recordFailure(zarfPkg)
			if r.maxRetriesExceeded(zarfPkg) {
				r.markMaxRetriesExceeded(zarfPkg, "removing package")
				return ctrl.Result{}, nil
			}
			return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
		}
	}

	r.resetFailures(zarfPkg)

	// Remove finalizer
	patch := client.MergeFrom(zarfPkg.DeepCopy())
	controllerutil.RemoveFinalizer(zarfPkg, ZarfPackageFinalizer)
	if err := r.Patch(ctx, zarfPkg, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ZarfPackageReconciler) syncStatusFromDeployed(zarfPkg *opsv1alpha1.ZarfPackage, deployedPkg *zarf.PackageInfo) {
	if deployedPkg == nil {
		return
	}
	zarfPkg.Status.PackageName = deployedPkg.Name
	zarfPkg.Status.DeployedVersion = deployedPkg.Version
	zarfPkg.Status.DeployedGeneration = deployedPkg.Generation
	zarfPkg.Status.ComponentStatuses = r.convertComponentStatuses(deployedPkg.DeployedComponents)
}

func (r *ZarfPackageReconciler) convertComponentStatuses(comps []zarf.DeployedComponent) []opsv1alpha1.ComponentStatus {
	result := make([]opsv1alpha1.ComponentStatus, 0, len(comps))
	for _, c := range comps {
		charts := make([]opsv1alpha1.InstalledChartStatus, 0, len(c.InstalledCharts))
		for _, ch := range c.InstalledCharts {
			charts = append(charts, opsv1alpha1.InstalledChartStatus{
				Namespace: ch.Namespace,
				ChartName: ch.ChartName,
				Status:    string(ch.Status),
			})
		}
		result = append(result, opsv1alpha1.ComponentStatus{
			Name:               c.Name,
			Status:             string(c.Status),
			InstalledCharts:    charts,
			ObservedGeneration: c.ObservedGeneration,
		})
	}
	return result
}

func (r *ZarfPackageReconciler) setCondition(zarfPkg *opsv1alpha1.ZarfPackage, condType opsv1alpha1.ZarfPackageConditionType, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	newCond := opsv1alpha1.ZarfPackageCondition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	}

	for i, c := range zarfPkg.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				zarfPkg.Status.Conditions[i] = newCond
			} else {
				// Only update reason/message, keep transition time
				zarfPkg.Status.Conditions[i].Reason = reason
				zarfPkg.Status.Conditions[i].Message = message
			}
			return
		}
	}
	zarfPkg.Status.Conditions = append(zarfPkg.Status.Conditions, newCond)
}

// parseSetVariables converts a slice of "KEY=value" strings to a map
func parseSetVariables(set []string) map[string]string {
	result := make(map[string]string)
	for _, s := range set {
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

func isPermanentError(err error) bool {
	st := status.Convert(err)
	switch st.Code() {
	case codes.InvalidArgument, codes.NotFound:
		return true
	default:
		return false
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ZarfPackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("zarfpackage-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.ZarfPackage{}).
		Complete(r)
}
