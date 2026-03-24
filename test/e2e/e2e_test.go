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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/enel1221/zarf-operator/test/utils"
)

// namespace where the operator is deployed
const namespace = "zarf-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "zarf-operator-controller-manager"

// metricsServiceName is the name of the metrics service
const metricsServiceName = "zarf-operator-controller-manager-metrics-service"

// metricsRoleBindingName for RBAC to allow metrics access
const metricsRoleBindingName = "zarf-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy",
			fmt.Sprintf("IMG=%s", projectImage),
			fmt.Sprintf("SIDECAR_IMG=%s", sidecarImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics",
			"-n", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("cleaning up metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding",
			metricsRoleBindingName, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName,
				"-c", "manager", "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n%s", controllerLogs)
			}

			By("Fetching sidecar logs")
			cmd = exec.Command("kubectl", "logs", controllerPodName,
				"-c", "zarf-sidecar", "-n", namespace)
			sidecarLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Sidecar logs:\n%s", sidecarLogs)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace,
				"--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName,
				"-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Pod description:\n%s", podDescription)
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Operator Lifecycle", func() {
		It("should run the controller-manager successfully", func() {
			By("validating that the controller-manager pod is running")
			verifyControllerUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods",
					"-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)
				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get", "pods", controllerPodName,
					"-o", "jsonpath={.status.phase}", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"))
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should run manager with configured concurrent reconcile workers", func() {
			By("verifying manager container args include --concurrent=5")
			verifyConcurrentArg := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"zarf-operator-controller-manager",
					"-n", namespace,
					"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='manager')].args}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("--concurrent=5"))
			}
			Eventually(verifyConcurrentArg).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for metrics access")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=zarf-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service exists")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints",
					metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"))
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying the metrics server has started")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName,
					"-c", "manager", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"))
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {"drop": ["ALL"]},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {"type": "RuntimeDefault"}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_total"))
		})
	})

	Context("ZarfPackage Deployment", func() {
		const (
			zarfPkgName      = "e2e-nginx"
			zarfPkgNamespace = "default"
			targetNamespace  = "e2e-test-nginx"
		)

		AfterEach(func() {
			// Clean up the ZarfPackage CR and target namespace after each test
			By("cleaning up the ZarfPackage CR")
			cmd := exec.Command("kubectl", "delete", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace, "--ignore-not-found=true", "--timeout=120s")
			_, _ = utils.Run(cmd)

			By("cleaning up the target namespace")
			cmd = exec.Command("kubectl", "delete", "ns", targetNamespace,
				"--ignore-not-found=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should deploy a Zarf package from an OCI registry", func() {
			By("applying a ZarfPackage CR pointing to the in-cluster registry")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
`, zarfPkgName, zarfPkgNamespace, registryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply ZarfPackage CR")

			By("verifying the ZarfPackage transitions to Deploying phase")
			verifyDeploying := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(SatisfyAny(
					Equal("Deploying"),
					Equal("Deployed"),
				), "Expected phase to be Deploying or Deployed, got: %s", output)
			}
			Eventually(verifyDeploying, 2*time.Minute).Should(Succeed())

			By("verifying the ZarfPackage reaches Deployed phase")
			verifyDeployed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"),
					"Expected phase Deployed, got: %s", output)
			}
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("verifying the nginx deployment exists in the target namespace")
			verifyNginxDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "nginx-test",
					"-n", targetNamespace,
					"-o", "jsonpath={.status.availableReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"),
					"Expected 1 available replica, got: %s", output)
			}
			Eventually(verifyNginxDeployment, 3*time.Minute).Should(Succeed())

			By("verifying ZarfPackage status fields are populated")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.packageName}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("e2e-test-nginx"))

			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.deployedVersion}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("0.0.1"))

			By("verifying component statuses are tracked")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.componentStatuses[0].name}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("nginx"))

			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.componentStatuses[0].status}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Succeeded"))

			By("verifying the Ready condition is True")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("True"))

			By("deleting the ZarfPackage CR and verifying cleanup")
			cmd = exec.Command("kubectl", "delete", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace, "--timeout=120s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete ZarfPackage CR")

			By("verifying the ZarfPackage CR is removed")
			verifyDeleted := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "ZarfPackage should be deleted")
			}
			Eventually(verifyDeleted, 2*time.Minute).Should(Succeed())
		})

		It("should deploy a Zarf package with specific components", func() {
			By("applying a ZarfPackage CR with component selection")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
  components:
    - nginx
`, zarfPkgName, zarfPkgNamespace, registryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply ZarfPackage CR")

			By("verifying the ZarfPackage reaches Deployed phase")
			verifyDeployed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("verifying the nginx deployment exists")
			verifyNginxDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "nginx-test",
					"-n", targetNamespace,
					"-o", "jsonpath={.status.availableReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"))
			}
			Eventually(verifyNginxDeployment, 3*time.Minute).Should(Succeed())

			By("verifying only the selected component is deployed")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.componentStatuses}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("nginx"))
		})

		It("should handle suspend and resume", func() {
			By("applying a ZarfPackage CR")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
`, zarfPkgName, zarfPkgNamespace, registryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the package to be deployed")
			verifyDeployed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("suspending the ZarfPackage")
			cmd = exec.Command("kubectl", "patch", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"--type=merge", "-p", `{"spec":{"suspend":true}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Suspended condition is set")
			verifySuspended := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Suspended')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}
			Eventually(verifySuspended, 30*time.Second).Should(Succeed())

			By("resuming the ZarfPackage")
			cmd = exec.Command("kubectl", "patch", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"--type=merge", "-p", `{"spec":{"suspend":false}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Suspended condition is removed")
			verifyResumed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Suspended')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(SatisfyAny(Equal("False"), BeEmpty()))
			}
			Eventually(verifyResumed, 30*time.Second).Should(Succeed())
		})

		It("should set Failed phase for an invalid OCI source", func() {
			By("applying a ZarfPackage CR with an invalid source")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://invalid.registry.example.com/does-not-exist:v99.99.99"
  skipSignatureValidation: true
`, zarfPkgName, zarfPkgNamespace)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply ZarfPackage CR")

			By("verifying the ZarfPackage transitions to Failed phase")
			verifyFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"),
					"Expected phase Failed, got: %s", output)
			}
			Eventually(verifyFailed, 2*time.Minute).Should(Succeed())

			By("verifying the Ready condition is False with DeployFailed reason")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("False"))

			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("DeployFailed"))

			By("verifying the Ready condition message contains an error description")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].message}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).NotTo(BeEmpty(), "Expected a descriptive error message in the Ready condition")
		})

		It("should add and remove the finalizer during lifecycle", func() {
			By("applying a ZarfPackage CR")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
`, zarfPkgName, zarfPkgNamespace, registryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply ZarfPackage CR")

			By("verifying the finalizer is added")
			verifyFinalizer := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.metadata.finalizers}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("zarfpackage.zarf.dev/finalizer"))
			}
			Eventually(verifyFinalizer, 60*time.Second).Should(Succeed())

			By("waiting for the package to be deployed")
			verifyDeployed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("deleting the ZarfPackage CR")
			cmd = exec.Command("kubectl", "delete", "zarfpackage", zarfPkgName,
				"-n", zarfPkgNamespace, "--timeout=120s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete ZarfPackage CR")

			By("verifying the CR is fully removed (finalizer cleaned up)")
			verifyDeleted := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", zarfPkgNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "ZarfPackage should be deleted")
			}
			Eventually(verifyDeleted, 2*time.Minute).Should(Succeed())
		})
	})

	Context("Operator Metrics After Reconciliation", func() {
		It("should expose controller-runtime reconciliation metrics", func() {
			By("creating a short-lived ZarfPackage CR to trigger reconciliation")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: metrics-test-pkg
  namespace: default
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
`, registryURL))
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for reconciliation to complete")
			verifyReconciled := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", "metrics-test-pkg",
					"-n", "default",
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(SatisfyAny(
					Equal("Deployed"),
					Equal("Failed"),
				))
			}
			Eventually(verifyReconciled, 5*time.Minute).Should(Succeed())

			By("getting a fresh service account token for metrics")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("cleaning up previous curl pod if it exists")
			cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics-reconcile",
				"-n", namespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			By("querying the metrics endpoint for reconciliation metrics")
			cmd = exec.Command("kubectl", "run", "curl-metrics-reconcile", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {"drop": ["ALL"]},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {"type": "RuntimeDefault"}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics-reconcile pod")

			By("waiting for the curl pod to complete")
			verifyCurlDone := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics-reconcile",
					"-o", "jsonpath={.status.phase}", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyCurlDone, 5*time.Minute).Should(Succeed())

			By("reading metrics output")
			cmd = exec.Command("kubectl", "logs", "curl-metrics-reconcile", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying controller reconcile total metric exists")
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_total"),
				"Expected controller_runtime_reconcile_total metric")

			By("verifying reconcile duration histogram exists")
			Expect(metricsOutput).To(ContainSubstring("controller_runtime_reconcile_time_seconds"),
				"Expected controller_runtime_reconcile_time_seconds metric")

			By("verifying work queue metrics exist")
			Expect(metricsOutput).To(ContainSubstring("workqueue_adds_total"),
				"Expected workqueue_adds_total metric")

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "zarfpackage", "metrics-test-pkg",
				"-n", "default", "--ignore-not-found=true", "--timeout=120s")
			_, _ = utils.Run(cmd)

			cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics-reconcile",
				"-n", namespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})
	})

	Context("Multi-Component Package", func() {
		const (
			multiPkgNamespace = "default"
			targetNamespace   = "e2e-test-multi"
		)

		deployAndAssertComponents := func(
			zarfPkgName string,
			selectedComponents []string,
			expectedConfigMaps []string,
			unexpectedConfigMaps []string,
			expectedComponentStatuses []string,
			unexpectedComponentStatuses []string,
		) {
			componentsYAML := ""
			if len(selectedComponents) > 0 {
				componentsYAML = "\n  components:\n"
				for _, component := range selectedComponents {
					componentsYAML += fmt.Sprintf("    - %s\n", component)
				}
			}

			By("applying a ZarfPackage CR")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-multi-component:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true%s
`, zarfPkgName, multiPkgNamespace, registryURL, componentsYAML)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the ZarfPackage reaches Deployed phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", multiPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}, 5*time.Minute).Should(Succeed())

			for _, configMapName := range expectedConfigMaps {
				By(fmt.Sprintf("verifying %s ConfigMap exists", configMapName))
				cmd = exec.Command("kubectl", "get", "configmap", configMapName,
					"-n", targetNamespace)
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("%s should exist", configMapName))
			}

			for _, configMapName := range unexpectedConfigMaps {
				By(fmt.Sprintf("verifying %s ConfigMap does NOT exist", configMapName))
				cmd = exec.Command("kubectl", "get", "configmap", configMapName,
					"-n", targetNamespace)
				_, err = utils.Run(cmd)
				Expect(err).To(HaveOccurred(), fmt.Sprintf("%s should NOT exist", configMapName))
			}

			By("verifying componentStatuses")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", multiPkgNamespace,
				"-o", "jsonpath={.status.componentStatuses[*].name}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			for _, componentName := range expectedComponentStatuses {
				Expect(output).To(ContainSubstring(componentName))
			}
			for _, componentName := range unexpectedComponentStatuses {
				Expect(output).NotTo(ContainSubstring(componentName))
			}
		}

		AfterEach(func() {
			By("cleaning up multi-component ZarfPackage CRs")
			for _, name := range []string{"e2e-multi-required", "e2e-multi-all"} {
				cmd := exec.Command("kubectl", "delete", "zarfpackage", name,
					"-n", multiPkgNamespace, "--ignore-not-found=true", "--timeout=120s")
				_, _ = utils.Run(cmd)
			}

			By("cleaning up the target namespace")
			cmd := exec.Command("kubectl", "delete", "ns", targetNamespace,
				"--ignore-not-found=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should deploy only explicitly selected components", func() {
			deployAndAssertComponents(
				"e2e-multi-required",
				[]string{"alpha"},
				[]string{"alpha-config"},
				[]string{"beta-config"},
				[]string{"alpha"},
				[]string{"beta"},
			)
		})

		It("should deploy all selected components including optional", func() {
			deployAndAssertComponents(
				"e2e-multi-all",
				[]string{"alpha", "beta"},
				[]string{"alpha-config", "beta-config"},
				nil,
				[]string{"alpha", "beta"},
				nil,
			)
		})
	})

	Context("Package Variables", func() {
		const (
			varPkgNamespace = "default"
			targetNamespace = "e2e-test-httpbin"
		)

		deployAndAssertVariables := func(
			zarfPkgName string,
			setValues []string,
			expectedReplicas string,
			expectedServicePort string,
		) {
			setYAML := ""
			if len(setValues) > 0 {
				setYAML = "\n  set:\n"
				for _, setValue := range setValues {
					setYAML += fmt.Sprintf("    - %q\n", setValue)
				}
			}

			By(fmt.Sprintf("applying ZarfPackage %s", zarfPkgName))
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-httpbin:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true%s
`, zarfPkgName, varPkgNamespace, registryURL, setYAML)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the ZarfPackage reaches Deployed phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", varPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}, 5*time.Minute).Should(Succeed())

			By(fmt.Sprintf("verifying the httpbin deployment has %s replicas", expectedReplicas))
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "httpbin",
					"-n", targetNamespace,
					"-o", "jsonpath={.spec.replicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(expectedReplicas))
			}, 3*time.Minute).Should(Succeed())

			By(fmt.Sprintf("verifying the httpbin service port is %s", expectedServicePort))
			cmd = exec.Command("kubectl", "get", "service", "httpbin",
				"-n", targetNamespace,
				"-o", "jsonpath={.spec.ports[0].port}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(expectedServicePort))
		}

		AfterEach(func() {
			By("cleaning up httpbin ZarfPackage CRs")
			for _, name := range []string{"e2e-httpbin-defaults", "e2e-httpbin-custom", "e2e-httpbin-update"} {
				cmd := exec.Command("kubectl", "delete", "zarfpackage", name,
					"-n", varPkgNamespace, "--ignore-not-found=true", "--timeout=120s")
				_, _ = utils.Run(cmd)
			}

			By("cleaning up the target namespace")
			cmd := exec.Command("kubectl", "delete", "ns", targetNamespace,
				"--ignore-not-found=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should deploy with default variable values", func() {
			deployAndAssertVariables("e2e-httpbin-defaults", nil, "1", "8080")
		})

		It("should deploy with custom variable values via spec.set", func() {
			deployAndAssertVariables(
				"e2e-httpbin-custom",
				[]string{"REPLICAS=3", "SERVICE_PORT=9090"},
				"3",
				"9090",
			)
		})

		It("should redeploy when spec.set changes", func() {
			const zarfPkgName = "e2e-httpbin-update"

			By("applying a ZarfPackage CR with REPLICAS=1")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: %s
  namespace: %s
spec:
  source: "oci://%s/e2e-test-httpbin:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
  set:
    - "REPLICAS=1"
`, zarfPkgName, varPkgNamespace, registryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for initial deploy")
			verifyDeployed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", varPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("capturing the initial deployedSpecHash")
			cmd = exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
				"-n", varPkgNamespace,
				"-o", "jsonpath={.status.deployedSpecHash}")
			initialHash, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(initialHash).NotTo(BeEmpty())

			By("patching spec.set to REPLICAS=2")
			cmd = exec.Command("kubectl", "patch", "zarfpackage", zarfPkgName,
				"-n", varPkgNamespace,
				"--type=merge", "-p", `{"spec":{"set":["REPLICAS=2"]}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the package redeploys with new hash")
			verifyNewHash := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", zarfPkgName,
					"-n", varPkgNamespace,
					"-o", "jsonpath={.status.deployedSpecHash}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty())
				g.Expect(output).NotTo(Equal(initialHash),
					"deployedSpecHash should change after spec.set update")
			}
			Eventually(verifyNewHash, 5*time.Minute).Should(Succeed())

			By("verifying the package returns to Deployed phase")
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("verifying the httpbin deployment has 2 replicas")
			verifyReplicas := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "httpbin",
					"-n", targetNamespace,
					"-o", "jsonpath={.spec.replicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("2"))
			}
			Eventually(verifyReplicas, 3*time.Minute).Should(Succeed())
		})
	})

	Context("Registry Authentication", func() {
		const (
			authPkgNamespace = "default"
			authTargetNs     = "e2e-test-nginx"
			authSecretName   = "e2e-auth-registry-cred"
		)

		AfterEach(func() {
			By("cleaning up registry auth test resources")
			for _, name := range []string{"e2e-auth-deploy", "e2e-auth-nosecret", "e2e-auth-nocreds"} {
				cmd := exec.Command("kubectl", "delete", "zarfpackage", name,
					"-n", authPkgNamespace, "--ignore-not-found=true", "--timeout=120s")
				_, _ = utils.Run(cmd)
			}
			cmd := exec.Command("kubectl", "delete", "secret", authSecretName,
				"-n", authPkgNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", authTargetNs,
				"--ignore-not-found=true", "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should deploy from an auth-protected registry with valid credentials", func() {
			By("creating a dockerconfigjson Secret with valid credentials")
			cmd := exec.Command("kubectl", "create", "secret", "docker-registry", authSecretName,
				"--docker-server="+authRegistryURL,
				"--docker-username=testuser",
				"--docker-password=testpass",
				"-n", authPkgNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create auth secret")

			By("applying a ZarfPackage CR with registryCredentialSecretRef")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: e2e-auth-deploy
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
  registryCredentialSecretRef: %s
`, authPkgNamespace, authRegistryURL, authSecretName)

			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the ZarfPackage reaches Deployed phase")
			verifyDeployed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-deploy",
					"-n", authPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"))
			}
			Eventually(verifyDeployed, 5*time.Minute).Should(Succeed())

			By("verifying the nginx deployment exists in the target namespace")
			verifyNginxDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "nginx-test",
					"-n", authTargetNs,
					"-o", "jsonpath={.status.availableReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"))
			}
			Eventually(verifyNginxDeployment, 3*time.Minute).Should(Succeed())

			By("verifying the Ready condition is True")
			cmd = exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-deploy",
				"-n", authPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("True"))
		})

		It("should fail with SecretNotFound when the referenced Secret does not exist", func() {
			By("applying a ZarfPackage CR referencing a non-existent secret")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: e2e-auth-nosecret
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
  registryCredentialSecretRef: nonexistent-secret
`, authPkgNamespace, authRegistryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the ZarfPackage transitions to Failed phase")
			verifyFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nosecret",
					"-n", authPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"))
			}
			Eventually(verifyFailed, 2*time.Minute).Should(Succeed())

			By("verifying the Ready condition shows SecretNotFound")
			cmd = exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nosecret",
				"-n", authPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("SecretNotFound"))

			By("verifying the Ready condition message mentions the secret name")
			cmd = exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nosecret",
				"-n", authPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].message}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("nonexistent-secret"))

			By("verifying a SecretNotFound event was emitted")
			verifyEvent := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events",
					"-n", authPkgNamespace,
					"--field-selector", "involvedObject.name=e2e-auth-nosecret,reason=SecretNotFound",
					"-o", "jsonpath={.items[0].message}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("nonexistent-secret"))
			}
			Eventually(verifyEvent, 30*time.Second).Should(Succeed())
		})

		It("should fail to pull from an auth-protected registry without credentials", func() {
			By("applying a ZarfPackage CR without registryCredentialSecretRef")
			zarfPkgYAML := fmt.Sprintf(`apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: e2e-auth-nocreds
  namespace: %s
spec:
  source: "oci://%s/e2e-test-nginx:0.0.1"
  plainHTTP: true
  yolo: true
  skipSignatureValidation: true
`, authPkgNamespace, authRegistryURL)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = utils.StringReader(zarfPkgYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the ZarfPackage transitions to Failed phase")
			verifyFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nocreds",
					"-n", authPkgNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"))
			}
			Eventually(verifyFailed, 2*time.Minute).Should(Succeed())

			By("verifying the Ready condition is False with DeployFailed reason")
			cmd = exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nocreds",
				"-n", authPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("False"))

			cmd = exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nocreds",
				"-n", authPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("DeployFailed"))

			By("verifying the error message indicates an authentication failure")
			cmd = exec.Command("kubectl", "get", "zarfpackage", "e2e-auth-nocreds",
				"-n", authPkgNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].message}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(SatisfyAny(
				ContainSubstring("unauthorized"),
				ContainSubstring("authentication required"),
				ContainSubstring("401"),
				ContainSubstring("basic credential not found"),
			))
		})
	})
})

// serviceAccountToken returns a token for the specified service account.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace, serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
