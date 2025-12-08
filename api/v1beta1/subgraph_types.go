package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type SubGraph struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubGraphSpec   `json:"spec,omitempty"`
	Status SubGraphStatus `json:"status,omitempty"`
}

// SubGraphList contains a list of SubGraph.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SubGraphList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubGraph `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubGraph{}, &SubGraphList{})
}

// SubGraphSpec
// +k8s:openapi-gen=true
type SubGraphSpec struct {
	Endpoint string  `json:"endpoint,omitempty"`
	Suspend  bool    `json:"suspend,omitempty"`
	Schema   *Schema `json:"schema,omitempty"`
}

type Schema struct {
	SDL string `json:"sdl,omitempty"`
}

type SubGraphStatus struct {
	// ConfigMap reference
	ConfigMap corev1.LocalObjectReference `json:"configMap,omitempty"`

	SHA256Checksum string `json:"sha256Checksum,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the conditions for the SuperGraph.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func SubGraphReconciling(subgraph SubGraph, status metav1.ConditionStatus, reason, message string) SubGraph {
	setResourceCondition(&subgraph, ConditionReconciling, status, reason, message, subgraph.Generation)
	return subgraph
}

func SubGraphReady(subgraph SubGraph, status metav1.ConditionStatus, reason, message string) SubGraph {
	setResourceCondition(&subgraph, ConditionReady, status, reason, message, subgraph.Generation)
	return subgraph
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SubGraph) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *SubGraph) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

func (in *SubGraph) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
