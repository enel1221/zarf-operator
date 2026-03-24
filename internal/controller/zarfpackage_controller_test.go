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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/enel1221/zarf-operator/api/v1alpha1"
	"github.com/enel1221/zarf-operator/pkg/zarf"
	"github.com/enel1221/zarf-operator/pkg/zarf/fake"
)

type failingStatusClient struct {
	client.Client
	statusErr error
}

func (c *failingStatusClient) Status() client.SubResourceWriter {
	return &failingStatusWriter{
		SubResourceWriter: c.Client.Status(),
		err:               c.statusErr,
	}
}

type failingStatusWriter struct {
	client.SubResourceWriter
	err error
}

func (w *failingStatusWriter) Update(_ context.Context, _ client.Object, _ ...client.SubResourceUpdateOption) error {
	return w.err
}

func (w *failingStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return w.err
}

var _ = Describe("ZarfPackage Controller", func() {
	ctx := context.Background()
	const (
		testPackageName     = "pkg"
		packageNameToRemove = "pkg-to-remove"
	)

	newReconciler := func(zc zarf.Client) *ZarfPackageReconciler {
		return &ZarfPackageReconciler{
			Client:          k8sClient,
			Scheme:          k8sClient.Scheme(),
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

	findCondition := func(conditions []metav1.Condition, condType opsv1alpha1.ZarfPackageConditionType) *metav1.Condition {
		for i := range conditions {
			if conditions[i].Type == string(condType) {
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

		It("should requeue at the standard interval on user-recoverable deploy error", func() {
			nn := createResource("deploy-permanent-fail-pkg", true, "oci://example.com/pkg:v1")

			fakeZarf := fake.New().
				WithGetDeployedPackage(nil, nil).
				WithDeploy(nil, status.Error(codes.InvalidArgument, "invalid source"))

			reconciler := newReconciler(fakeZarf)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseFailed))
			ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Reason).To(Equal(ReasonDeployFailed))
			Expect(updated.Status.FailureCount).To(Equal(int32(0)))
			recorder := reconciler.recorder.(*record.FakeRecorder)
			expectEventReason(recorder, ReasonDeployFailed)
		})

		It("should keep scheduled requeue when status update fails after reconcile result is set", func() {
			nn := createResource("status-update-fail-with-result-pkg", true, "oci://example.com/pkg:v1")

			fakeZarf := fake.New().
				WithGetDeployedPackage(nil, nil).
				WithDeploy(nil, fmt.Errorf("temporary network failure"))

			reconciler := &ZarfPackageReconciler{
				Client: &failingStatusClient{
					Client:    k8sClient,
					statusErr: fmt.Errorf("status update failed"),
				},
				Scheme:          k8sClient.Scheme(),
				ZarfClient:      fakeZarf,
				RequeueInterval: 5 * time.Minute,
				recorder:        record.NewFakeRecorder(100),
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		})

		It("should handle deploy gRPC error codes with correct requeue and status behavior", func() {
			testCases := []struct {
				name               string
				code               codes.Code
				expectStdInterval  bool
				expectFailureCount int32
			}{
				{
					name:               "invalid-argument-user-recoverable",
					code:               codes.InvalidArgument,
					expectStdInterval:  true,
					expectFailureCount: 0,
				},
				{
					name:               "not-found-user-recoverable",
					code:               codes.NotFound,
					expectStdInterval:  true,
					expectFailureCount: 0,
				},
				{
					name:               "permission-denied-user-recoverable",
					code:               codes.PermissionDenied,
					expectStdInterval:  true,
					expectFailureCount: 0,
				},
				{
					name:               "unauthenticated-user-recoverable",
					code:               codes.Unauthenticated,
					expectStdInterval:  true,
					expectFailureCount: 0,
				},
				{
					name:               "unavailable-transient",
					code:               codes.Unavailable,
					expectStdInterval:  false,
					expectFailureCount: 1,
				},
			}

			for _, tc := range testCases {
				nn := createResource(fmt.Sprintf("deploy-code-%s", tc.name), true, "oci://example.com/pkg:v1")
				rec := record.NewFakeRecorder(100)
				fakeZarf := fake.New().
					WithGetDeployedPackage(nil, nil).
					WithDeploy(nil, status.Error(tc.code, "deploy failed for test case"))

				reconciler := &ZarfPackageReconciler{
					Client:          k8sClient,
					Scheme:          k8sClient.Scheme(),
					ZarfClient:      fakeZarf,
					RequeueInterval: 5 * time.Minute,
					recorder:        rec,
				}

				result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
				Expect(err).NotTo(HaveOccurred(), tc.name)
				if tc.expectStdInterval {
					Expect(result.RequeueAfter).To(Equal(5*time.Minute), tc.name)
				} else {
					Expect(result.RequeueAfter).To(BeNumerically(">=", 2*backoffBaseInterval), tc.name)
					Expect(result.RequeueAfter).To(BeNumerically("<", 2*backoffBaseInterval+backoffMaxJitter), tc.name)
				}

				updated := getResource(nn)
				Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhaseFailed), tc.name)
				ready := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeReady)
				Expect(ready).NotTo(BeNil(), tc.name)
				Expect(ready.Reason).To(Equal(ReasonDeployFailed), tc.name)
				Expect(updated.Status.FailureCount).To(Equal(tc.expectFailureCount), tc.name)

				expectEventReason(rec, ReasonDeployFailed)
			}
		})

		It("should requeue when sidecar is unavailable", func() {
			nn := createResource("sidecar-unavailable-pkg", true, "oci://example.com/pkg:v1")

			rec := record.NewFakeRecorder(100)
			reconciler := &ZarfPackageReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
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

		It("should call deploy with context timeout from spec timeout plus one minute buffer", func() {
			nn := createResource("deploy-timeout-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Spec.Timeout = "2m"
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			var observed time.Duration
			fakeZarf := fake.New().
				WithGetDeployedPackage(nil, nil).
				WithDeployFunc(func(c context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
					deadline, ok := c.Deadline()
					Expect(ok).To(BeTrue())
					observed = time.Until(deadline)
					return nil, status.Error(codes.Unavailable, "temporary deploy failure")
				})

			reconciler := newReconciler(fakeZarf)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(observed).To(BeNumerically(">", 2*time.Minute+30*time.Second))
			Expect(observed).To(BeNumerically("<", 3*time.Minute+30*time.Second))
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

	Context("Dependency ordering", func() {
		It("should keep package pending when dependencies are not yet deployed", func() {
			dependencyNN := createResource("dep-unready", true, "oci://example.com/dep:v1")
			_ = dependencyNN

			nn := createResource("dependent-unready", true, "oci://example.com/pkg:v1")
			obj := getResource(nn)
			obj.Spec.DependsOn = []opsv1alpha1.DependsOnReference{{Name: "dep-unready"}}
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			reconciler := newReconciler(fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			}))

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(dependencyRequeue))
			Expect(deployCalled).To(Equal(0))

			updated := getResource(nn)
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhasePending))
			deps := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeDependenciesMet)
			Expect(deps).NotTo(BeNil())
			Expect(deps.Status).To(Equal(metav1.ConditionFalse))
			Expect(deps.Reason).To(Equal(opsv1alpha1.ReasonDependenciesNotMet))
		})

		It("should deploy once dependencies are deployed", func() {
			dependencyNN := createResource("dep-ready", true, "oci://example.com/dep:v1")
			dependencyObj := getResource(dependencyNN)
			dependencyObj.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeployed
			Expect(k8sClient.Status().Update(ctx, dependencyObj)).To(Succeed())

			nn := createResource("dependent-ready", true, "oci://example.com/pkg:v1")
			obj := getResource(nn)
			obj.Spec.DependsOn = []opsv1alpha1.DependsOnReference{{Name: "dep-ready"}}
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			reconciler := newReconciler(fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			}))

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))

			updated := getResource(nn)
			deps := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeDependenciesMet)
			Expect(deps).NotTo(BeNil())
			Expect(deps.Status).To(Equal(metav1.ConditionTrue))
			Expect(deps.Reason).To(Equal(opsv1alpha1.ReasonDependenciesMet))
		})

		It("should deploy immediately when no dependencies are declared", func() {
			nn := createResource("dependent-no-deps", true, "oci://example.com/pkg:v1")

			deployCalled := 0
			reconciler := newReconciler(fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			}))

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))
		})

		It("should keep package pending when dependency does not exist", func() {
			nn := createResource("dependent-missing-dep", true, "oci://example.com/pkg:v1")
			obj := getResource(nn)
			obj.Spec.DependsOn = []opsv1alpha1.DependsOnReference{{Name: "not-found"}}
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			reconciler := newReconciler(fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			}))

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(dependencyRequeue))
			Expect(deployCalled).To(Equal(0))

			updated := getResource(nn)
			deps := findCondition(updated.Status.Conditions, opsv1alpha1.ConditionTypeDependenciesMet)
			Expect(deps).NotTo(BeNil())
			Expect(deps.Message).To(ContainSubstring("not-found"))
			Expect(updated.Status.Phase).To(Equal(opsv1alpha1.ZarfPackagePhasePending))
		})

		It("should resolve dependencies across namespaces", func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "deps-ns"}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })

			dependency := &opsv1alpha1.ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dep-cross-ns",
					Namespace: "deps-ns",
				},
				Spec: opsv1alpha1.ZarfPackageSpec{Source: "oci://example.com/dep:v1"},
			}
			Expect(k8sClient.Create(ctx, dependency)).To(Succeed())
			DeferCleanup(func() {
				lookup := &opsv1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "dep-cross-ns", Namespace: "deps-ns"}, lookup)
				if err == nil {
					_ = k8sClient.Delete(ctx, lookup)
				}
			})

			dependency.Status.Phase = opsv1alpha1.ZarfPackagePhaseDeployed
			Expect(k8sClient.Status().Update(ctx, dependency)).To(Succeed())

			nn := createResource("dependent-cross-ns", true, "oci://example.com/pkg:v1")
			obj := getResource(nn)
			obj.Spec.DependsOn = []opsv1alpha1.DependsOnReference{
				{Name: "dep-cross-ns", Namespace: "deps-ns"},
			}
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())

			deployCalled := 0
			reconciler := newReconciler(fake.New().WithDeployFunc(func(_ context.Context, _ zarf.DeployOptions) (*zarf.DeployResult, error) {
				deployCalled++
				return &zarf.DeployResult{PackageName: testPackageName, Version: "v1", Generation: 1}, nil
			}))

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(deployCalled).To(Equal(1))
		})
	})

	Context("Deletion behavior", func() {
		It("should remove package and clear finalizer on deletion", func() {
			nn := createResource("delete-success-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = packageNameToRemove
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
			Expect(removePackageName).To(Equal(packageNameToRemove))

			Eventually(func() bool {
				lookup := &opsv1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, nn, lookup)
				return errors.IsNotFound(err)
			}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
		})

		It("should requeue and keep finalizer on remove failure", func() {
			nn := createResource("delete-fail-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = packageNameToRemove
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
			recorder := reconciler.recorder.(*record.FakeRecorder)
			expectEventReason(recorder, ReasonRemoveFailed)
		})

		It("should call remove with fixed sixteen minute timeout", func() {
			nn := createResource("delete-timeout-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = packageNameToRemove
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())
			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())

			var observed time.Duration
			fakeZarf := fake.New()
			fakeZarf.RemoveFn = func(c context.Context, _ zarf.RemoveOptions) error {
				deadline, ok := c.Deadline()
				Expect(ok).To(BeTrue())
				observed = time.Until(deadline)
				return status.Error(codes.Unavailable, "temporary remove failure")
			}

			reconciler := newReconciler(fakeZarf)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(observed).To(BeNumerically(">", 15*time.Minute+30*time.Second))
			Expect(observed).To(BeNumerically("<", 16*time.Minute+30*time.Second))
		})

		It("should treat remove not-found as successful cleanup and clear finalizer", func() {
			nn := createResource("delete-recoverable-fail-pkg", true, "oci://example.com/pkg:v1")

			obj := getResource(nn)
			obj.Status.PackageName = packageNameToRemove
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			fakeZarf := fake.New().WithRemove(status.Error(codes.NotFound, "package missing"))
			reconciler := newReconciler(fakeZarf)

			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			Eventually(func() bool {
				lookup := &opsv1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, nn, lookup)
				return errors.IsNotFound(err)
			}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
		})

		It("should requeue at the standard interval after max retries are exceeded", func() {
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
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

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

	Context("User recoverable error classification", func() {
		It("should treat permission denied as permanent", func() {
			Expect(isUserRecoverableError(status.Error(codes.PermissionDenied, "forbidden"))).To(BeTrue())
		})

		It("should treat unauthenticated as permanent", func() {
			Expect(isUserRecoverableError(status.Error(codes.Unauthenticated, "missing auth"))).To(BeTrue())
		})

		It("should treat unavailable as transient", func() {
			Expect(isUserRecoverableError(status.Error(codes.Unavailable, "temporary"))).To(BeFalse())
		})
	})

	Context("Controller rate limiter", func() {
		It("should apply at least one second backoff per failing item", func() {
			rl := buildRateLimiter()
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "pkg-a", Namespace: "default"}}

			first := rl.When(req)
			second := rl.When(req)

			Expect(first).To(BeNumerically(">=", time.Second))
			Expect(second).To(BeNumerically(">", first))
		})
	})

	Context("Controller concurrency", func() {
		It("should default max concurrent reconciles to one when unset", func() {
			reconciler := &ZarfPackageReconciler{}
			Expect(reconciler.maxConcurrentReconciles()).To(Equal(1))
		})

		It("should use configured max concurrent reconciles", func() {
			reconciler := &ZarfPackageReconciler{MaxConcurrentReconciles: 5}
			Expect(reconciler.maxConcurrentReconciles()).To(Equal(5))
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
