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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
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
	registrySecretRefKey = "spec.registryCredentialSecretRef"
	dependsOnRefKey      = "spec.dependsOnRef"
	backoffBaseInterval  = 10 * time.Second
	backoffMaxInterval   = 5 * time.Minute
	backoffMultiplier    = 2.0
	backoffMaxJitter     = 5 * time.Second
	dependencyRequeue    = 10 * time.Second
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
	ReasonStalled            = "Stalled"
)

// Error definitions
var (
	ErrDeployFailed   = errors.New("deployment failed")
	ErrRemoveFailed   = errors.New("removal failed")
	ErrClientNotReady = errors.New("zarf client not ready")
	tracer            = otel.Tracer("zarfpackage-controller")
	phaseMetric       = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "zarfpackage_phase_info",
			Help: "Current phase for each ZarfPackage (value is always 1 for the active phase).",
		},
		[]string{"namespace", "name", "phase"},
	)
	deployDurationMetric = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "zarfpackage_deploy_duration_seconds",
			Help:    "Duration of zarf package deploy attempts.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"result"},
	)
	failureMetric = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "zarfpackage_failures_total",
			Help: "Total number of failed controller operations by reason.",
		},
		[]string{"reason"},
	)
	driftDetectedMetric = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "zarfpackage_drift_detected_total",
			Help: "Total number of times drift was detected.",
		},
	)
	suspendedMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "zarfpackage_suspended",
			Help: "Whether a ZarfPackage is currently suspended (1) or active (0).",
		},
		[]string{"namespace", "name"},
	)
)

// ZarfPackageReconciler reconciles a ZarfPackage object
type ZarfPackageReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	ZarfClient              zarf.Client
	RequeueInterval         time.Duration
	MaxConcurrentReconciles int
	recorder                record.EventRecorder
	helmReleaseExistsFn     func(ctx context.Context, releaseName, namespace string) (bool, error)
	helmReleaseStatusFn     func(ctx context.Context, releaseName, namespace string) (release.Status, error)
	fixStuckHelmReleaseFn   func(ctx context.Context, releaseName, namespace string, state release.Status) error
	mu                      sync.RWMutex
}

// +kubebuilder:rbac:groups=zarf.dev,resources=zarfpackages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=zarf.dev,resources=zarfpackages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=zarf.dev,resources=zarfpackages/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
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
	log := logf.FromContext(ctx).WithValues("zarfpackage", req.NamespacedName)
	start := time.Now()

	// Fetch the ZarfPackage
	zarfPkg := &opsv1alpha1.ZarfPackage{}
	if err := r.Get(ctx, req.NamespacedName, zarfPkg); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Capture original object for status patching.
	original := zarfPkg.DeepCopy()

	// Defer status update
	defer func() {
		if !equality.Semantic.DeepEqual(&original.Status, &zarfPkg.Status) {
			zarfPkg.Status.LastReconcileTime = metav1.Now()
			zarfPkg.Status.ObservedGeneration = zarfPkg.Generation

			if updateErr := r.Status().Patch(ctx, zarfPkg, client.MergeFrom(original)); updateErr != nil {
				if apierrors.IsConflict(updateErr) {
					log.V(1).Info("conflict while updating status, will requeue")
					if err == nil {
						result = ctrl.Result{Requeue: true}
					}
					return
				}
				log.Error(updateErr, "failed to update status")
				if err == nil && result.IsZero() {
					err = updateErr
				}
			}
		}
		r.recordResourceMetrics(zarfPkg)
		log.Info("reconcile complete", "duration", time.Since(start))
	}()
	zarfClient := r.getZarfClient()

	// Handle deletion
	if !zarfPkg.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, log, zarfPkg, zarfClient)
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
		r.setSuspendedMetric(zarfPkg, true)
		return ctrl.Result{}, nil
	}

	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeSuspended, metav1.ConditionFalse,
		"Active", "Reconciliation is active")
	r.setSuspendedMetric(zarfPkg, false)

	// Check if zarf client is available
	if zarfClient == nil {
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			ReasonReconciling, "Waiting for Zarf sidecar to be ready")
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionFalse,
			ReasonReconciling, "Waiting for sidecar connectivity")
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonSidecarUnavailable,
				"Zarf sidecar is not available, waiting for reconnection")
		}
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhasePending
		r.recordFailure(zarfPkg)
		if r.maxRetriesExceeded(zarfPkg) {
			r.markMaxRetriesExceeded(zarfPkg, "waiting for Zarf sidecar")
			return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
		}
		return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
	}

	dependenciesMet, notReadyDeps, depErr := r.checkDependencies(ctx, zarfPkg)
	if depErr != nil {
		return ctrl.Result{}, depErr
	}
	if !dependenciesMet {
		message := fmt.Sprintf("Waiting for dependencies to be deployed: %s", strings.Join(notReadyDeps, ", "))
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeDependenciesMet, metav1.ConditionFalse,
			opsv1alpha1.ReasonDependenciesNotMet, message)
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			opsv1alpha1.ReasonDependenciesNotMet, message)
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionFalse,
			opsv1alpha1.ReasonDependenciesNotMet, "Waiting for dependency readiness")
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhasePending
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeNormal, opsv1alpha1.ReasonDependenciesNotMet, message)
		}
		return ctrl.Result{RequeueAfter: dependencyRequeue}, nil
	}
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeDependenciesMet, metav1.ConditionTrue,
		opsv1alpha1.ReasonDependenciesMet, "All dependencies are deployed")

	// Perform reconciliation
	return r.reconcile(ctx, log, zarfPkg, zarfClient, original)

	// return ctrl.Result{}, nil
}

