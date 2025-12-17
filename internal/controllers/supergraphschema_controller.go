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
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/apollo-controller/api/v1beta1"
	"github.com/DoodleScheduling/apollo-controller/internal/merge"
	"github.com/fluxcd/pkg/runtime/conditions"
)

// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=supergraphschemas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=supergraphschemas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=subgraphs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;watch;list
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;update;patch;delete;watch;list
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SuperGraphSchema reconciles a SuperGraphSchema object
type SuperGraphSchemaReconciler struct {
	client.Client
	Log               logr.Logger
	Scheme            *runtime.Scheme
	Recorder          record.EventRecorder
	DefaultRoverImage string
	DefaultHTTPDImage string
	HTTPGetter        httpGetter
}

type httpGetter interface {
	Get(url string) (*http.Response, error)
}

type SuperGraphSchemaReconcilerOptions struct {
	MaxConcurrentReconciles int
}

// SetupWithManager adding controllers
func (r *SuperGraphSchemaReconciler) SetupWithManager(mgr ctrl.Manager, opts SuperGraphSchemaReconcilerOptions) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.SuperGraphSchema{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(
			&infrav1beta1.SubGraph{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForChangeBySubGraphSelector),
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SuperGraphSchema{}, handler.OnlyControllerOwner()),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SuperGraphSchema{}, handler.OnlyControllerOwner()),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

func (r *SuperGraphSchemaReconciler) requestsForChangeBySubGraphSelector(ctx context.Context, o client.Object) []reconcile.Request {
	var list infrav1beta1.SuperGraphSchemaList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for _, schema := range list.Items {
		var namespaces corev1.NamespaceList
		if schema.Spec.NamespaceSelector == nil {
			namespaces.Items = append(namespaces.Items, corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: schema.Namespace,
				},
			})
		} else {
			namespaceSelector, err := metav1.LabelSelectorAsSelector(schema.Spec.NamespaceSelector)
			if err != nil {
				return nil
			}

			err = r.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: namespaceSelector})
			if err != nil {
				return nil
			}
		}

		var hasReferencedSuperGraphSchemaNamespace bool
		for _, ns := range namespaces.Items {
			if ns.Name == o.GetNamespace() {
				hasReferencedSuperGraphSchemaNamespace = true
				break
			}
		}

		if !hasReferencedSuperGraphSchemaNamespace {
			continue
		}

		labelSel, err := metav1.LabelSelectorAsSelector(schema.Spec.SubGraphSelector)
		if err != nil {
			r.Log.Error(err, "can not select subgraph selectors")
			continue
		}

		if labelSel.Matches(labels.Set(o.GetLabels())) {
			r.Log.V(1).Info("referenced resource from a SuperGraphSchema changed detected", "namespace", schema.GetNamespace(), "name", schema.GetName())
			reqs = append(reqs, reconcile.Request{NamespacedName: objectKey(&schema)})
		}
	}

	return reqs
}

// Reconcile SuperGraphSchemas
func (r *SuperGraphSchemaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling SuperGraphSchema")

	// Fetch the SuperGraphSchema instance
	schema := infrav1beta1.SuperGraphSchema{}

	err := r.Get(ctx, req.NamespacedName, &schema)
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

	if schema.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	schema, result, err := r.reconcile(ctx, schema, logger)
	schema.Status.ObservedGeneration = schema.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		schema = infrav1beta1.SuperGraphSchemaReady(schema, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Event(&schema, "Normal", "error", err.Error())
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &schema); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{}, err
	}

	return result, err
}

type superGraphConfig struct {
	FederationVersion int `yaml:"federation_version"`
	SubGraphs         map[string]subGraphConfig
}

type subGraphConfig struct {
	RoutingURL string         `yaml:"routing_url"`
	Schema     subGraphSchema `yaml:"schema"`
}

type subGraphSchema struct {
	File string `yaml:"file"`
}

