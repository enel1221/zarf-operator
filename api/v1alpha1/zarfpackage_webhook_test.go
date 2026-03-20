package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validPackage() *ZarfPackage {
	return &ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pkg", Namespace: "default"},
		Spec: ZarfPackageSpec{
			Source:  "oci://registry.example.com/test:v1.0.0",
			Retries: 3,
			Timeout: "15m",
		},
	}
}

func TestZarfPackageDefault(t *testing.T) {
	d := &zarfPackageCustomDefaulter{}
	pkg := &ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pkg"},
		Spec:       ZarfPackageSpec{Source: "oci://example.com/pkg:v1"},
	}

	if err := d.Default(context.Background(), pkg); err != nil {
		t.Fatalf("Default() returned error: %v", err)
	}
	if pkg.Spec.Retries != 3 {
		t.Errorf("expected Retries=3, got %d", pkg.Spec.Retries)
	}
	if pkg.Spec.Timeout != "15m" {
		t.Errorf("expected Timeout=15m, got %s", pkg.Spec.Timeout)
	}
	if pkg.Spec.SyncPolicy != SyncPolicyIgnore {
		t.Errorf("expected SyncPolicy=Ignore, got %s", pkg.Spec.SyncPolicy)
	}
	if pkg.Spec.OciConcurrency != 6 {
		t.Errorf("expected OciConcurrency=6, got %d", pkg.Spec.OciConcurrency)
	}
}

func TestZarfPackageDefaultDoesNotOverwrite(t *testing.T) {
	d := &zarfPackageCustomDefaulter{}
	pkg := &ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pkg"},
		Spec: ZarfPackageSpec{
			Source:         "oci://example.com/pkg:v1",
			Retries:        5,
			Timeout:        "30m",
			SyncPolicy:     SyncPolicyRemediate,
			OciConcurrency: 10,
		},
	}

	if err := d.Default(context.Background(), pkg); err != nil {
		t.Fatalf("Default() returned error: %v", err)
	}
	if pkg.Spec.Retries != 5 {
		t.Errorf("expected Retries=5, got %d", pkg.Spec.Retries)
	}
	if pkg.Spec.Timeout != "30m" {
		t.Errorf("expected Timeout=30m, got %s", pkg.Spec.Timeout)
	}
	if pkg.Spec.SyncPolicy != SyncPolicyRemediate {
		t.Errorf("expected SyncPolicy=Remediate, got %s", pkg.Spec.SyncPolicy)
	}
	if pkg.Spec.OciConcurrency != 10 {
		t.Errorf("expected OciConcurrency=10, got %d", pkg.Spec.OciConcurrency)
	}
}

func TestValidateCreate(t *testing.T) {
	v := &zarfPackageCustomValidator{}

	tests := []struct {
		name    string
		pkg     *ZarfPackage
		wantErr bool
	}{
		{
			name: "valid package",
			pkg:  validPackage(),
		},
		{
			name: "empty source",
			pkg: &ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{Name: "bad"},
				Spec:       ZarfPackageSpec{},
			},
			wantErr: true,
		},
		{
			name: "empty component name",
			pkg: &ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{Name: "bad"},
				Spec: ZarfPackageSpec{
					Source:     "oci://example.com/pkg:v1",
					Components: []string{"good", ""},
				},
			},
			wantErr: true,
		},
		{
			name: "negative retries",
			pkg: &ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{Name: "bad"},
				Spec: ZarfPackageSpec{
					Source:  "oci://example.com/pkg:v1",
					Retries: -1,
				},
			},
			wantErr: true,
		},
		{
			name: "invalid timeout",
			pkg: &ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{Name: "bad"},
				Spec: ZarfPackageSpec{
					Source:  "oci://example.com/pkg:v1",
					Timeout: "not-a-duration",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.ValidateCreate(context.Background(), tt.pkg)
			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateUpdateRejectsSourceChangeWhileDeploying(t *testing.T) {
	v := &zarfPackageCustomValidator{}

	oldPkg := validPackage()
	oldPkg.Status.Phase = ZarfPackagePhaseDeploying

	newPkg := validPackage()
	newPkg.Spec.Source = "oci://registry.example.com/other:v2.0.0"

	_, err := v.ValidateUpdate(context.Background(), oldPkg, newPkg)
	if err == nil {
		t.Error("expected error when changing source while deploying, got nil")
	}
}

func TestValidateUpdateAllowsUnchangedSourceWhileDeploying(t *testing.T) {
	v := &zarfPackageCustomValidator{}

	oldPkg := validPackage()
	oldPkg.Status.Phase = ZarfPackagePhaseDeploying

	newPkg := validPackage()
	// same source, different retries — should be allowed
	newPkg.Spec.Retries = 5

	_, err := v.ValidateUpdate(context.Background(), oldPkg, newPkg)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateDeleteAlwaysAllowed(t *testing.T) {
	v := &zarfPackageCustomValidator{}
	_, err := v.ValidateDelete(context.Background(), validPackage())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
