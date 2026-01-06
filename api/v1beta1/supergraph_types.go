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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.deploymentTemplate.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""

// SuperGraph is the Schema for the SuperGraphs API
type SuperGraph struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SuperGraphSpec   `json:"spec,omitempty"`
	Status SuperGraphStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SuperGraphList contains a list of SuperGraph
type SuperGraphList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SuperGraph `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SuperGraph{}, &SuperGraphList{})
}

// SuperGraphSpec defines the desired state of SuperGraph
type SuperGraphSpec struct {
	DeploymentTemplate *DeploymentTemplate `json:"deploymentTemplate,omitempty"`

	// Suspend reconciliation
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	RouterConfig runtime.RawExtension `json:"routerConfig,omitempty"`

	// Schema
	// +kubebuilder:validation:Required
	Schema corev1.LocalObjectReference `json:"schema"`
}

type DeploymentTemplate struct {
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMetadata `json:"metadata,omitempty"`

	// Specification of the desired behavior of the pod.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	// +optional
	Spec DeploymentSpec `json:"spec,omitempty"`
}

type DeploymentSpec struct {
	// Number of desired pods. This is a pointer to distinguish between explicit
	// zero and not specified. Defaults to 1.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Template describes the pods that will be created.
	// The only allowed template.spec.restartPolicy value is "Always".
	Template corev1.PodTemplateSpec `json:"template"`

	// The deployment strategy to use to replace existing pods with new ones.
	// +optional
	// +patchStrategy=retainKeys
	Strategy appsv1.DeploymentStrategy `json:"strategy,omitempty" patchStrategy:"retainKeys"`

	// Minimum number of seconds for which a newly created pod should be ready
	// without any of its container crashing, for it to be considered available.
	// Defaults to 0 (pod will be considered available as soon as it is ready)
	// +optional
	MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

	// The number of old ReplicaSets to retain to allow rollback.
	// This is a pointer to distinguish between explicit zero and not specified.
	// Defaults to 10.
	// +optional
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`

	// Indicates that the deployment is paused.
	// +optional
	Paused bool `json:"paused,omitempty"`

	// The maximum time in seconds for a deployment to make progress before it
	// is considered to be failed. The deployment controller will continue to
	// process failed deployments and a condition with a ProgressDeadlineExceeded
	// reason will be surfaced in the deployment status. Note that progress will
	// not be estimated during the time a deployment is paused. Defaults to 600s.
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty"`
}

type ObjectMetadata struct {
	// Map of string keys and values that can be used to organize and categorize
	// (scope and select) objects. May match selectors of replication controllers
	// and services.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations is an unstructured key value map stored with a resource that may be
	// set by external tools to store and retrieve arbitrary metadata. They are not
	// queryable and should be preserved when modifying objects.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// SuperGraphStatus defines the observed state of SuperGraph
type SuperGraphStatus struct {
	// Conditions holds the conditions for the SuperGraph.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ConfigMap reference
	ConfigMap corev1.LocalObjectReference `json:"configMap,omitempty"`

	// ObservedSHA256Checksum is a checksum of the discovered schema
	ObservedSHA256Checksum string `json:"observedSHA256Checksum,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

func SuperGraphReconciling(supergraph SuperGraph, status metav1.ConditionStatus, reason, message string) SuperGraph {
	setResourceCondition(&supergraph, ConditionReconciling, status, reason, message, supergraph.Generation)
	return supergraph
}

func SuperGraphReady(supergraph SuperGraph, status metav1.ConditionStatus, reason, message string) SuperGraph {
	setResourceCondition(&supergraph, ConditionReady, status, reason, message, supergraph.Generation)
	return supergraph
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SuperGraph) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *SuperGraph) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

func (in *SuperGraph) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
