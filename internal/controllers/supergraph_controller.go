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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	infrav1beta1 "github.com/DoodleScheduling/apollo-controller/api/v1beta1"
	"github.com/DoodleScheduling/apollo-controller/internal/merge"
)

// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=supergraphs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=supergraphs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=supergraphschemas,verbs=get;list;watch
// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=supergraphschemas/status,verbs=get
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;watch;list
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=services,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SuperGraph reconciles a SuperGraph object
type SuperGraphReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

type SuperGraphReconcilerOptions struct {
	MaxConcurrentReconciles int
}

const schemaIndexKey = ".metadata.schema"

// SetupWithManager adding controllers
func (r *SuperGraphReconciler) SetupWithManager(mgr ctrl.Manager, opts SuperGraphReconcilerOptions) error {
	if err := mgr.GetFieldIndexer().IndexField(context.TODO(), &infrav1beta1.SuperGraph{}, schemaIndexKey,
		func(o client.Object) []string {
			supergraph := o.(*infrav1beta1.SuperGraph)
			return []string{
				fmt.Sprintf("%s/%s", supergraph.GetNamespace(), supergraph.Spec.Schema.Name),
			}
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.SuperGraph{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(
			&infrav1beta1.SuperGraphSchema{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForChangeBySchema),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SuperGraph{}, handler.OnlyControllerOwner()),
		).
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SuperGraph{}, handler.OnlyControllerOwner()),
		).
		Watches(
			&corev1.Service{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SuperGraph{}, handler.OnlyControllerOwner()),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

func (r *SuperGraphReconciler) requestsForChangeBySchema(ctx context.Context, o client.Object) []reconcile.Request {
	schema, ok := o.(*infrav1beta1.SuperGraphSchema)
	if !ok {
		panic(fmt.Sprintf("expected a Secret, got %T", o))
	}

	var list infrav1beta1.SuperGraphList
	if err := r.List(ctx, &list, client.MatchingFields{
		schemaIndexKey: objectKey(schema).String(),
	}); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for _, i := range list.Items {
		r.Log.Info("referenced schema from a supergraph change detected", "namespace", i.GetNamespace(), "name", i.GetName())
		reqs = append(reqs, reconcile.Request{NamespacedName: objectKey(&i)})
	}

	return reqs
}

// Reconcile SuperGraphs
func (r *SuperGraphReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling SuperGraph")

	// Fetch the SuperGraph instance
	supergraph := infrav1beta1.SuperGraph{}

	err := r.Get(ctx, req.NamespacedName, &supergraph)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if supergraph.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	supergraph, result, err := r.reconcile(ctx, supergraph)
	supergraph.Status.ObservedGeneration = supergraph.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		supergraph = infrav1beta1.SuperGraphReady(supergraph, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Event(&supergraph, "Normal", "error", err.Error())
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &supergraph); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{}, err
	}

	return result, err
}

func isOwner(owner, owned metav1.Object) bool {
	runtimeObj, ok := (owner).(runtime.Object)
	if !ok {
		return false
	}
	for _, ownerRef := range owned.GetOwnerReferences() {
		if ownerRef.Name == owner.GetName() && ownerRef.UID == owner.GetUID() && ownerRef.Kind == runtimeObj.GetObjectKind().GroupVersionKind().Kind {
			return true
		}
	}
	return false
}

func (r *SuperGraphReconciler) reconcile(ctx context.Context, supergraph infrav1beta1.SuperGraph) (infrav1beta1.SuperGraph, ctrl.Result, error) {
	var graphschema infrav1beta1.SuperGraphSchema
	err := r.Get(ctx, client.ObjectKey{
		Namespace: supergraph.Namespace,
		Name:      supergraph.Spec.Schema.Name,
	}, &graphschema)

	if err != nil && !apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, fmt.Errorf("schema %s not found", supergraph.Spec.Schema.Name)
	}

	if graphschema.Status.ConfigMap.Name == "" {
		return supergraph, ctrl.Result{}, fmt.Errorf("supergraphschema is not ready")
	}

	var schema corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{
		Namespace: graphschema.Namespace,
		Name:      graphschema.Status.ConfigMap.Name,
	}, &schema)

	if err != nil && !apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, fmt.Errorf("schema configmap %s not found", graphschema.Status.ConfigMap.Name)
	}

	supergraph, result, err := r.reconcileConfig(ctx, supergraph)
	if err != nil {
		return supergraph, result, err
	}

	var (
		gid          int64 = 10000
		uid          int64 = 10000
		runAsNonRoot       = true
		replicas     int32 = 1
	)

	controllerOwner := true
	template := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("apollo-router-%s", supergraph.Name),
			Namespace: supergraph.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       supergraph.Name,
					APIVersion: supergraph.APIVersion,
					Kind:       supergraph.Kind,
					UID:        supergraph.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:    &uid,
						RunAsGroup:   &gid,
						RunAsNonRoot: &runAsNonRoot,
					},
				},
			},
		},
	}

	if supergraph.Spec.DeploymentTemplate != nil {
		template.Labels = supergraph.Spec.DeploymentTemplate.Labels
		template.Annotations = supergraph.Spec.DeploymentTemplate.Annotations
		supergraph.Spec.DeploymentTemplate.Spec.Template.DeepCopyInto(&template.Spec.Template)
		template.Spec.MinReadySeconds = supergraph.Spec.DeploymentTemplate.Spec.MinReadySeconds
		template.Spec.Paused = supergraph.Spec.DeploymentTemplate.Spec.Paused
		template.Spec.ProgressDeadlineSeconds = supergraph.Spec.DeploymentTemplate.Spec.ProgressDeadlineSeconds
		template.Spec.Replicas = supergraph.Spec.DeploymentTemplate.Spec.Replicas
		template.Spec.RevisionHistoryLimit = supergraph.Spec.DeploymentTemplate.Spec.RevisionHistoryLimit
		template.Spec.Strategy = supergraph.Spec.DeploymentTemplate.Spec.Strategy
	}

	if template.Labels == nil {
		template.Labels = make(map[string]string)
	}

	if template.Spec.Template.Labels == nil {
		template.Spec.Template.Labels = make(map[string]string)
	}

	template.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app.kubernetes.io/instance":   "apollo-router",
			"app.kubernetes.io/name":       "apollo-router",
			"apollo-controller/supergraph": supergraph.Name,
		},
	}

	if template.Spec.Replicas == nil {
		template.Spec.Replicas = &replicas
	}

	template.Spec.Template.Labels["app.kubernetes.io/instance"] = "apollo-router"
	template.Spec.Template.Labels["app.kubernetes.io/name"] = "apollo-router"
	template.Spec.Template.Labels["apollo-controller/supergraph"] = supergraph.Name
	template.Labels["app.kubernetes.io/instance"] = "apollo-router"
	template.Labels["app.kubernetes.io/name"] = "apollo-router"
	template.Labels["apollo-controller/supergraph"] = supergraph.Name

	if template.Annotations == nil {
		template.Annotations = make(map[string]string)
	}

	template.Spec.Template.Spec.Volumes = append(template.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: "schema",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: graphschema.Status.ConfigMap.Name,
				},
			},
		},
	}, corev1.Volume{
		Name: "config",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: supergraph.Status.ConfigMap.Name,
				},
			},
		},
	})

	containers := []corev1.Container{
		{
			Name:  "router",
			Image: "ghcr.io/apollographql/router:v2.9.0",
			Args: []string{
				"--config",
				"/config/router.yaml",
				"--supergraph",
				"/schema/schema.graphql",
				"--listen",
				"0.0.0.0:4000",
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.IntOrString{StrVal: "health", Type: intstr.String},
						Path: "/health?live",
					},
				},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.IntOrString{StrVal: "health", Type: intstr.String},
						Path: "/health?ready",
					},
				},
			},
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 4000,
				},
				{
					Name:          "health",
					ContainerPort: 8088,
				},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "schema",
					MountPath: "/schema",
				},
				{
					Name:      "config",
					MountPath: "/config",
				},
			},
		},
	}

	containers, err = merge.MergePatchContainers(containers, template.Spec.Template.Spec.Containers)
	if err != nil {
		return supergraph, ctrl.Result{}, err
	}

	template.Spec.Template.Spec.Containers = containers
	r.Log.Info("create apollo-router deployment", "deployment-name", template.Name)

	svcTemplate := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("apollo-router-%s", supergraph.Name),
			Namespace: supergraph.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       supergraph.Name,
					APIVersion: supergraph.APIVersion,
					Kind:       supergraph.Kind,
					UID:        supergraph.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.IntOrString{StrVal: "http", Type: intstr.String},
				},
			},
			Selector: map[string]string{
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
				"apollo-controller/supergraph": supergraph.Name,
			},
		},
	}

	var svc corev1.Service
	err = r.Get(ctx, client.ObjectKey{
		Namespace: svcTemplate.Namespace,
		Name:      svcTemplate.Name,
	}, &svc)

	if err != nil && !apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, svcTemplate); err != nil {
			return supergraph, ctrl.Result{}, err
		}
	} else {
		if !isOwner(&supergraph, &svc) {
			return supergraph, ctrl.Result{}, fmt.Errorf("can not take ownership of existing service: %s", svc.Name)
		}

		if err := r.Update(ctx, svcTemplate); err != nil {
			return supergraph, ctrl.Result{}, err
		}
	}

	var deployment appsv1.Deployment
	err = r.Get(ctx, client.ObjectKey{
		Namespace: template.Namespace,
		Name:      template.Name,
	}, &deployment)

	if err != nil && !apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, template); err != nil {
			return supergraph, ctrl.Result{}, err
		}
	} else {
		if !isOwner(&supergraph, &deployment) {
			return supergraph, ctrl.Result{}, fmt.Errorf("can not take ownership of existing deployment: %s", deployment.Name)
		}

		if err := r.Update(ctx, template); err != nil {
			return supergraph, ctrl.Result{}, err
		}
	}

	supergraph = infrav1beta1.SuperGraphReady(supergraph, metav1.ConditionTrue, "ReconciliationSuccessful", fmt.Sprintf("deployment/%s created", template.Name))
	return supergraph, ctrl.Result{}, nil
}

