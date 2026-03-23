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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/enel1221/zarf-operator/api/v1alpha1"
	"github.com/enel1221/zarf-operator/pkg/zarf"
	"github.com/enel1221/zarf-operator/pkg/zarf/fake"
)

var _ = Describe("ZarfPackage Controller", func() {
	ctx := context.Background()
	const testPackageName = "pkg"

	newReconciler := func(zc zarf.Client) *ZarfPackageReconciler {
		return &ZarfPackageReconciler{
			Client:          k8sClient,
			Scheme:          k8sClient.Scheme(),
			Log:             logr.Discard(),
			ZarfClient:      zc,
			RequeueInterval: 5 * time.Minute,
			recorder:        record.NewFakeRecorder(100),
		}
	}

	createResource := func(name string, withFinalizer bool, source string) types.NamespacedName {
		resource := &opsv1alpha1.ZarfPackage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: opsv1alpha1.ZarfPackageSpec{Source: source},
		}
		if withFinalizer {
			resource.Finalizers = []string{ZarfPackageFinalizer}
		}

		nn := types.NamespacedName{Name: name, Namespace: "default"}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())

		DeferCleanup(func() {
			obj := &opsv1alpha1.ZarfPackage{}
			err := k8sClient.Get(ctx, nn, obj)
			if errors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
		})

		return nn
	}

	getResource := func(nn types.NamespacedName) *opsv1alpha1.ZarfPackage {
		obj := &opsv1alpha1.ZarfPackage{}
		Expect(k8sClient.Get(ctx, nn, obj)).To(Succeed())
		return obj
	}

	findCondition := func(conditions []opsv1alpha1.ZarfPackageCondition, condType opsv1alpha1.ZarfPackageConditionType) *opsv1alpha1.ZarfPackageCondition {
		for i := range conditions {
			if conditions[i].Type == condType {
				return &conditions[i]
			}
		}
		return nil
	}

	expectEventReason := func(rec *record.FakeRecorder, reason string) {
		Eventually(func() string {
			select {
			case event := <-rec.Events:
				return event
			default:
				return ""
			}
		}, 5*time.Second, 100*time.Millisecond).Should(ContainSubstring(reason))
	}

	Context("Deploy behavior", func() {
		It("should deploy a fresh ZarfPackage with no existing hash", func() {
			nn := createResource("fresh-pkg", false, "oci://example.com/pkg:v1")

			deployCalled := 0
			fakeZarf := fake.New().WithDeployFunc(func(_ context.Context, opts zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				Expect(opts.Source).To(Equal("oci://example.com/pkg:v1"))
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			})

			reconciler := newReconciler(fakeZarf)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseDeployed))
			Expect(updated.Status.DeployedSpecHash).NotTo(BeEmpty())
		})

		It("should redeploy when deployment hash changes", func() {
			nn := createResource("hash-mismatch-pkg", false, "oci://example.com/pkg:v1")

			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{Name: testPackageName, Version: "v1", Generation: 1}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: deployCalled}, nil
				})
			rec := record.NewFakeRecorder(100)
			reconciler := &ZarfPackageReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Log:             logr.Discard(),
				ZarfClient:      fakeZarf,
				RequeueInterval: 5 * time.Minute,
				recorder:        rec,
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))

			first := getResource(nn)
			firstHash := first.Status.DeployedSpecHash
			Expect(firstHash).NotTo(BeEmpty())

			first.Spec.Source = "oci://example.com/pkg:v2"
			Expect(k8sClient.Update(ctx, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(2))
			expectEventReason(rec, "SpecChanged")

			updated := getResource(nn)
			Expect(updated.Status.DeployedSpecHash).NotTo(Equal(firstHash))
		})

		It("should redeploy when a component is failed", func() {
			nn := createResource("failed-component-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = testPackageName
			obj.Status.DeployedSpecHash = obj.Spec.DeploymentHash()
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{
					Name:       testPackageName,
					Version:    "v1",
					Generation: 1,
					DeployedComponents: []zarf.DeployedComponent{
						{Name: "comp1", Status: zarf.ComponentStatusFailed},
					},
				}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 2}, nil
				})

			reconciler := newReconciler(fakeZarf)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))
		})

		It("should not redeploy when already in sync", func() {
			nn := createResource("in-sync-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = testPackageName
			obj.Status.DeployedSpecHash = obj.Spec.DeploymentHash()
			obj.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeployed
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{Name: testPackageName, Version: "v1", Generation: 1}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 2}, nil
				})

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(0))
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
		})

		It("should set failed status and requeue on deploy error", func() {
			nn := createResource("deploy-fail-pkg", true, "oci://example.com/pkg:v1")

			fakeZarf := fake.New().
				WithGetDeployedPackage(nil, nil).
				WithDeploy(nil, fmt.Errorf("connection refused"))

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">=", 2*backoffBaseInterval))
			Expect(result.RequeueAfter).To(BeNumerically("<", 2*backoffBaseInterval+backoffMaxJitter))

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseFailed))
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(ReasonDeployFailed))
			Expect(updated.Status.FailureCount).To(Equal(int32(1)))
			Expect(updated.Status.LastFailureTime).NotTo(BeNil())
		})

		It("should not requeue on permanent deploy error", func() {
			nn := createResource("deploy-permanent-fail-pkg", true, "oci://example.com/pkg:v1")

			fakeZarf := fake.New().
				WithGetDeployedPackage(nil, nil).
				WithDeploy(nil, status.Error(codes.InvalidArgument, "invalid source"))

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseFailed))
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Reason).To(Equal(ReasonDeployFailed))
			Expect(updated.Status.FailureCount).To(Equal(int32(0)))
		})

		It("should requeue when sidecar is unavailable", func() {
			nn := createResource("sidecar-unavailable-pkg", true, "oci://example.com/pkg:v1")

			rec := record.NewFakeRecorder(100)
			reconciler := &ZarfPackageReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Log:             logr.Discard(),
				ZarfClient:      nil,
				RequeueInterval: 5 * time.Minute,
				recorder:        rec,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">=", 2*backoffBaseInterval))
			Expect(result.RequeueAfter).To(BeNumerically("<", 2*backoffBaseInterval+backoffMaxJitter))
			expectEventReason(rec, ReasonSidecarUnavailable)

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhasePending))
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(ReasonReconciling))
			Expect(updated.Status.FailureCount).To(Equal(int32(1)))
		})

		It("should refresh status from deployed package when already deployed", func() {
			nn := createResource("sync-status-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = testPackageName
			obj.Status.DeployedSpecHash = obj.Spec.DeploymentHash()
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			fakeZarf := fake.New().WithGetDeployedPackage(&zarf.PackageInfo{
				Name:       "pkg-from-cluster",
				Version:    "2.0.0",
				Generation: 12,
				DeployedComponents: []zarf.DeployedComponent{
					{Name: "comp1", Status: zarf.ComponentStatusSucceeded},
				},
			}, nil)

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

			updated := getResource(nn)
			Expect(updated.Status.PackageName).To(Equal("pkg-from-cluster"))
			Expect(updated.Status.DeployedVersion).To(Equal("2.0.0"))
			Expect(updated.Status.DeployedGeneration).To(Equal(12))
			Expect(updated.Status.ComponentStatuses).To(HaveLen(1))
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should report drift without redeploy when syncPolicy is Detect", func() {
			nn := createResource("drift-detect-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Spec.SyncPolicy = opsv1alpha1.SyncPolicyDetect
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			obj = getResource(nn)
			obj.Status.PackageName = testPackageName
			obj.Status.DeployedSpecHash = obj.Spec.DeploymentHash()
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{
					Name:       testPackageName,
					Version:    "1.0.0",
					Generation: 1,
					DeployedComponents: []zarf.DeployedComponent{
						{
							Name:   "comp1",
							Status: zarf.ComponentStatusSucceeded,
							InstalledCharts: []zarf.InstalledChart{
								{Namespace: "default", ChartName: "missing-release"},
							},
						},
					},
				}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "1.0.0", Generation: 2}, nil
				})

			rec := record.NewFakeRecorder(100)
			reconciler := &ZarfPackageReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Log:             logr.Discard(),
				ZarfClient:      fakeZarf,
				RequeueInterval: 5 * time.Minute,
				recorder:        rec,
			}
			reconciler.helmReleaseExistsFn = func(_ context.Context, _ string, _ string) (bool, error) {
				return false, nil
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
			Expect(deployCalled).To(Equal(0))
			expectEventReason(rec, ReasonDriftDetected)

			updated := getResource(nn)
			Expect(updated.Status.DriftInfo).NotTo(BeNil())
			Expect(updated.Status.DriftInfo.Detected).To(BeTrue())
			Expect(updated.Status.DriftInfo.MissingReleases).To(ContainElement("default/missing-release"))
			drift := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeDriftDetected)
			Expect(drift).NotTo(BeNil())
			Expect(drift.Status).To(Equal(metav1.ConditionTrue))

			reconciler.helmReleaseExistsFn = func(_ context.Context, _ string, _ string) (bool, error) {
				return true, nil
			}
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			expectEventReason(rec, ReasonDriftResolved)
		})

		It("should redeploy on drift when syncPolicy is Remediate", func() {
			nn := createResource("drift-remediate-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Spec.SyncPolicy = opsv1alpha1.SyncPolicyRemediate
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			obj = getResource(nn)
			obj.Status.PackageName = testPackageName
			obj.Status.DeployedSpecHash = obj.Spec.DeploymentHash()
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{
					Name:       testPackageName,
					Version:    "1.0.0",
					Generation: 1,
					DeployedComponents: []zarf.DeployedComponent{
						{
							Name:   "comp1",
							Status: zarf.ComponentStatusSucceeded,
							InstalledCharts: []zarf.InstalledChart{
								{Namespace: "default", ChartName: "missing-release"},
							},
						},
					},
				}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "1.0.0", Generation: 2}, nil
				})

			reconciler := newReconciler(fakeZarf)
			reconciler.helmReleaseExistsFn = func(_ context.Context, _ string, _ string) (bool, error) {
				return false, nil
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))
		})

		It("should redeploy when redeploy annotation is set and clear it after deploy", func() {
			nn := createResource("redeploy-annotation-pkg", true, "oci://example.com/pkg:v1")

			// First reconcile to deploy and establish baseline
			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{Name: testPackageName, Version: "v1", Generation: 1}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: deployCalled}, nil
				})

			rec := record.NewFakeRecorder(100)
			reconciler := &ZarfPackageReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Log:             logr.Discard(),
				ZarfClient:      fakeZarf,
				RequeueInterval: 5 * time.Minute,
				recorder:        rec,
			}

			// Initial deploy
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))

			// Verify it's deployed and in sync (no redeploy on next reconcile)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1)) // still 1

			// Now add the redeploy annotation
			obj := getResource(nn)
			if obj.Annotations == nil {
				obj.Annotations = map[string]string{}
			}
			obj.Annotations[AnnotationRedeploy] = "true"
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			// Reconcile should trigger deploy
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(2))
			expectEventReason(rec, ReasonRedeployRequested)

			// Annotation should be cleared
			updated := getResource(nn)
			_, hasAnnotation := updated.Annotations[AnnotationRedeploy]
			Expect(hasAnnotation).To(BeFalse(), "redeploy annotation should be cleared after deploy")
		})

		It("should not enter a reconcile loop after clearing the redeploy annotation", func() {
			nn := createResource("redeploy-no-loop-pkg", true, "oci://example.com/pkg:v1")

			deployCalled := 0
			fakeZarf := fake.New().
				WithGetDeployedPackage(&zarf.PackageInfo{Name: testPackageName, Version: "v1", Generation: 1}, nil).
				WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deployCalled++
					return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: deployCalled}, nil
				})

			reconciler := newReconciler(fakeZarf)

			// Initial deploy
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))

			// Add redeploy annotation
			obj := getResource(nn)
			if obj.Annotations == nil {
				obj.Annotations = map[string]string{}
			}
			obj.Annotations[AnnotationRedeploy] = "1234567890"
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			// Reconcile triggers redeploy and clears annotation
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(2))

			// Next reconcile should NOT redeploy — annotation is gone
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(2), "should not redeploy again after annotation was cleared")
		})
	})

	Context("Deletion behavior", func() {
		It("should remove package and clear finalizer on deletion", func() {
			nn := createResource("delete-success-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = "pkg-to-remove"
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			removeCalled := 0
			removePackageName := ""
			fakeZarf := fake.New().WithRemove(nil)
			fakeZarf.RemoveFn = func(_ context.Context, opts zarf.RemoveOptions) error {
				removeCalled++
				removePackageName = opts.PackageName
				return nil
			}

			reconciler := newReconciler(fakeZarf)

			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(removeCalled).To(Equal(1))
			Expect(removePackageName).To(Equal("pkg-to-remove"))

			Eventually(func() bool {
				lookup := &opsv1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, nn, lookup)
				return errors.IsNotFound(err)
			}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
		})

		It("should requeue and keep finalizer on remove failure", func() {
			nn := createResource("delete-fail-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = "pkg-to-remove"
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			fakeZarf := fake.New().WithRemove(fmt.Errorf("timeout"))
			reconciler := newReconciler(fakeZarf)

			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">=", 2*backoffBaseInterval))
			Expect(result.RequeueAfter).To(BeNumerically("<", 2*backoffBaseInterval+backoffMaxJitter))

			updated := getResource(nn)
			Expect(controllerutil.ContainsFinalizer(updated, ZarfPackageFinalizer)).To(BeTrue())
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(ReasonRemoveFailed))
			Expect(updated.Status.FailureCount).To(Equal(int32(1)))
		})

		It("should stop retrying after max retries are exceeded", func() {
			nn := createResource("max-retries-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Spec.MaxRetries = 3
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			fakeZarf := fake.New().
				WithGetDeployedPackage(nil, nil).
				WithDeploy(nil, fmt.Errorf("connection refused"))

			rec := record.NewFakeRecorder(100)
			reconciler := &ZarfPackageReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Log:             logr.Discard(),
				ZarfClient:      fakeZarf,
				RequeueInterval: 5 * time.Minute,
				recorder:        rec,
			}

			for i := 0; i < 2; i++ {
				result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			updated := getResource(nn)
			Expect(updated.Status.FailureCount).To(Equal(int32(3)))
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseFailed))
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(ReasonMaxRetriesExceeded))
			expectEventReason(rec, ReasonMaxRetriesExceeded)
		})
	})

	Context("Backoff calculation", func() {
		It("should return base interval for zero failures", func() {
			d := calculateBackoff(0)
			Expect(d).To(BeNumerically("==", backoffBaseInterval))
		})

		It("should grow exponentially with higher failure counts", func() {
			d1 := calculateBackoff(1)
			d3 := calculateBackoff(3)
			Expect(d3).To(BeNumerically(">", d1))
		})

		It("should cap at max interval plus jitter", func() {
			d := calculateBackoff(100)
			Expect(d).To(BeNumerically("<=", backoffMaxInterval+backoffMaxJitter))
		})
	})

	Context("Suspend behavior", func() {
		It("should skip deploy and not requeue when suspended", func() {
			nn := createResource("suspended-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Spec.Suspend = true
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			})

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
			Expect(deployCalled).To(Equal(0))

			updated := getResource(nn)
			suspended := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeSuspended)
			Expect(suspended).NotTo(BeNil())
			Expect(suspended.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should resume normal reconciliation when suspend is cleared", func() {
			nn := createResource("resume-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Spec.Suspend = true
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			})
			reconciler := newReconciler(fakeZarf)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(0))

			obj = getResource(nn)
			obj.Spec.Suspend = false
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))

			updated := getResource(nn)
			suspended := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeSuspended)
			Expect(suspended).NotTo(BeNil())
			Expect(suspended.Status).To(Equal(metav1.ConditionFalse))
		})
	})

	Context("Registry credential behavior", func() {
		It("should pass registry credentials from a referenced Secret to deploy opts", func() {
			// Create dockerconfigjson secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "reg-cred", Namespace: "default"},
				Type:       corev1.SecretTypeDockerConfigJson,
				Data:       map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"ghcr.io":{"auth":"dGVzdA=="}}}`)},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })

			nn := createResource("reg-cred-pkg", true, "oci://example.com/pkg:v1")
			obj := getResource(nn)
			obj.Spec.RegistryCredentialSecretRef = "reg-cred"
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			var capturedOpts zarf.DeployOptions
			fakeZarf := fake.New().WithDeployFunc(func(_ context.Context, opts zarf.DeployOptions) (*zarf.DeployResult, error) {
				capturedOpts = opts
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			})

			reconciler := newReconciler(fakeZarf)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(capturedOpts.RegistryCredentialJSON).To(Equal([]byte(`{"auths":{"ghcr.io":{"auth":"dGVzdA=="}}}`)))
		})

		It("should fail when the referenced registry credential Secret does not exist", func() {
			nn := createResource("reg-missing-pkg", true, "oci://example.com/pkg:v1")
			obj := getResource(nn)
			obj.Spec.RegistryCredentialSecretRef = "nonexistent-secret"
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			fakeZarf := fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			})

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(deployCalled).To(Equal(0))

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseFailed))

			recorder := reconciler.recorder.(*record.FakeRecorder)
			expectEventReason(recorder, ReasonSecretNotFound)
		})
	})
})