func (r *SuperGraphSchemaReconciler) reconcile(ctx context.Context, schema infrav1beta1.SuperGraphSchema, logger logr.Logger) (infrav1beta1.SuperGraphSchema, ctrl.Result, error) {
	schema.Status.SubResourceCatalog = []infrav1beta1.ResourceReference{}
	schema, subgraphs, err := r.extendSuperWithSubGraphs(ctx, schema, logger)
	if err != nil {
		return schema, ctrl.Result{}, err
	}

	checksum := r.subGraphCheckum(subgraphs)
	logger.V(1).Info("schema checksum", "checksum", checksum)

	supergraphConfig := r.createSuperGraphConfig(subgraphs)
	pod := &corev1.Pod{}
	configmap := &corev1.ConfigMap{}

	var configmapErr error
	var podErr error
	var needUpdate bool

	cleanup := func() error {
		if podErr == nil {
			if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("could not delete reconciler pod: %w", err)
			}
		}

		return nil
	}

	// check for stale reconciler
	if schema.Status.Reconciler.Name != "" {
		configmapErr = r.Get(ctx, client.ObjectKey{Name: schema.Status.Reconciler.Name, Namespace: schema.Namespace}, configmap)
		podErr = r.Get(ctx, client.ObjectKey{Name: schema.Status.Reconciler.Name, Namespace: schema.Namespace}, pod)
		specVersion, ok := pod.Annotations["apollo-controller/spec-version"]
		if !needUpdate && podErr == nil && ok {
			needUpdate = specVersion != fmt.Sprintf("%d", schema.Generation)
		}

		specChecksum, ok := pod.Annotations["apollo-controller/subgraphs-checksum"]
		if !needUpdate && podErr == nil && ok {
			needUpdate = specChecksum != checksum
		}

		if !ok {
			needUpdate = true
		}

		if configmapErr != nil && !apierrors.IsNotFound(configmapErr) {
			return schema, ctrl.Result{}, configmapErr
		}

		if podErr != nil && !apierrors.IsNotFound(podErr) {
			return schema, ctrl.Result{}, podErr
		}
	}

	// unlink schema reconciler only if the reconciler pod is gone
	if apierrors.IsNotFound(podErr) {
		conditions.Delete(&schema, infrav1beta1.ConditionReconciling)
		schema.Status.Reconciler.Name = ""
		return schema, ctrl.Result{Requeue: true}, nil
	}

	// cleanup reconciler pod if stale
	if needUpdate {
		logger.V(1).Info("schema checksum changed, delete stale reconciler", "pod-name", schema.Status.Reconciler)
		return schema, ctrl.Result{Requeue: true}, cleanup()
	}

	if schema.Status.ConfigMap.Name != "" {
		configmap := &corev1.ConfigMap{}
		configmapErr = r.Get(ctx, client.ObjectKey{Name: schema.Status.ConfigMap.Name, Namespace: schema.Namespace}, configmap)

		if configmapErr != nil && !apierrors.IsNotFound(configmapErr) {
			return schema, ctrl.Result{}, configmapErr
		}

		if configmapErr == nil && schema.Status.ObservedSHA256Checksum == checksum {
			if schema.Spec.Interval != nil {
				logger.V(1).Info("skip reconciliation due checksum match, requeue ", "after", schema.Spec.Interval.Duration)

				return schema, ctrl.Result{
					RequeueAfter: schema.Spec.Interval.Duration,
				}, nil
			} else {
				logger.V(1).Info("skip reconciliation due checksum match ")
				return schema, ctrl.Result{}, nil
			}
		}
	}

	// handle reconciler pod state
	if podErr == nil && pod.Name != "" {
		return r.handlerReconcilerState(ctx, schema, pod, checksum, logger)
	}

	return r.createReconciler(ctx, schema, supergraphConfig, subgraphs, checksum, logger)
}

func (r *SuperGraphSchemaReconciler) subGraphCheckum(subgraphs []infrav1beta1.SubGraph) string {
	checksumSha := sha256.New()
	for _, subgraph := range subgraphs {
		checksumSha.Write([]byte(subgraph.Spec.Endpoint))
		checksumSha.Write([]byte(subgraph.Status.SHA256Checksum))
	}

	return fmt.Sprintf("%x", checksumSha.Sum(nil))
}