func (r *ZarfPackageReconciler) reconcile(
	ctx context.Context,
	log logr.Logger,
	zarfPkg *opsv1alpha1.ZarfPackage,
	zarfClient zarf.Client,
	original *opsv1alpha1.ZarfPackage,
) (ctrl.Result, error) {
	// Check current deployment state
	deployedPkg, err := zarfClient.GetDeployedPackage(ctx, zarfPkg.Status.PackageName)
	if err != nil {
		log.Error(err, "failed to get deployed package state")
	}

	// Determine if we need to deploy or update
	needsDeploy := r.needsDeploy(ctx, zarfPkg, deployedPkg)

	if needsDeploy {
		return r.deploy(ctx, log, zarfPkg, zarfClient, original)
	}

	// Package is deployed and in sync
	r.syncStatusFromDeployed(zarfPkg, deployedPkg)
	r.resetFailures(zarfPkg)
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionTrue,
		ReasonDeployed, "Package deployed successfully")
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionFalse,
		ReasonDeployed, "Package is actively reconciling")
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
			driftDetectedMetric.Inc()
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

func buildRateLimiter() workqueue.TypedRateLimiter[reconcile.Request] {
	failureRateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
		1*time.Second,
		5*time.Minute,
	)

	totalRateLimiter := &workqueue.TypedBucketRateLimiter[reconcile.Request]{
		Limiter: rate.NewLimiter(rate.Limit(10), 100),
	}

	return workqueue.NewTypedMaxOfRateLimiter[reconcile.Request](failureRateLimiter, totalRateLimiter)
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
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionTrue,
		ReasonStalled, fmt.Sprintf("Controller stopped retrying after %d failures", zarfPkg.Status.FailureCount))
}

