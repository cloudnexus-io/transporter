package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PodMigrationSpec defines the desired state of a Pod migration.
type PodMigrationSpec struct {
	// PodName is the name of the pod to migrate.
	PodName string `json:"podName"`
	// Namespace is the namespace of the pod to migrate.
	Namespace string `json:"namespace"`
	// SourceNode is the name of the node where the pod is currently running.
	SourceNode string `json:"sourceNode,omitempty"`
	// TargetNode is the name of the node to which the pod should be migrated.
	TargetNode string `json:"targetNode"`
	// Strategy is the migration strategy, e.g. "live" or "cold".
	Strategy string `json:"strategy,omitempty"`
}

// PodMigrationPhase represents the phase of a migration.
// Valid values: Pending, Syncing, Finalizing, Completed, Failed.
type PodMigrationPhase string

const (
	PodMigrationPhasePending    PodMigrationPhase = "Pending"
	PodMigrationPhaseSyncing    PodMigrationPhase = "Syncing"
	PodMigrationPhaseFinalizing PodMigrationPhase = "Finalizing"
	PodMigrationPhaseCompleted  PodMigrationPhase = "Completed"
	PodMigrationPhaseFailed     PodMigrationPhase = "Failed"
)

// PodMigrationStatus defines the observed state of a Pod migration.
type PodMigrationStatus struct {
	// MigrationID is a unique identifier for this migration.
	MigrationID string `json:"migrationID,omitempty"`
	// Phase is the current phase of the migration.
	Phase PodMigrationPhase `json:"phase,omitempty"`
	// StartTime is when the migration started.
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// Message provides human-readable details about the current phase or any errors.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=podmigrations,scope=Namespaced,shortName=pm

// PodMigration is the Schema for the podmigrations API.
type PodMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodMigrationSpec   `json:"spec,omitempty"`
	Status PodMigrationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PodMigrationList contains a list of PodMigration.
type PodMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodMigration `json:"items"`
}

// DeepCopyObject implements runtime.Object for PodMigration.
func (in *PodMigration) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(PodMigration)
	*out = *in
	return out
}

// DeepCopyObject implements runtime.Object for PodMigrationList.
func (in *PodMigrationList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(PodMigrationList)
	*out = *in
	return out
}