func (r *SuperGraphSchemaReconciler) createSuperGraphConfig(subgraphs []infrav1beta1.SubGraph) superGraphConfig {
	config := superGraphConfig{
		FederationVersion: 2,
		SubGraphs:         make(map[string]subGraphConfig, len(subgraphs)),
	}

	for _, subgraph := range subgraphs {
		config.SubGraphs[subgraph.Name] = subGraphConfig{
			RoutingURL: subgraph.Spec.Endpoint,
			Schema: subGraphSchema{
				File: fmt.Sprintf("/schemas/%s.%s.graphql", subgraph.Namespace, subgraph.Name),
			},
		}
	}

	return config
}

func (r *SuperGraphSchemaReconciler) handlerReconcilerState(ctx context.Context, schema infrav1beta1.SuperGraphSchema, pod *corev1.Pod, checksum string, logger logr.Logger) (infrav1beta1.SuperGraphSchema, ctrl.Result, error) {
	var containerStatus *corev1.ContainerStatus
	for _, container := range pod.Status.ContainerStatuses {
		if container.Name == "rover" {
			containerStatus = &container
			break
		}
	}

	switch {
	case containerStatus == nil:
		return schema, ctrl.Result{}, nil
	case containerStatus.State.Terminated != nil && containerStatus.State.Terminated.ExitCode == 0:
		logger.Info("reconciler pod succeeded", "pod-name", schema.Status.Reconciler)

		schema, res, err := r.updateSchemaStatus(ctx, schema, pod, logger)

		if err != nil {
			// Don't delete the pod on transient errors (e.g., composition file not found)
			// The pod will be cleaned up on the next successful reconcile or when it becomes stale
			// This prevents immediate reconcile loops that bypass backoff logic
			logger.V(1).Info("error updating schema status, keeping pod for retry", "error", err)
			return schema, res, err
		}

		// Only cleanup pod on successful status update
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return schema, res, fmt.Errorf("could not delete reconciler pod: %w", err)
		}

		schema.Status.ObservedSHA256Checksum = checksum
		schema = infrav1beta1.SuperGraphSchemaReady(schema, metav1.ConditionTrue, "ReconciliationSucceeded", fmt.Sprintf("reconciler %s terminated with code 0", schema.Status.Reconciler.Name))
		msg := "schema successfully composed"
		r.Recorder.Event(&schema, "Normal", "info", msg)

		return schema, res, err
	case containerStatus.State.Terminated != nil:
		err := fmt.Errorf("reconciler terminated with code %d", containerStatus.State.Terminated.ExitCode)
		schema = infrav1beta1.SuperGraphSchemaReady(schema, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		return schema, ctrl.Result{}, err
	}

	return schema, ctrl.Result{}, nil
}

func (r *SuperGraphSchemaReconciler) updateSchemaStatus(ctx context.Context, schema infrav1beta1.SuperGraphSchema, pod *corev1.Pod, logger logr.Logger) (infrav1beta1.SuperGraphSchema, ctrl.Result, error) {
	scrapeURL := fmt.Sprintf("http://%s:29000/schema.graphql", pod.Status.PodIP)
	logger.Info("fetch composed config from url", "url", scrapeURL)

	resp, err := http.Get(scrapeURL)
	if err != nil {
		return schema, ctrl.Result{}, fmt.Errorf("request failed: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	logger.Info("scrape result", "url", scrapeURL, "http-code", resp.StatusCode, "content-length", resp.ContentLength)
	if resp.StatusCode != http.StatusOK {
		return schema, ctrl.Result{}, fmt.Errorf("bad status: %s", resp.Status)
	}

	var b []byte
	buf := bytes.NewBuffer(b)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		return schema, ctrl.Result{}, fmt.Errorf("write failed: %w", err)
	}

	controllerOwner := true
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("supergraph-schema-%s", schema.Name),
			Namespace: schema.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       schema.Name,
					APIVersion: schema.APIVersion,
					Kind:       schema.Kind,
					UID:        schema.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Data: map[string]string{
			"schema.graphql": buf.String(),
		},
	}

	var existingSpec corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{
		Namespace: cm.Namespace,
		Name:      cm.Name,
	}, &existingSpec)

	if err != nil && !apierrors.IsNotFound(err) {
		return schema, ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, cm); err != nil {
			return schema, ctrl.Result{}, err
		}
	} else {
		if err := r.Update(ctx, cm); err != nil {
			return schema, ctrl.Result{}, err
		}
	}

	schema.Status.ConfigMap.Name = cm.Name
	return schema, ctrl.Result{Requeue: true}, nil
}