func (r *ZarfPackageReconciler) helmReleaseExists(ctx context.Context, releaseName, namespace string) (bool, error) {
	// Add timeout to prevent blocking the controller (following ESO pattern)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	actionConfig, err := r.initHelmActionConfig(namespace)
	if err != nil {
		return false, err
	}

	// Use History action to check if release exists
	historyClient := action.NewHistory(actionConfig)
	historyClient.Max = 1 // We only need to know if it exists

	_, err = historyClient.Run(releaseName)
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

func (r *ZarfPackageReconciler) deploy(
	ctx context.Context,
	log logr.Logger,
	zarfPkg *opsv1alpha1.ZarfPackage,
	zarfClient zarf.Client,
	original *opsv1alpha1.ZarfPackage,
) (ctrl.Result, error) {
	ctx, span := tracer.Start(ctx, "Deploy", trace.WithAttributes(
		attribute.String("source", zarfPkg.Spec.Source),
	))
	defer span.End()
	deployStart := time.Now()
	wasFailed := zarfPkg.Status.Phase == opsv1alpha1.ZarfPackagePhaseFailed

	log.Info("deploying package", "source", zarfPkg.Spec.Source)

	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeploying
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeProgressing, metav1.ConditionTrue,
		ReasonDeploying, "Deployment in progress")
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonDeploying, "Starting package deployment")
	}
	if patchErr := r.Status().Patch(ctx, zarfPkg, client.MergeFrom(original)); patchErr != nil {
		log.Error(patchErr, "failed to persist Deploying status")
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
			r.recordFailure(zarfPkg)
			r.recordFailureMetric(ReasonSecretNotFound)
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
			r.recordFailure(zarfPkg)
			r.recordFailureMetric(ReasonSecretNotFound)
			return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
		}
		opts.RegistryCredentialJSON = dockerCfg
		log.Info("resolved registry credentials from secret", "secret", ref)
	}

	if wasFailed {
		r.preDeployCleanup(ctx, log, zarfPkg, zarfClient)
	}

	deployCtx, cancel := context.WithTimeout(ctx, timeout+time.Minute)
	defer cancel()

	result, err := zarfClient.Deploy(deployCtx, opts)
	if err != nil {
		result, err = r.retryDeployAfterHelmRecovery(ctx, log, zarfPkg, zarfClient, opts, timeout, err)
	}
	if err != nil {
		r.observeDeployDuration(deployStart, "failure")
		log.Error(err, "deployment failed")
		zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseFailed
		r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			ReasonDeployFailed, fmt.Sprintf("Deployment failed: %v", err))
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonDeployFailed, truncateMessage(err.Error(), 512))
			var deployErr *zarf.DeployError
			if errors.As(err, &deployErr) {
				if deployErr.FailedComponent != "" {
					r.recorder.Eventf(zarfPkg, corev1.EventTypeWarning, ReasonDeployFailed,
						"Component %q failed (chart: %s): %s",
						deployErr.FailedComponent,
						emptyIfBlank(deployErr.FailedChart, "unknown"),
						truncateMessage(deployErr.Error(), 400),
					)
				}
				if len(deployErr.DeployLogs) > 0 {
					r.recorder.Event(zarfPkg, corev1.EventTypeWarning, "DeployLogs",
						formatLogSummary(deployErr.DeployLogs, 1024))
				}
			}
		}
		if isUserRecoverableError(err) {
			r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionTrue,
				ReasonStalled, "Waiting for user action to fix deployment input")
			r.recordFailureMetric(ReasonDeployFailed)
			log.Info("user-recoverable error, will recheck at standard interval", "error", err)
			return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
		}
		r.recordFailure(zarfPkg)
		r.recordFailureMetric(ReasonDeployFailed)

		if r.maxRetriesExceeded(zarfPkg) {
			r.markMaxRetriesExceeded(zarfPkg, "deploying package")
			return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
		}

		// Requeue for retry.
		return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
	}
	r.observeDeployDuration(deployStart, "success")
	r.applyDeploySuccessStatus(zarfPkg, result)

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

