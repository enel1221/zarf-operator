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
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-zarf-dev-v1alpha1-zarfpackage,mutating=true,failurePolicy=fail,sideEffects=None,groups=zarf.dev,resources=zarfpackages,verbs=create;update,versions=v1alpha1,name=mzarfpackage-v1alpha1.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-zarf-dev-v1alpha1-zarfpackage,mutating=false,failurePolicy=fail,sideEffects=None,groups=zarf.dev,resources=zarfpackages,verbs=create;update,versions=v1alpha1,name=vzarfpackage-v1alpha1.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &zarfPackageCustomDefaulter{}
var _ webhook.CustomValidator = &zarfPackageCustomValidator{}

func (r *ZarfPackage) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&ZarfPackage{}).
		WithDefaulter(&zarfPackageCustomDefaulter{}).
		WithValidator(&zarfPackageCustomValidator{}).
		Complete()
}

type zarfPackageCustomDefaulter struct{}

func (d *zarfPackageCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	pkg, ok := obj.(*ZarfPackage)
	if !ok {
		return fmt.Errorf("expected a ZarfPackage object but got %T", obj)
	}

	if pkg.Spec.Retries == 0 {
		pkg.Spec.Retries = 3
	}
	if pkg.Spec.Timeout == "" {
		pkg.Spec.Timeout = "15m"
	}
	if pkg.Spec.SyncPolicy == "" {
		pkg.Spec.SyncPolicy = SyncPolicyIgnore
	}
	if pkg.Spec.OciConcurrency == 0 {
		pkg.Spec.OciConcurrency = 6
	}

	return nil
}

type zarfPackageCustomValidator struct{}

func (v *zarfPackageCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	pkg, ok := obj.(*ZarfPackage)
	if !ok {
		return nil, fmt.Errorf("expected a ZarfPackage object but got %T", obj)
	}

	allErrs := validateZarfPackageSpec(pkg)
	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "ZarfPackage"}, pkg.Name, allErrs)
	}

	return nil, nil
}

func (v *zarfPackageCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldPkg, ok := oldObj.(*ZarfPackage)
	if !ok {
		return nil, fmt.Errorf("expected old object to be ZarfPackage but got %T", oldObj)
	}
	newPkg, ok := newObj.(*ZarfPackage)
	if !ok {
		return nil, fmt.Errorf("expected new object to be ZarfPackage but got %T", newObj)
	}

	allErrs := validateZarfPackageSpec(newPkg)

	if oldPkg.Status.Phase == ZarfPackagePhaseDeploying && oldPkg.Spec.Source != newPkg.Spec.Source {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "source"),
			"cannot change source while deployment is in progress",
		))
	}

	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "ZarfPackage"}, newPkg.Name, allErrs)
	}

	return nil, nil
}

func (v *zarfPackageCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateZarfPackageSpec(pkg *ZarfPackage) field.ErrorList {
	var allErrs field.ErrorList

	if pkg.Spec.Source == "" {
		allErrs = append(allErrs, field.Required(field.NewPath("spec", "source"), "source is required"))
	}

	for i, comp := range pkg.Spec.Components {
		if comp == "" {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "components").Index(i),
				comp,
				"component name must not be empty",
			))
		}
	}

	if pkg.Spec.Retries < 0 {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "retries"),
			pkg.Spec.Retries,
			"retries must be >= 0",
		))
	}

	if pkg.Spec.Timeout != "" {
		if _, err := time.ParseDuration(pkg.Spec.Timeout); err != nil {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "timeout"),
				pkg.Spec.Timeout,
				"invalid duration format",
			))
		}
	}

	return allErrs
}
