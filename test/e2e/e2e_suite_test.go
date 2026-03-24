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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/enel1221/zarf-operator/test/utils"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// projectImage is the name of the image which will be built and loaded
	// with the code source changes to be tested.
	projectImage = "example.com/zarf-operator:v0.0.1"
	sidecarImage = "example.com/zarf-operator-sidecar:v0.0.1"

	// registryURL is the in-cluster registry address used in ZarfPackage CRs.
	// The registry is deployed as a NodePort service in the e2e-registry namespace.
	registryURL = "registry.e2e-registry.svc.cluster.local:5000"

	// authRegistryURL is the in-cluster auth-protected registry address.
	// Credentials: testuser / testpass (htpasswd-based basic auth).
	authRegistryURL = "registry-auth.e2e-registry-auth.svc.cluster.local:5000"
)

// TestE2E runs the end-to-end (e2e) test suite for the project.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting zarf-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

func kindClusterExists(name string) bool {
	cmd := exec.Command("kind", "get", "clusters")
	output, err := utils.Run(cmd)
	if err != nil {
		return false
	}

	for _, cluster := range utils.GetNonEmptyLines(output) {
		if strings.TrimSpace(cluster) == name {
			return true
		}
	}

	return false
}

var _ = BeforeSuite(func() {
	kindCluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok && strings.TrimSpace(v) != "" {
		kindCluster = v
	}

	By("building the manager (operator) image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("building the sidecar image")
	cmd = exec.Command("make", "docker-build-sidecar", fmt.Sprintf("SIDECAR_IMG=%s", sidecarImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the sidecar image")

	if !kindClusterExists(kindCluster) {
		By("bootstrapping Kind e2e infrastructure")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"Kind cluster %q not found; running make e2e-setup for test prerequisites\n", kindCluster)
		cmd = exec.Command(
			"make",
			"e2e-setup",
			fmt.Sprintf("E2E_KIND_CLUSTER=%s", kindCluster),
			fmt.Sprintf("IMG=%s", projectImage),
			fmt.Sprintf("SIDECAR_IMG=%s", sidecarImage),
		)
		_, err = utils.Run(cmd)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to bootstrap Kind e2e infrastructure")
	}

	By("loading the manager image into Kind")
	err = utils.LoadImageToKindClusterWithName(projectImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	By("loading the sidecar image into Kind")
	err = utils.LoadImageToKindClusterWithName(sidecarImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the sidecar image into Kind")

	// Setup CertManager if not skipped and not already installed
	if !skipCertManagerInstall {
		By("checking if cert-manager is already installed")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "CertManager already installed, skipping...\n")
		}
	}
})

var _ = AfterSuite(func() {
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})