func (r *SuperGraphSchemaReconciler) createReconciler(ctx context.Context, schema infrav1beta1.SuperGraphSchema, config superGraphConfig, subgraphs []infrav1beta1.SubGraph, checksum string, logger logr.Logger) (infrav1beta1.SuperGraphSchema, ctrl.Result, error) {
	r.Recorder.Event(&schema, "Normal", "info", "reconcile schema progressing")
	schema = infrav1beta1.SuperGraphSchemaReady(schema, metav1.ConditionUnknown, "Progressing", "Reconciliation in progress")
	schema = infrav1beta1.SuperGraphSchemaReconciling(schema, metav1.ConditionTrue, "Progressing", "")

	composeConfig, err := yaml.Marshal(config)
	if err != nil {
		return schema, ctrl.Result{}, fmt.Errorf("failed to marshal config: %w", err)
	}

	controllerOwner := true
	template := &corev1.Pod{}

	if schema.Spec.ReconcilerTemplate != nil {
		template.Labels = schema.Spec.ReconcilerTemplate.Labels
		template.Annotations = schema.Spec.ReconcilerTemplate.Annotations
		schema.Spec.ReconcilerTemplate.Spec.DeepCopyInto(&template.Spec)
	}

	template.Name = fmt.Sprintf("rover-%s", rand.String(8))
	template.Namespace = schema.Namespace
	template.OwnerReferences = []metav1.OwnerReference{
		{
			Name:       schema.Name,
			APIVersion: schema.APIVersion,
			Kind:       schema.Kind,
			UID:        schema.UID,
			Controller: &controllerOwner,
		},
	}

	template.ResourceVersion = ""
	template.UID = ""

	if template.Labels == nil {
		template.Labels = make(map[string]string)
	}

	template.Labels["app.kubernetes.io/instance"] = "rover"
	template.Labels["app.kubernetes.io/name"] = "rover"
	template.Labels["apollo-controller/schema"] = schema.Name

	if template.Annotations == nil {
		template.Annotations = make(map[string]string)
	}

	template.Annotations["apollo-controller/spec-version"] = fmt.Sprintf("%d", schema.Generation)
	template.Annotations["apollo-controller/subgraphs-checksum"] = checksum

	containers := []corev1.Container{
		{
			Name: "rover",
			Command: []string{
				"/bin/sh",
				"-c",
				"rover supergraph compose --config /supergraph/supergraph.yaml --skip-update-check > /output/schema.graphql",
			},
			Image: r.DefaultRoverImage,
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "supergraph-config",
					MountPath: "/supergraph",
					ReadOnly:  true,
				},
				{
					Name:      "rover-config",
					MountPath: "/.config",
				},
				{
					Name:      "output",
					MountPath: "/output",
				},
			},
		},
		{
			Name: "httpd",
			Command: []string{
				"httpd",
				"-f",
				"-v",
				"-p",
				"29000",
				"-h",
				"/output",
			},
			Image: r.DefaultHTTPDImage,
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "output",
					MountPath: "/output",
				},
			},
		},
	}

	containers, err = merge.MergePatchContainers(containers, template.Spec.Containers)
	if err != nil {
		return schema, ctrl.Result{}, err
	}

	template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
	template.Spec.Containers = containers
	template.Spec.Volumes = append(template.Spec.Volumes,
		corev1.Volume{
			Name: "supergraph-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: template.Name,
					},
				},
			},
		},
		corev1.Volume{
			Name: "rover-config",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		corev1.Volume{
			Name: "output",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)

	for _, subgraph := range subgraphs {
		template.Spec.Volumes = append(template.Spec.Volumes, corev1.Volume{
			Name: fmt.Sprintf("subgraph-%s-%s", subgraph.Namespace, subgraph.Name),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: subgraph.Status.ConfigMap.Name,
					},
				},
			},
		})

		template.Spec.Containers[0].VolumeMounts = append(template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      fmt.Sprintf("subgraph-%s-%s", subgraph.Namespace, subgraph.Name),
			MountPath: fmt.Sprintf("/schemas/%s.%s.graphql", subgraph.Namespace, subgraph.Name),
			SubPath:   "schema.graphql",
			ReadOnly:  true,
		})
	}

	// If the status update fails the creation of the reconciler pod is postponed to the next reconciliation run
	schema.Status.Reconciler = corev1.LocalObjectReference{
		Name: template.Name,
	}

	if err := r.patchStatus(ctx, &schema); err != nil {
		return schema, ctrl.Result{}, err
	}

	logger.Info("create new reconciler pod", "pod", template.Name, "previous", schema.Status.Reconciler.Name)
	if err := r.Create(ctx, template); err != nil {
		return schema, ctrl.Result{}, err
	}

	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: template.Name,
			Labels: map[string]string{
				"app.kubernetes.io/instance": "rover",
				"app.kubernetes.io/name":     "rover",
				"apollo-controller/schema":   schema.Name,
			},
			Namespace: schema.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       template.Name,
					APIVersion: "v1",
					Kind:       "Pod",
					UID:        template.UID,
					Controller: &controllerOwner,
				},
			},
		},
		Data: map[string]string{
			"supergraph.yaml": string(composeConfig),
		},
	}

	logger.Info("creating new supergraph composer configmap", "configmap", configmap.Name)

	if err := r.Create(ctx, configmap); err != nil {
		return schema, ctrl.Result{}, err
	}

	if schema.Spec.Timeout != nil {
		return schema, ctrl.Result{
			RequeueAfter: schema.Spec.Timeout.Duration,
		}, nil
	}

	return schema, ctrl.Result{}, err
}