func (r *ZarfPackageReconciler) handleDeletion(ctx context.Context, log logr.Logger, zarfPkg *opsv1alpha1.ZarfPackage, zarfClient zarf.Client) (ctrl.Result, error) {
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

	if zarfPkg.Status.PackageName != "" && zarfClient != nil {
		removeCtx, cancel := context.WithTimeout(ctx, 16*time.Minute)
		defer cancel()

		if err := zarfClient.Remove(removeCtx, zarf.RemoveOptions{
			PackageName: zarfPkg.Status.PackageName,
		}); err != nil {
			st := status.Convert(err)
			if st.Code() == codes.NotFound {
				log.Info("package already absent during deletion, proceeding with finalizer removal", "package", zarfPkg.Status.PackageName)
			} else {
				log.Error(err, "failed to remove package")
				r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
					ReasonRemoveFailed, fmt.Sprintf("Remove failed: %v", err))
				if r.recorder != nil {
					r.recorder.Event(zarfPkg, corev1.EventTypeWarning, ReasonRemoveFailed, err.Error())
				}
				if isUserRecoverableError(err) {
					log.Info("user-recoverable error during removal, will recheck at standard interval", "error", err)
					return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
				}
				r.recordFailure(zarfPkg)
				r.recordFailureMetric(ReasonRemoveFailed)
				if r.maxRetriesExceeded(zarfPkg) {
					r.markMaxRetriesExceeded(zarfPkg, "removing package")
					return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
				}
				return ctrl.Result{RequeueAfter: calculateBackoff(zarfPkg.Status.FailureCount)}, nil
			}
		}
	}

	r.resetFailures(zarfPkg)
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionFalse,
		ReasonRemoving, "Deletion is progressing")

	// Remove finalizer
	patch := client.MergeFrom(zarfPkg.DeepCopy())
	controllerutil.RemoveFinalizer(zarfPkg, ZarfPackageFinalizer)
	if err := r.Patch(ctx, zarfPkg, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ZarfPackageReconciler) applyDeploySuccessStatus(zarfPkg *opsv1alpha1.ZarfPackage, result *zarf.DeployResult) {
	zarfPkg.Status.PackageName = result.PackageName
	zarfPkg.Status.DeployedVersion = result.Version
	zarfPkg.Status.DeployedGeneration = result.Generation
	zarfPkg.Status.Source = zarfPkg.Spec.Source
	zarfPkg.Status.ComponentStatuses = r.convertComponentStatuses(result.DeployedComponents)
	zarfPkg.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeployed
	zarfPkg.Status.DeployedSpecHash = zarfPkg.Spec.DeploymentHash()
	r.resetFailures(zarfPkg)

	// Clear drift info after successful deploy (drift has been remediated).
	zarfPkg.Status.DriftInfo = nil
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeDriftDetected, metav1.ConditionFalse,
		ReasonDriftResolved, "No drift detected")
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeReady, metav1.ConditionTrue,
		ReasonDeployed, "Package deployed successfully")
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeStalled, metav1.ConditionFalse,
		ReasonDeployed, "Deployment succeeded")
	r.setCondition(zarfPkg, opsv1alpha1.ConditionTypeProgressing, metav1.ConditionFalse,
		ReasonDeployed, "Deployment complete")
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeNormal, ReasonDeployed,
			fmt.Sprintf("Package %s version %s deployed", result.PackageName, result.Version))
	}
}

func (r *ZarfPackageReconciler) retryDeployAfterHelmRecovery(
	ctx context.Context,
	log logr.Logger,
	zarfPkg *opsv1alpha1.ZarfPackage,
	zarfClient zarf.Client,
	opts zarf.DeployOptions,
	timeout time.Duration,
	deployErr error,
) (*zarf.DeployResult, error) {
	var dErr *zarf.DeployError
	if !errors.As(deployErr, &dErr) || !dErr.IsHelmOperationInProgress() {
		return nil, deployErr
	}

	log.Info("detected stuck helm operation; attempting recovery")
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeWarning, "HelmStuck",
			"Detected helm operation in progress; attempting automatic recovery")
	}

	recovered := r.cleanupStuckHelmReleases(ctx, log, zarfPkg)
	if recovered == 0 {
		log.Info("helm recovery found no stuck releases")
		return nil, deployErr
	}

	retryCtx, retryCancel := context.WithTimeout(ctx, timeout+time.Minute)
	defer retryCancel()

	retryResult, retryErr := zarfClient.Deploy(retryCtx, opts)
	if retryErr != nil {
		log.Error(retryErr, "deployment retry failed after helm recovery")
		return nil, retryErr
	}
	if r.recorder != nil {
		r.recorder.Eventf(zarfPkg, corev1.EventTypeNormal, "HelmRecoverySucceeded",
			"Recovered %d stuck Helm release(s) and deployment retry succeeded", recovered)
	}

	return retryResult, nil
}

func (r *ZarfPackageReconciler) preDeployCleanup(ctx context.Context, log logr.Logger, zarfPkg *opsv1alpha1.ZarfPackage, zarfClient zarf.Client) {
	log.Info("running pre-deploy cleanup for previously failed package")

	cleanedReleases := r.cleanupStuckHelmReleases(ctx, log, zarfPkg)
	if cleanedReleases > 0 && r.recorder != nil {
		r.recorder.Eventf(zarfPkg, corev1.EventTypeWarning, "HelmCleanup",
			"Cleaned up %d stuck Helm release(s) before deploy retry", cleanedReleases)
	}

	if zarfPkg.Status.PackageName == "" {
		return
	}

	removeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	removeErr := zarfClient.Remove(removeCtx, zarf.RemoveOptions{
		PackageName: zarfPkg.Status.PackageName,
	})
	if removeErr == nil {
		log.Info("removed previous failed package state", "package", zarfPkg.Status.PackageName)
		if r.recorder != nil {
			r.recorder.Event(zarfPkg, corev1.EventTypeNormal, "PreDeployCleanup",
				"Removed previous failed package state before retry")
		}
		return
	}

	st := status.Convert(removeErr)
	if st.Code() == codes.NotFound {
		log.Info("previous package state already absent during pre-deploy cleanup", "package", zarfPkg.Status.PackageName)
		return
	}

	log.Error(removeErr, "pre-deploy remove failed; continuing with deploy")
	if r.recorder != nil {
		r.recorder.Event(zarfPkg, corev1.EventTypeWarning, "PreDeployCleanupFailed",
			truncateMessage(removeErr.Error(), 512))
	}
}

