/*


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

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""

// SuperGraphSchema is the Schema for the SuperGraphSchemas API
type SuperGraphSchema struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SuperGraphSchemaSpec   `json:"spec,omitempty"`
	Status SuperGraphSchemaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SuperGraphSchemaList contains a list of SuperGraphSchema
type SuperGraphSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SuperGraphSchema `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SuperGraphSchema{}, &SuperGraphSchemaList{})
}

// SuperGraphSchemaSpec defines the desired state of SuperGraphSchema
type SuperGraphSchemaSpec struct {
	// Suspend reconciliation
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// FederationVersion
	// +kubebuilder:default="2"
	FederationVersion string `json:"federationVersion"`

	Timeout  *metav1.Duration `json:"timeout,omitempty"`
	Interval *metav1.Duration `json:"interval,omitempty"`

	ReconcilerTemplate *ReconcilerTemplate `json:"reconcilerTemplate,omitempty"`

	// SubgraphSelector defines a selector to select subgraphs associated with this schema
	SubGraphSelector *metav1.LabelSelector `json:"subGraphSelector,omitempty"`

	// NamespaceSelector defines a selector to select namespaces where subgraphs are looked up
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
}

type ReconcilerTemplate struct {
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMetadata `json:"metadata,omitempty"`

	// Specification of the desired behavior of the pod.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	// +optional
	Spec corev1.PodSpec `json:"spec,omitempty"`
}

// SuperGraphSchemaStatus defines the observed state of SuperGraphSchema
type SuperGraphSchemaStatus struct {
	// Conditions holds the conditions for the SuperGraphSchema.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	Reconciler corev1.LocalObjectReference `json:"reconciler,omitempty"`

	ComposeErrors []ComposeError `json:"composeErrors,omitempty"`

	// ObservedSHA256Checksum is a checkum across all subgraph resources discovered by this resource
	ObservedSHA256Checksum string `json:"observedSHA256Checksum,omitempty"`

	// ConfigMap reference
	ConfigMap corev1.LocalObjectReference `json:"configMap,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SubResourceCatalog holds discovered references to all sub resources including SwaggerDefinition and SwaggerUnification associated with this hub
	SubResourceCatalog []ResourceReference `json:"subResourceCatalog,omitempty"`
}

type ComposeError struct {
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
	Type    string `json:"type,omitempty"`
}

// ResourceReference metadata to lookup another resource
type ResourceReference struct {
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

func SuperGraphSchemaReconciling(schema SuperGraphSchema, status metav1.ConditionStatus, reason, message string) SuperGraphSchema {
	setResourceCondition(&schema, ConditionReconciling, status, reason, message, schema.Generation)
	return schema
}

func SuperGraphSchemaReady(schema SuperGraphSchema, status metav1.ConditionStatus, reason, message string) SuperGraphSchema {
	setResourceCondition(&schema, ConditionReady, status, reason, message, schema.Generation)
	return schema
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SuperGraphSchema) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *SuperGraphSchema) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

func (in *SuperGraphSchema) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