func (r *SuperGraphReconciler) reconcileConfig(ctx context.Context, supergraph infrav1beta1.SuperGraph) (infrav1beta1.SuperGraph, ctrl.Result, error) {
	controllerOwner := true
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("supergraph-config-%s", supergraph.Name),
			Namespace: supergraph.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       supergraph.Name,
					APIVersion: supergraph.APIVersion,
					Kind:       supergraph.Kind,
					UID:        supergraph.UID,
					Controller: &controllerOwner,
				},
			},
		},
	}

	routerConfig := make(map[string]interface{})
	if len(supergraph.Spec.RouterConfig.Raw) > 0 {
		if err := json.Unmarshal(supergraph.Spec.RouterConfig.Raw, &routerConfig); err != nil {
			return supergraph, ctrl.Result{}, fmt.Errorf("failed to deserialize router config: %w", err)
		}
	}

	if healthCheckVal, ok := routerConfig["health_check"].(map[string]interface{}); ok {
		if _, exists := healthCheckVal["listen"]; !exists {
			healthCheckVal["listen"] = "0.0.0.0:8088"
		}
	} else {
		routerConfig["health_check"] = map[string]interface{}{
			"listen": "0.0.0.0:8088",
		}
	}

	routerConfigYAML, err := yaml.Marshal(routerConfig)
	if err != nil {
		return supergraph, ctrl.Result{}, fmt.Errorf("failed to serialize router config to YAML: %w", err)
	}

	cm.BinaryData = make(map[string][]byte)
	cm.BinaryData["router.yaml"] = routerConfigYAML

	var existingSpec corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{
		Namespace: cm.Namespace,
		Name:      cm.Name,
	}, &existingSpec)

	if err != nil && !apierrors.IsNotFound(err) {
		return supergraph, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, cm); err != nil {
			return supergraph, ctrl.Result{}, err
		}
	} else {
		if err := r.Update(ctx, cm); err != nil {
			return supergraph, ctrl.Result{}, err
		}
	}

	supergraph.Status.ConfigMap = corev1.LocalObjectReference{
		Name: cm.Name,
	}

	return supergraph, ctrl.Result{}, nil
}

func (r *SuperGraphReconciler) patchStatus(ctx context.Context, supergraph *infrav1beta1.SuperGraph) error {
	key := client.ObjectKeyFromObject(supergraph)
	latest := &infrav1beta1.SuperGraph{}
	if err := r.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Status().Patch(ctx, supergraph, client.MergeFrom(latest))
}

// objectKey returns client.ObjectKey for the object.
func objectKey(object metav1.Object) client.ObjectKey {
	return client.ObjectKey{
		Namespace: object.GetNamespace(),
		Name:      object.GetName(),
	}
}
