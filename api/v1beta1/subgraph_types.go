package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
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
	Endpoint             string           `json:"endpoint,omitempty"`
	Suspend              bool             `json:"suspend,omitempty"`
	SkipSchemaValidation bool             `json:"skipSchemaValidation,omitempty"`
	Timeout              *metav1.Duration `json:"timeout,omitempty"`
	Interval             *metav1.Duration `json:"interval,omitempty"`
	Schema               Schema           `json:"schema,omitempty"`
}

// +kubebuilder:validation:ExactlyOneOf=sdl;http
type Schema struct {
	SDL  *string     `json:"sdl,omitempty"`
	HTTP *SchemaHTTP `json:"http,omitempty"`
}

type SchemaHTTP struct {
	Endpoint string `json:"endpoint,omitempty"`
}

type SubGraphStatus struct {
	SHA256Checksum string `json:"sha256Checksum,omitempty"`

	// Schema
	Schema string `json:"schema,omitempty"`

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
