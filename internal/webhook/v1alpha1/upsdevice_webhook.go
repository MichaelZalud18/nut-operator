/*
Copyright 2026 Michael Zalud.

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
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var upsdevicelog = logf.Log.WithName("upsdevice-resource")

// SetupUPSDeviceWebhookWithManager registers the webhook for UPSDevice in the manager.
func SetupUPSDeviceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.UPSDevice{}).
		WithValidator(&UPSDeviceCustomValidator{}).
		WithDefaulter(&UPSDeviceCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-upsdevice,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=upsdevices,verbs=create;update,versions=v1alpha1,name=mupsdevice-v1alpha1.kb.io,admissionReviewVersions=v1

// UPSDeviceCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind UPSDevice when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type UPSDeviceCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind UPSDevice.
func (d *UPSDeviceCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.UPSDevice) error {
	upsdevicelog.Info("Defaulting for UPSDevice", "name", obj.GetName())

	if obj.Spec.UpstreamNUT != nil {
		if obj.Spec.UpstreamNUT.Port == nil {
			obj.Spec.UpstreamNUT.Port = ptrInt32(3493)
		}
		if obj.Spec.UpstreamNUT.StrictStart == nil {
			obj.Spec.UpstreamNUT.StrictStart = ptrBool(true)
		}
		if obj.Spec.UpstreamNUT.Auth.Mode == "" {
			obj.Spec.UpstreamNUT.Auth.Mode = powerv1alpha1.UPSUpstreamNUTAuthNone
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-upsdevice,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=upsdevices,verbs=create;update,versions=v1alpha1,name=vupsdevice-v1alpha1.kb.io,admissionReviewVersions=v1

// UPSDeviceCustomValidator struct is responsible for validating the UPSDevice resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type UPSDeviceCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type UPSDevice.
func (v *UPSDeviceCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.UPSDevice) (admission.Warnings, error) {
	upsdevicelog.Info("Validation for UPSDevice upon creation", "name", obj.GetName())

	return nil, validateUPSDeviceAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type UPSDevice.
func (v *UPSDeviceCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.UPSDevice) (admission.Warnings, error) {
	upsdevicelog.Info("Validation for UPSDevice upon update", "name", newObj.GetName())

	return nil, validateUPSDeviceAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type UPSDevice.
func (v *UPSDeviceCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.UPSDevice) (admission.Warnings, error) {
	upsdevicelog.Info("Validation for UPSDevice upon deletion", "name", obj.GetName())

	return nil, nil
}

func validateUPSDeviceAdmission(obj *powerv1alpha1.UPSDevice) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if obj.Spec.UpstreamNUT != nil {
		errs = append(errs, validateUpstreamNUTAdmission(obj, specPath)...)
	} else {
		if obj.Spec.Driver == "" {
			errs = append(errs, field.Required(specPath.Child("driver"), "required unless spec.upstreamNUT is set"))
		} else if isUnsupportedLocalUPSDriver(obj.Spec.Driver) {
			// F-103. Not field.NotSupported: its third argument is the *valid* set, so passing the
			// unsupported list reported usbhid-ups as unsupported and then listed it first among
			// "supported values". There is no valid set to offer here either -- the network
			// allowlist is not an alternative for a device wired over USB -- so say what is out of
			// scope instead of offering a substitution that does not exist.
			errs = append(errs, field.Invalid(specPath.Child("driver"), obj.Spec.Driver, unsupportedLocalUPSDriverMessage))
		} else if !isSupportedNetworkUPSDriver(obj.Spec.Driver) {
			errs = append(errs, field.NotSupported(specPath.Child("driver"), obj.Spec.Driver, supportedNetworkUPSDrivers()))
		}
		// Reserved here for the same reason it is reserved on the upstreamNUT path: ups.conf takes
		// the last `driver =` line, so overriding it in driverOptions would render a driver the
		// allowlist above never saw (F-85).
		if _, exists := obj.Spec.DriverOptions["driver"]; exists {
			errs = append(errs, field.Forbidden(specPath.Child("driverOptions").Key("driver"), "rendered from spec.driver; overriding it would bypass the driver allowlist"))
		}
		if obj.Spec.Driver != "" && obj.Spec.Driver != "dummy-ups" && obj.Spec.Endpoint == nil {
			errs = append(errs, field.Required(specPath.Child("endpoint"), "required for network-reachable NUT drivers"))
		}
		if obj.Spec.Endpoint != nil && obj.Spec.Endpoint.Host == "" {
			errs = append(errs, field.Required(specPath.Child("endpoint").Child("host"), "required when spec.endpoint is set"))
		}
	}

	if obj.Spec.Identity.Firmware != "" && obj.Spec.Identity.Model == "" {
		errs = append(errs, field.Required(specPath.Child("identity").Child("model"), "required when spec.identity.firmware is set"))
	}
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(
		powerv1alpha1.GroupVersion.WithKind("UPSDevice").GroupKind(),
		obj.GetName(),
		errs,
	)
}

func validateUpstreamNUTAdmission(obj *powerv1alpha1.UPSDevice, specPath *field.Path) field.ErrorList {
	upstream := obj.Spec.UpstreamNUT
	upstreamPath := specPath.Child("upstreamNUT")
	var errs field.ErrorList

	if obj.Spec.Driver != "" && obj.Spec.Driver != "dummy-ups" {
		errs = append(errs, field.Invalid(specPath.Child("driver"), obj.Spec.Driver, "must be empty or dummy-ups when spec.upstreamNUT is set"))
	}
	if obj.Spec.Endpoint != nil {
		errs = append(errs, field.Forbidden(specPath.Child("endpoint"), "cannot be set with spec.upstreamNUT"))
	}
	if obj.Spec.CredentialSecretRef != nil {
		errs = append(errs, field.Forbidden(specPath.Child("credentialSecretRef"), "use spec.upstreamNUT.auth.secretKeyRef for upstream NUT authconf"))
	}
	if upstream.Host == "" {
		errs = append(errs, field.Required(upstreamPath.Child("host"), "required for upstream NUT relay"))
	} else if strings.ContainsAny(upstream.Host, "\r\n[]@:") {
		errs = append(errs, field.Invalid(upstreamPath.Child("host"), upstream.Host, "must not contain control characters, brackets, @, or inline ports"))
	}
	if upstream.Port != nil && (*upstream.Port < 1 || *upstream.Port > 65535) {
		errs = append(errs, field.Invalid(upstreamPath.Child("port"), *upstream.Port, "must be between 1 and 65535"))
	}
	if upstream.UPSName == "" {
		errs = append(errs, field.Required(upstreamPath.Child("upsName"), "required for upstream NUT relay"))
	} else if strings.ContainsAny(upstream.UPSName, "\r\n[]@") {
		errs = append(errs, field.Invalid(upstreamPath.Child("upsName"), upstream.UPSName, "must not contain control characters, brackets, or @"))
	}
	for _, key := range []string{"port", "driver", "mode", "authconf", "repeater_disable_strict_start"} {
		if _, exists := obj.Spec.DriverOptions[key]; exists {
			errs = append(errs, field.Forbidden(specPath.Child("driverOptions").Key(key), "rendered from spec.upstreamNUT"))
		}
	}
	authPath := upstreamPath.Child("auth")
	switch upstreamAuthMode(upstream) {
	case powerv1alpha1.UPSUpstreamNUTAuthNone, powerv1alpha1.UPSUpstreamNUTAuthDefault:
		if upstream.Auth.SecretKeyRef != nil {
			errs = append(errs, field.Forbidden(authPath.Child("secretKeyRef"), "requires spec.upstreamNUT.auth.mode Secret"))
		}
	case powerv1alpha1.UPSUpstreamNUTAuthSecret:
		if upstream.Auth.SecretKeyRef == nil {
			errs = append(errs, field.Required(authPath.Child("secretKeyRef"), "required when spec.upstreamNUT.auth.mode is Secret"))
		}
	default:
		errs = append(errs, field.NotSupported(authPath.Child("mode"), upstream.Auth.Mode, []string{
			string(powerv1alpha1.UPSUpstreamNUTAuthNone),
			string(powerv1alpha1.UPSUpstreamNUTAuthDefault),
			string(powerv1alpha1.UPSUpstreamNUTAuthSecret),
		}))
	}
	return errs
}

func upstreamAuthMode(upstream *powerv1alpha1.UPSUpstreamNUTSpec) powerv1alpha1.UPSUpstreamNUTAuthMode {
	if upstream == nil || upstream.Auth.Mode == "" {
		return powerv1alpha1.UPSUpstreamNUTAuthNone
	}
	return upstream.Auth.Mode
}

func isSupportedNetworkUPSDriver(driver string) bool {
	for _, supported := range supportedNetworkUPSDrivers() {
		if driver == supported {
			return true
		}
	}
	return false
}

// supportedNetworkUPSDrivers is the admission allowlist, and every entry must exist in the
// nut-server operand image. hack/smoke-image.sh asserts the binaries are present and
// TestSmokeTestCoversEveryAllowlistedDriver asserts the two lists agree, because they drifted
// apart once already and nothing noticed (F-50).
//
// powerman-pdu was admitted here and is not in the image: images/nut-server/Dockerfile passes
// --without-powerman, and building it in would mean compiling libpowerman from source as well,
// since Alpine does not package it. Adding a second source build to every operand image for a
// driver no device in the inventory uses is the wrong trade against an allowlist entry that could
// never start, so the allowlist gives way rather than the image.
func supportedNetworkUPSDrivers() []string {
	return []string{"dummy-ups", "snmp-ups", "netxml-ups", "apcupsd-ups"}
}

// unsupportedLocalUPSDriverMessage states the scope boundary rather than naming a replacement.
// RB-1 and RB-2 keep this operator on network-reachable devices: a USB or serial driver needs the
// device attached to the pod's host, which the operand image cannot express and the operator has
// no way to schedule against.
const unsupportedLocalUPSDriverMessage = "requires local USB or serial attachment; USB and serial attachment is out of scope and this operator supports network-reachable UPS devices only"

func isUnsupportedLocalUPSDriver(driver string) bool {
	for _, unsupported := range unsupportedLocalUPSDrivers() {
		if driver == unsupported {
			return true
		}
	}
	return false
}

func unsupportedLocalUPSDrivers() []string {
	return []string{
		"usbhid-ups",
		"nutdrv_qx",
		"blazer_usb",
		"richcomm_usb",
		"riello_usb",
		"tripplite_usb",
		"apcsmart",
		"bcmxcp",
		"bestups",
		"belkin",
		"genericups",
		"liebert",
		"mge-shut",
		"powercom",
		"safenet",
		"solis",
		"tripplite",
		"victronups",
	}
}

func ptrBool(value bool) *bool {
	return &value
}

func ptrInt32(value int32) *int32 {
	return &value
}
