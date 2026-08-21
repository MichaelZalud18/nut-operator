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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ShutdownHookSpec defines a pre-shutdown hook invocation.
type ShutdownHookSpec struct {
	// invocation is the request sent during an enforce-mode shutdown.
	Invocation ShutdownHookInvocationSpec `json:"invocation"`

	// dryRun is an optional author-declared rehearsal invocation. If omitted,
	// dry-run records the request that would have been sent without contacting
	// the target system.
	// +optional
	DryRun *ShutdownHookInvocationSpec `json:"dryRun,omitempty"`

	// failurePolicy controls how a failed hook is surfaced. Hooks are advisory:
	// they never hold a shutdown wave and never engage abortPolicy.
	// +kubebuilder:validation:Enum=MarkDegraded
	// +kubebuilder:default=MarkDegraded
	// +optional
	FailurePolicy ShutdownHookFailurePolicy `json:"failurePolicy,omitempty"`
}

// ShutdownHookInvocationSpec declares one concrete hook invocation.
type ShutdownHookInvocationSpec struct {
	// transport selects how the hook is delivered.
	// +kubebuilder:validation:Enum=HTTP;KubernetesObject
	// +kubebuilder:default=HTTP
	// +optional
	Transport ShutdownHookTransport `json:"transport,omitempty"`

	// timeout limits this hook delivery attempt. The executor may compress the
	// effective timeout when battery runtime forces faster wave timing.
	// +kubebuilder:default="10s"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// http declares the CloudEvents-over-HTTP invocation.
	// +optional
	HTTP *ShutdownHookHTTPSpec `json:"http,omitempty"`

	// kubernetesObject declares a raw Kubernetes object to create. This is a
	// generic transport, not an Argo-specific workflow path; the install must
	// grant RBAC for the selected GVK before using it.
	// +optional
	KubernetesObject *ShutdownHookKubernetesObjectSpec `json:"kubernetesObject,omitempty"`
}

// ShutdownHookTransport is the hook delivery mechanism.
// +kubebuilder:validation:Enum=HTTP;KubernetesObject
type ShutdownHookTransport string

const (
	// ShutdownHookTransportHTTP sends a CloudEvents-shaped HTTP request.
	ShutdownHookTransportHTTP ShutdownHookTransport = "HTTP"
	// ShutdownHookTransportKubernetesObject creates the supplied Kubernetes object.
	ShutdownHookTransportKubernetesObject ShutdownHookTransport = "KubernetesObject"
)

// ShutdownHookFailurePolicy describes how hook failures are reported.
// +kubebuilder:validation:Enum=MarkDegraded
type ShutdownHookFailurePolicy string

const (
	// ShutdownHookFailureMarkDegraded records advisory failure evidence and marks
	// the owning ShutdownFlow degraded without aborting the flow.
	ShutdownHookFailureMarkDegraded ShutdownHookFailurePolicy = "MarkDegraded"
)

// ShutdownHookHTTPSpec declares the CloudEvents-over-HTTP delivery target.
type ShutdownHookHTTPSpec struct {
	// url is the endpoint that receives the CloudEvents-shaped JSON body.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// method is the HTTP method. POST is the only supported method for v1alpha1
	// hooks so receivers get one predictable contract.
	// +kubebuilder:validation:Enum=POST
	// +kubebuilder:default=POST
	// +optional
	Method string `json:"method,omitempty"`

	// cloudEventType overrides the CloudEvents type. Empty uses the operator default.
	// +optional
	CloudEventType string `json:"cloudEventType,omitempty"`

	// cloudEventSource overrides the CloudEvents source. Empty uses the
	// ShutdownFlow name at execution time.
	// +optional
	CloudEventSource string `json:"cloudEventSource,omitempty"`

	// headers are non-secret HTTP headers. Sensitive headers such as
	// Authorization must be supplied through secretHeaders.
	// +listType=map
	// +listMapKey=name
	// +optional
	Headers []ShutdownHookHTTPHeader `json:"headers,omitempty"`

	// secretHeaders are HTTP headers whose values are read from Secrets.
	// +listType=map
	// +listMapKey=name
	// +optional
	SecretHeaders []ShutdownHookHTTPSecretHeader `json:"secretHeaders,omitempty"`

	// data adds static string fields under the CloudEvents data.extensions object.
	// +optional
	Data map[string]string `json:"data,omitempty"`
}

// ShutdownHookHTTPHeader is one non-secret HTTP header.
type ShutdownHookHTTPHeader struct {
	// name is the HTTP header name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// value is the literal header value.
	// +optional
	Value string `json:"value,omitempty"`
}

// ShutdownHookHTTPSecretHeader is one HTTP header sourced from a Secret key.
type ShutdownHookHTTPSecretHeader struct {
	// name is the HTTP header name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// valueFrom identifies the Secret key containing the header value.
	ValueFrom SecretKeyReference `json:"valueFrom"`
}

// ShutdownHookKubernetesObjectSpec declares a raw Kubernetes object create.
type ShutdownHookKubernetesObjectSpec struct {
	// object is the Kubernetes object body to create. The object must carry
	// apiVersion, kind, metadata, and either metadata.name or metadata.generateName.
	// +kubebuilder:pruning:PreserveUnknownFields
	Object runtime.RawExtension `json:"object"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// ShutdownHook is the Schema for the shutdownhooks API.
//
// Deliberately statusless (F-96). It declared a status subresource and a conditions array that
// nothing ever wrote, because nothing reconciles the kind — and a permanently empty `status: {}` is
// indistinguishable from a controller that has stalled, which is worse than having no status field
// at all.
//
// Nothing should reconcile it either, and the two things a status could have carried both belong
// elsewhere:
//
//   - Hook *health* would mean probing declared endpoints on a schedule. `GP-4` excludes that: the
//     operator consumes signals from existing monitoring rather than becoming monitoring.
//   - Hook *outcome* already lands on the owning `ShutdownFlow`. `OD-34` makes hooks advisory, so a
//     failed or timed-out delivery degrades the flow, and the flow is the thing that was affected.
//     Recording it twice would be two places to keep in sync and one to disagree.
//
// A hook is a declaration, not a workload: there is no operand behind it and nothing to drift, so
// admission is where it gets told it is wrong.
type ShutdownHook struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ShutdownHook
	// +required
	Spec ShutdownHookSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ShutdownHookList contains a list of ShutdownHook
type ShutdownHookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ShutdownHook `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ShutdownHook{}, &ShutdownHookList{})
		return nil
	})
}