func (r *SuperGraphSchemaReconciler) extendSuperWithSubGraphs(ctx context.Context, schema infrav1beta1.SuperGraphSchema, logger logr.Logger) (infrav1beta1.SuperGraphSchema, []infrav1beta1.SubGraph, error) {
	var subgraphs infrav1beta1.SubGraphList
	subgraphselector, err := metav1.LabelSelectorAsSelector(schema.Spec.SubGraphSelector)
	if err != nil {
		return schema, nil, err
	}

	var namespaces corev1.NamespaceList
	if schema.Spec.NamespaceSelector == nil {
		namespaces.Items = append(namespaces.Items, corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: schema.Namespace,
			},
		})
	} else {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(schema.Spec.NamespaceSelector)
		if err != nil {
			return schema, nil, err
		}

		err = r.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: namespaceSelector})
		if err != nil {
			return schema, nil, err
		}
	}

	for _, namespace := range namespaces.Items {
		var namespacedSubGraph infrav1beta1.SubGraphList
		err = r.List(ctx, &namespacedSubGraph, client.InNamespace(namespace.Name), client.MatchingLabelsSelector{Selector: subgraphselector})
		if err != nil {
			return schema, nil, err
		}

		subgraphs.Items = append(subgraphs.Items, namespacedSubGraph.Items...)
	}

	slices.SortFunc(subgraphs.Items, func(a, b infrav1beta1.SubGraph) int {
		return cmp.Or(
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Namespace, b.Namespace),
		)
	})

	var ready []infrav1beta1.SubGraph

	for _, subgraph := range subgraphs.Items {
		cm := &corev1.ConfigMap{}
		err = r.Get(ctx, client.ObjectKey{Name: subgraph.Status.ConfigMap.Name, Namespace: subgraph.Namespace}, cm)
		if err != nil {
			logger.Info("subgraph configmap not found, ignoring", "configmap", subgraph.Status.ConfigMap.Name, "namespace", subgraph.Namespace, "error", err)
			continue
		}

		ready = append(ready, subgraph)

		ref := infrav1beta1.ResourceReference{
			Kind:       subgraph.Kind,
			Name:       subgraph.Name,
			APIVersion: subgraph.APIVersion,
		}

		if subgraph.Namespace != schema.Namespace {
			ref.Namespace = subgraph.Namespace
		}

		schema.Status.SubResourceCatalog = append(schema.Status.SubResourceCatalog, ref)
	}

	return schema, ready, nil
}

func (r *SuperGraphSchemaReconciler) patchStatus(ctx context.Context, schema *infrav1beta1.SuperGraphSchema) error {
	key := client.ObjectKeyFromObject(schema)
	latest := &infrav1beta1.SuperGraphSchema{}
	if err := r.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Status().Patch(ctx, schema, client.MergeFrom(latest))
}