func (r *ZarfPackageReconciler) cleanupStuckHelmReleases(ctx context.Context, log logr.Logger, zarfPkg *opsv1alpha1.ZarfPackage) int {
	refs := uniqueChartRefs(zarfPkg.Status.ComponentStatuses)
	if len(refs) == 0 {
		return 0
	}

	cleaned := 0
	for _, ref := range refs {
		state, err := r.helmReleaseStatus(ctx, ref.name, ref.namespace)
		if err != nil {
			log.Error(err, "failed to check helm release state", "release", ref.name, "namespace", ref.namespace)
			continue
		}
		if !isStuckHelmStatus(state) {
			continue
		}

		log.Info("found stuck helm release", "release", ref.name, "namespace", ref.namespace, "state", state)
		if err := r.fixStuckHelmRelease(ctx, ref.name, ref.namespace, state); err != nil {
			log.Error(err, "failed to recover stuck helm release", "release", ref.name, "namespace", ref.namespace)
			continue
		}
		cleaned++
		if r.recorder != nil {
			r.recorder.Eventf(zarfPkg, corev1.EventTypeWarning, "HelmReleaseRecovered",
				"Recovered stuck Helm release %s/%s (state: %s)", ref.namespace, ref.name, state)
		}
	}

	return cleaned
}

func (r *ZarfPackageReconciler) helmReleaseStatus(ctx context.Context, releaseName, namespace string) (release.Status, error) {
	if r.helmReleaseStatusFn != nil {
		return r.helmReleaseStatusFn(ctx, releaseName, namespace)
	}

	actionConfig, err := r.initHelmActionConfig(namespace)
	if err != nil {
		return release.StatusUnknown, err
	}

	historyClient := action.NewHistory(actionConfig)
	historyClient.Max = 1
	releases, err := historyClient.Run(releaseName)
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return release.StatusUnknown, nil
		}
		return release.StatusUnknown, fmt.Errorf("failed to get release history: %w", err)
	}
	if len(releases) == 0 || releases[0].Info == nil {
		return release.StatusUnknown, nil
	}
	return releases[0].Info.Status, nil
}

func (r *ZarfPackageReconciler) fixStuckHelmRelease(ctx context.Context, releaseName, namespace string, state release.Status) error {
	if r.fixStuckHelmReleaseFn != nil {
		return r.fixStuckHelmReleaseFn(ctx, releaseName, namespace, state)
	}

	actionConfig, err := r.initHelmActionConfig(namespace)
	if err != nil {
		return err
	}

	switch state {
	case release.StatusPendingInstall:
		uninstall := action.NewUninstall(actionConfig)
		uninstall.KeepHistory = false
		_, err = uninstall.Run(releaseName)
		return err
	case release.StatusPendingUpgrade, release.StatusPendingRollback:
		rollback := action.NewRollback(actionConfig)
		rollback.Force = true
		rollback.CleanupOnFail = true
		return rollback.Run(releaseName)
	default:
		return nil
	}
}

func (r *ZarfPackageReconciler) initHelmActionConfig(namespace string) (*action.Configuration, error) {
	actionConfig := new(action.Configuration)
	configFlags := genericclioptions.NewConfigFlags(true)
	configFlags.Namespace = &namespace
	if err := actionConfig.Init(
		configFlags,
		namespace,
		"secret",
		func(string, ...interface{}) {},
	); err != nil {
		return nil, fmt.Errorf("failed to init helm action config: %w", err)
	}
	return actionConfig, nil
}

func isStuckHelmStatus(s release.Status) bool {
	switch s {
	case release.StatusPendingInstall, release.StatusPendingUpgrade, release.StatusPendingRollback:
		return true
	default:
		return false
	}
}

type chartRef struct {
	name      string
	namespace string
}

