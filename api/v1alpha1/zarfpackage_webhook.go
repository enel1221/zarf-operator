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
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
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

const minimumUpgradePolicyInterval = time.Minute

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
	if pkg.Spec.UpgradePolicy != nil && pkg.Spec.UpgradePolicy.Enabled && pkg.Spec.UpgradePolicy.Strategy == "" {
		pkg.Spec.UpgradePolicy.Strategy = UpgradeStrategySemVer
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

	for i, dep := range pkg.Spec.DependsOn {
		if strings.TrimSpace(dep.Name) == "" {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "dependsOn").Index(i).Child("name"),
				dep.Name,
				"dependency name must not be empty",
			))
			continue
		}
		depNamespace := dep.Namespace
		if depNamespace == "" {
			depNamespace = pkg.Namespace
		}
		if dep.Name == pkg.Name && depNamespace == pkg.Namespace {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "dependsOn").Index(i),
				dep,
				"package cannot depend on itself",
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

	allErrs = append(allErrs, validateUpgradePolicy(pkg)...)

	return allErrs
}

func validateUpgradePolicy(pkg *ZarfPackage) field.ErrorList {
	var allErrs field.ErrorList
	policy := pkg.Spec.UpgradePolicy
	if policy == nil {
		return allErrs
	}

	path := field.NewPath("spec", "upgradePolicy")
	strategy := policy.Strategy
	if strategy == "" {
		strategy = UpgradeStrategySemVer
	}
	if strategy != UpgradeStrategySemVer {
		allErrs = append(allErrs, field.NotSupported(
			path.Child("strategy"),
			policy.Strategy,
			[]string{string(UpgradeStrategySemVer)},
		))
	}

	if policy.Interval != "" {
		interval, err := time.ParseDuration(policy.Interval)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(
				path.Child("interval"),
				policy.Interval,
				"invalid duration format",
			))
		} else if interval < minimumUpgradePolicyInterval {
			allErrs = append(allErrs, field.Invalid(
				path.Child("interval"),
				policy.Interval,
				"interval must be at least 1 minute",
			))
		}
	}

	if policy.SemverConstraint != "" {
		if _, err := semver.NewConstraint(policy.SemverConstraint); err != nil {
			allErrs = append(allErrs, field.Invalid(
				path.Child("semverConstraint"),
				policy.SemverConstraint,
				"invalid semantic version constraint",
			))
		}
	}

	if !policy.Enabled {
		return allErrs
	}

	sourceRef, err := parseUpgradePolicyOCISource(pkg.Spec.Source)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "source"),
			pkg.Spec.Source,
			err.Error(),
		))
		return allErrs
	}
	semverTag := strings.TrimPrefix(sourceRef.tag, "v")
	if sourceRef.tag != semverTag && strings.HasPrefix(semverTag, "v") {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "source"),
			pkg.Spec.Source,
			"upgradePolicy requires spec.source to use a semantic version tag",
		))
		return allErrs
	}
	if strings.Contains(sourceRef.tag, "+") {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "source"),
			pkg.Spec.Source,
			"upgradePolicy requires spec.source to use an OCI-compatible semantic version tag without build metadata",
		))
		return allErrs
	}
	if _, err := semver.StrictNewVersion(semverTag); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "source"),
			pkg.Spec.Source,
			"upgradePolicy requires spec.source to use a semantic version tag",
		))
	}

	return allErrs
}

type upgradePolicySourceRef struct {
	tag string
}

func parseUpgradePolicyOCISource(source string) (upgradePolicySourceRef, error) {
	if !strings.HasPrefix(source, "oci://") {
		return upgradePolicySourceRef{}, fmt.Errorf("upgradePolicy requires an OCI source")
	}
	trimmed := strings.TrimPrefix(source, "oci://")
	if strings.Contains(trimmed, "@") {
		return upgradePolicySourceRef{}, fmt.Errorf("upgradePolicy requires a tag source, not a digest source")
	}
	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	if lastColon <= lastSlash || lastColon == len(trimmed)-1 {
		return upgradePolicySourceRef{}, fmt.Errorf("upgradePolicy requires spec.source to include an explicit tag")
	}
	return upgradePolicySourceRef{tag: trimmed[lastColon+1:]}, nil
}
