package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	opsv1alpha1 "github.com/enel1221/zarf-operator/api/v1alpha1"
	"github.com/enel1221/zarf-operator/pkg/zarf"
	"github.com/enel1221/zarf-operator/pkg/zarf/fake"
	"helm.sh/helm/v3/pkg/release"
)

func TestIsStuckHelmStatus(t *testing.T) {
	if !isStuckHelmStatus(release.StatusPendingInstall) {
		t.Fatal("expected pending-install to be stuck")
	}
	if !isStuckHelmStatus(release.StatusPendingUpgrade) {
		t.Fatal("expected pending-upgrade to be stuck")
	}
	if isStuckHelmStatus(release.StatusDeployed) {
		t.Fatal("did not expect deployed to be stuck")
	}
}

func TestUniqueChartRefsDeduplicates(t *testing.T) {
	refs := uniqueChartRefs([]opsv1alpha1.ComponentStatus{
		{
			Name: "a",
			InstalledCharts: []opsv1alpha1.InstalledChartStatus{
				{Namespace: "default", ChartName: "argo-cd"},
				{Namespace: "default", ChartName: "argo-cd"},
			},
		},
		{
			Name: "b",
			InstalledCharts: []opsv1alpha1.InstalledChartStatus{
				{Namespace: "zarf", ChartName: "other"},
			},
		},
	})

	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d: %+v", len(refs), refs)
	}
}

func TestRetryDeployAfterHelmRecovery(t *testing.T) {
	deployCalls := 0
	fakeZarf := fake.New().WithDeployFunc(func(context.Context, zarf.DeployOptions) (*zarf.DeployResult, error) {
		deployCalls++
		return &zarf.DeployResult{
			PackageName: "pkg",
			Version:     "1.0.0",
			Generation:  1,
		}, nil
	})

	statusCalls := 0
	fixCalls := 0
	r := &ZarfPackageReconciler{
		helmReleaseStatusFn: func(context.Context, string, string) (release.Status, error) {
			statusCalls++
			return release.StatusPendingUpgrade, nil
		},
		fixStuckHelmReleaseFn: func(context.Context, string, string, release.Status) error {
			fixCalls++
			return nil
		},
	}
	pkg := &opsv1alpha1.ZarfPackage{
		Status: opsv1alpha1.ZarfPackageStatus{
			ComponentStatuses: []opsv1alpha1.ComponentStatus{
				{
					InstalledCharts: []opsv1alpha1.InstalledChartStatus{
						{Namespace: "default", ChartName: "argo-cd"},
					},
				},
			},
		},
	}

	result, err := r.retryDeployAfterHelmRecovery(
		context.Background(),
		logr.Discard(),
		pkg,
		fakeZarf,
		zarf.DeployOptions{Source: "oci://example.com/pkg:v1"},
		time.Minute,
		&zarf.DeployError{Err: errors.New("another operation (install/upgrade/rollback) is in progress")},
	)
	if err != nil {
		t.Fatalf("expected retry success, got error: %v", err)
	}
	if result == nil || result.PackageName != "pkg" {
		t.Fatalf("unexpected retry result: %+v", result)
	}
	if statusCalls == 0 || fixCalls == 0 || deployCalls != 1 {
		t.Fatalf("unexpected calls status=%d fix=%d deploy=%d", statusCalls, fixCalls, deployCalls)
	}
}

func TestPreDeployCleanupIgnoresNotFoundRemove(t *testing.T) {
	removeCalls := 0
	fakeZarf := fake.New()
	fakeZarf.RemoveFn = func(context.Context, zarf.RemoveOptions) error {
		removeCalls++
		return status.Error(codes.NotFound, "not found")
	}

	r := &ZarfPackageReconciler{}
	pkg := &opsv1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "default"},
		Status: opsv1alpha1.ZarfPackageStatus{
			PackageName: "pkg",
		},
	}

	r.preDeployCleanup(context.Background(), logr.Discard(), pkg, fakeZarf)
	if removeCalls != 1 {
		t.Fatalf("expected one remove call, got %d", removeCalls)
	}
}

func TestFormatLogSummaryUsesLastLines(t *testing.T) {
	summary := formatLogSummary([]string{
		"line-one",
		"line-two",
		"line-three",
	}, 60)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if contains := (strings.Contains(summary, "line-three") && strings.Contains(summary, "line-two")); !contains {
		t.Fatalf("expected summary to include newest lines, got: %q", summary)
	}
}

func TestFormatLogSummaryRespectsMaxBytes(t *testing.T) {
	lines := []string{
		"aaaaaaaa",
		"bbbbbbbb",
		"cccccccc",
	}
	maxBytes := 44

	summary := formatLogSummary(lines, maxBytes)
	if len(summary) > maxBytes {
		t.Fatalf("summary length %d exceeds maxBytes %d: %q", len(summary), maxBytes, summary)
	}
	if strings.Contains(summary, "aaaaaaaa") && strings.Contains(summary, "bbbbbbbb") && strings.Contains(summary, "cccccccc") {
		t.Fatalf("expected trimming to fit byte limit, got all lines: %q", summary)
	}
}