func uniqueChartRefs(components []opsv1alpha1.ComponentStatus) []chartRef {
	refs := make([]chartRef, 0)
	for _, comp := range components {
		for _, chart := range comp.InstalledCharts {
			if chart.ChartName == "" || chart.Namespace == "" {
				continue
			}
			ref := chartRef{name: chart.ChartName, namespace: chart.Namespace}
			if slices.Contains(refs, ref) {
				continue
			}
			refs = append(refs, ref)
		}
	}
	return refs
}

func truncateMessage(msg string, max int) string {
	if max <= 3 || len(msg) <= max {
		return msg
	}
	return msg[:max-3] + "..."
}

func formatLogSummary(lines []string, maxBytes int) string {
	if len(lines) == 0 || maxBytes <= 0 {
		return ""
	}

	kept := make([]string, 0, len(lines))
	const (
		prefix   = "Last deploy logs:"
		firstSep = " "
		entrySep = " | "
	)
	total := len(prefix)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		separatorLen := len(entrySep)
		if len(kept) == 0 {
			separatorLen = len(firstSep)
		}
		candidate := total + separatorLen + len(line)
		if candidate > maxBytes {
			break
		}
		kept = append(kept, line)
		total = candidate
	}

	if len(kept) == 0 {
		return truncateMessage(lines[len(lines)-1], maxBytes)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	return prefix + firstSep + strings.Join(kept, entrySep)
}

func emptyIfBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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
	apimeta.SetStatusCondition(&zarfPkg.Status.Conditions, metav1.Condition{
		Type:               string(condType),
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: zarfPkg.Generation,
	})
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

func (r *ZarfPackageReconciler) checkDependencies(ctx context.Context, zarfPkg *opsv1alpha1.ZarfPackage) (bool, []string, error) {
	if len(zarfPkg.Spec.DependsOn) == 0 {
		return true, nil, nil
	}

	notReady := make([]string, 0)
	for _, dep := range zarfPkg.Spec.DependsOn {
		if strings.TrimSpace(dep.Name) == "" {
			notReady = append(notReady, "<empty dependency name>")
			continue
		}
		depNamespace := dep.Namespace
		if depNamespace == "" {
			depNamespace = zarfPkg.Namespace
		}
		depKey := types.NamespacedName{Namespace: depNamespace, Name: dep.Name}

		var depPkg opsv1alpha1.ZarfPackage
		if err := r.Get(ctx, depKey, &depPkg); err != nil {
			if apierrors.IsNotFound(err) {
				notReady = append(notReady, fmt.Sprintf("%s/%s (not found)", depNamespace, dep.Name))
				continue
			}
			return false, nil, err
		}

		if depPkg.Status.Phase != opsv1alpha1.ZarfPackagePhaseDeployed {
			phase := depPkg.Status.Phase
			if phase == "" {
				phase = opsv1alpha1.ZarfPackagePhasePending
			}
			notReady = append(notReady, fmt.Sprintf("%s/%s (phase=%s)", depNamespace, dep.Name, phase))
		}
	}

	return len(notReady) == 0, notReady, nil
}

func isUserRecoverableError(err error) bool {
	st := status.Convert(err)
	switch st.Code() {
	case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
		return true
	default:
		return false
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ZarfPackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("zarfpackage-controller")
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.ZarfPackage{}, registrySecretRefKey, func(raw client.Object) []string {
		zarfPkg, ok := raw.(*opsv1alpha1.ZarfPackage)
		if !ok {
			return nil
		}
		if zarfPkg.Spec.RegistryCredentialSecretRef == "" {
			return nil
		}
		return []string{zarfPkg.Spec.RegistryCredentialSecretRef}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.ZarfPackage{}, dependsOnRefKey, func(raw client.Object) []string {
		zarfPkg, ok := raw.(*opsv1alpha1.ZarfPackage)
		if !ok || len(zarfPkg.Spec.DependsOn) == 0 {
			return nil
		}
		refs := make([]string, 0, len(zarfPkg.Spec.DependsOn))
		for _, dep := range zarfPkg.Spec.DependsOn {
			if dep.Name == "" {
				continue
			}
			depNamespace := dep.Namespace
			if depNamespace == "" {
				depNamespace = zarfPkg.Namespace
			}
			refs = append(refs, dependencyRefIndexValue(depNamespace, dep.Name))
		}
		return refs
	}); err != nil {
		return err
	}

	annotationPredicate := predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return true },
		GenericFunc: func(_ event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
				return true
			}
			return e.ObjectOld.GetAnnotations()[AnnotationRedeploy] != e.ObjectNew.GetAnnotations()[AnnotationRedeploy]
		},
	}
	dependencyPhasePredicate := predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return false },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObj, oldOK := e.ObjectOld.(*opsv1alpha1.ZarfPackage)
			newObj, newOK := e.ObjectNew.(*opsv1alpha1.ZarfPackage)
			if !oldOK || !newOK {
				return false
			}
			return oldObj.Status.Phase != newObj.Status.Phase
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.ZarfPackage{}, builder.WithPredicates(annotationPredicate)).
		Watches(
			&opsv1alpha1.ZarfPackage{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForDependents),
			builder.WithPredicates(dependencyPhasePredicate),
		).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.requestsForRegistrySecret)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.maxConcurrentReconciles(),
			RateLimiter:             buildRateLimiter(),
		}).
		Complete(r)
}

