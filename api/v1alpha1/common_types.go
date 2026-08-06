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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionAccepted        = "Accepted"
	ConditionReady           = "Ready"
	ConditionReconciling     = "Reconciling"
	ConditionDegraded        = "Degraded"
	ConditionTriggerEligible = "TriggerEligible"
	ConditionExecutionReady  = "ExecutionReady"
)

// ObjectNameReference references another cluster-scoped power.zalud.io object by name.
type ObjectNameReference struct {
	// name is the referenced object's metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// NamespacedNameReference references a namespaced Kubernetes object.
type NamespacedNameReference struct {
	// namespace is the referenced object's namespace.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// name is the referenced object's metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SecretKeyReference references one key in a Kubernetes Secret.
type SecretKeyReference struct {
	// namespace is the Secret namespace.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// key is the data key inside the Secret.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// ImageReference describes an operand image without pinning users to one registry.
type ImageReference struct {
	// repository is the image repository, without the tag.
	// +optional
	Repository string `json:"repository,omitempty"`

	// tag is the image tag. Production installs should prefer digest pinning in packaging overlays.
	// +optional
	Tag string `json:"tag,omitempty"`

	// digest is an optional immutable image digest.
	// +optional
	Digest string `json:"digest,omitempty"`

	// pullPolicy controls when the kubelet pulls the image.
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// WorkloadPlacementSpec contains common workload scheduling controls.
type WorkloadPlacementSpec struct {
	// nodeSelector constrains operand pods to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations allows operand pods to schedule onto tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// affinity controls pod scheduling affinity and anti-affinity.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// priorityClassName sets the operand pod priority class.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
}

// PodHardeningSpec captures security controls applied to generated pods.
type PodHardeningSpec struct {
	// readOnlyRootFilesystem requests read-only container root filesystems.
	// +kubebuilder:default=true
	// +optional
	ReadOnlyRootFilesystem *bool `json:"readOnlyRootFilesystem,omitempty"`

	// seccompProfileType is the seccomp profile type for generated pods.
	// +kubebuilder:validation:Enum=RuntimeDefault;Localhost;Unconfined
	// +kubebuilder:default=RuntimeDefault
	// +optional
	SeccompProfileType string `json:"seccompProfileType,omitempty"`

	// runAsNonRoot requests non-root execution where the operand supports it.
	// +kubebuilder:default=true
	// +optional
	RunAsNonRoot *bool `json:"runAsNonRoot,omitempty"`
}

// ManagedResourceStatus records one Kubernetes resource managed for a CR.
type ManagedResourceStatus struct {
	// apiVersion is the managed resource apiVersion.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// kind is the managed resource kind.
	// +optional
	Kind string `json:"kind,omitempty"`

	// namespace is the managed resource namespace, when namespaced.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// name is the managed resource name.
	Name string `json:"name"`

	// hash is the rendered configuration hash, when applicable.
	// +optional
	Hash string `json:"hash,omitempty"`
}

// ServiceEndpointStatus reports a reachable endpoint produced by a controller.
type ServiceEndpointStatus struct {
	// name is the logical endpoint name.
	Name string `json:"name"`

	// namespace is the Service namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// dnsName is the in-cluster DNS name.
	// +optional
	DNSName string `json:"dnsName,omitempty"`

	// port is the service port.
	// +optional
	Port int32 `json:"port,omitempty"`
}

// OperandNamespaceSpec defines where the operator creates namespaced operands.
type OperandNamespaceSpec struct {
	// name is the namespace for generated workloads and Services.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=power-system
	Name string `json:"name"`

	// create allows the operator packaging to create the namespace.
	// +kubebuilder:default=true
	// +optional
	Create *bool `json:"create,omitempty"`
}

// PowerStorageMode selects durable storage for audit and execution state.
// +kubebuilder:validation:Enum=Disabled;ExternalPostgres;CNPG
type PowerStorageMode string

const (
	PowerStorageDisabled         PowerStorageMode = "Disabled"
	PowerStorageExternalPostgres PowerStorageMode = "ExternalPostgres"
	PowerStorageCNPG             PowerStorageMode = "CNPG"
)

// PowerStorageSpec configures durable audit and execution state storage.
type PowerStorageSpec struct {
	// mode selects the storage backend. Disabled is suitable only for development.
	// +kubebuilder:default=CNPG
	// +optional
	Mode PowerStorageMode `json:"mode,omitempty"`

	// externalPostgres points at an existing PostgreSQL database.
	// +optional
	ExternalPostgres *ExternalPostgresStorageSpec `json:"externalPostgres,omitempty"`

	// cnpg points at a CloudNativePG Cluster used for storage.
	// +optional
	CNPG *CNPGStorageSpec `json:"cnpg,omitempty"`

	// auditSpool enables a local JSONL fallback for audit records generated while PostgreSQL is unavailable during shutdown execution.
	// The configured path must be backed by a durable volume supplied by the deployment.
	// +optional
	AuditSpool AuditSpoolSpec `json:"auditSpool,omitempty"`

	// retention controls how long audit and telemetry records are retained.
	// +optional
	Retention *AuditRetentionSpec `json:"retention,omitempty"`
}

// AuditSpoolSpec configures a local fallback journal for shutdown-time audit records.
type AuditSpoolSpec struct {
	// enabled allows execution to continue when PostgreSQL writes fail after the audit store was opened.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// path is the absolute in-container directory where JSONL spool records are appended.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Path string `json:"path,omitempty"`

	// maxSize caps the spool journal, expressed as a Kubernetes quantity such as 64Mi.
	// A PostgreSQL outage has no bounded duration, so an uncapped journal grows until the
	// durable volume behind spec.storage.auditSpool.path is full. Once the cap is reached the
	// operator stops spooling and reports the loss on the ShutdownFlow rather than filling the
	// volume during a power event. Defaults to 64Mi.
	// +optional
	MaxSize *resource.Quantity `json:"maxSize,omitempty"`
}

// ExternalPostgresStorageSpec configures an existing PostgreSQL database.
type ExternalPostgresStorageSpec struct {
	// dsnSecretKeyRef contains a PostgreSQL DSN.
	DSNSecretKeyRef SecretKeyReference `json:"dsnSecretKeyRef"`

	// schema is the PostgreSQL schema used by this operator.
	// +kubebuilder:default=power
	// +optional
	Schema string `json:"schema,omitempty"`

	// requireTLS requires PostgreSQL TLS from operator-managed clients.
	// +kubebuilder:default=true
	// +optional
	RequireTLS *bool `json:"requireTLS,omitempty"`
}

// CNPGStorageSpec references a CloudNativePG Cluster.
type CNPGStorageSpec struct {
	// clusterRef references a postgresql.cnpg.io Cluster object.
	ClusterRef NamespacedNameReference `json:"clusterRef"`

	// database is the application database name.
	// +kubebuilder:default=power
	// +optional
	Database string `json:"database,omitempty"`

	// schema is the PostgreSQL schema used by this operator.
	// +kubebuilder:default=power
	// +optional
	Schema string `json:"schema,omitempty"`

	// appCredentialSecretRef points at credentials generated or accepted for the app user.
	// +optional
	AppCredentialSecretRef *NamespacedNameReference `json:"appCredentialSecretRef,omitempty"`
}

// AuditRetentionSpec configures durable state retention.
type AuditRetentionSpec struct {
	// events keeps power events and operator decisions for this duration.
	// +optional
	Events *metav1.Duration `json:"events,omitempty"`

	// telemetry keeps raw UPS telemetry snapshots for this duration.
	// +optional
	Telemetry *metav1.Duration `json:"telemetry,omitempty"`
}

// StorageStatus summarizes durable storage readiness.
type StorageStatus struct {
	// mode is the observed storage mode.
	// +optional
	Mode PowerStorageMode `json:"mode,omitempty"`

	// ready reports whether durable storage is usable.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// message summarizes the current storage state.
	// +optional
	Message string `json:"message,omitempty"`
}