func (r *ZarfPackageReconciler) maxConcurrentReconciles() int {
	if r.MaxConcurrentReconciles <= 0 {
		return 1
	}
	return r.MaxConcurrentReconciles
}

func dependencyRefIndexValue(namespace, name string) string {
	return namespace + "/" + name
}

func (r *ZarfPackageReconciler) requestsForRegistrySecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	var list opsv1alpha1.ZarfPackageList
	if err := r.List(ctx, &list, client.InNamespace(secret.Namespace), client.MatchingFields{registrySecretRefKey: secret.Name}); err != nil {
		logf.FromContext(ctx).Error(err, "failed to map Secret to ZarfPackage list", "secret", secret.Name, "namespace", secret.Namespace)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&list.Items[i]),
		})
	}
	return requests
}

func (r *ZarfPackageReconciler) requestsForDependents(ctx context.Context, obj client.Object) []reconcile.Request {
	dependency, ok := obj.(*opsv1alpha1.ZarfPackage)
	if !ok {
		return nil
	}
	dependencyKey := dependencyRefIndexValue(dependency.Namespace, dependency.Name)

	var list opsv1alpha1.ZarfPackageList
	if err := r.List(ctx, &list, client.MatchingFields{dependsOnRefKey: dependencyKey}); err != nil {
		logf.FromContext(ctx).Error(err, "failed to map dependency package to dependents", "dependency", dependencyKey)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].Name == dependency.Name && list.Items[i].Namespace == dependency.Namespace {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&list.Items[i]),
		})
	}
	return requests
}

func (r *ZarfPackageReconciler) getZarfClient() zarf.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ZarfClient
}

func (r *ZarfPackageReconciler) SetZarfClient(zarfClient zarf.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ZarfClient = zarfClient
}

func (r *ZarfPackageReconciler) recordResourceMetrics(zarfPkg *opsv1alpha1.ZarfPackage) {
	if zarfPkg == nil {
		return
	}
	phases := []opsv1alpha1.ZarfPackagePhase{
		opsv1alpha1.ZarfPackagePhasePending,
		opsv1alpha1.ZarfPackagePhaseDeploying,
		opsv1alpha1.ZarfPackagePhaseDeployed,
		opsv1alpha1.ZarfPackagePhaseFailed,
		opsv1alpha1.ZarfPackagePhaseRemoving,
	}
	for _, phase := range phases {
		value := 0.0
		if zarfPkg.Status.Phase == phase {
			value = 1
		}
		phaseMetric.WithLabelValues(zarfPkg.Namespace, zarfPkg.Name, string(phase)).Set(value)
	}
}

func (r *ZarfPackageReconciler) observeDeployDuration(start time.Time, result string) {
	deployDurationMetric.WithLabelValues(result).Observe(time.Since(start).Seconds())
}

func (r *ZarfPackageReconciler) recordFailureMetric(reason string) {
	failureMetric.WithLabelValues(reason).Inc()
}

func (r *ZarfPackageReconciler) setSuspendedMetric(zarfPkg *opsv1alpha1.ZarfPackage, suspended bool) {
	value := 0.0
	if suspended {
		value = 1
	}
	suspendedMetric.WithLabelValues(zarfPkg.Namespace, zarfPkg.Name).Set(value)
}

func init() {
	ctrlmetrics.Registry.MustRegister(
		phaseMetric,
		deployDurationMetric,
		failureMetric,
		driftDetectedMetric,
		suspendedMetric,
	)
}
