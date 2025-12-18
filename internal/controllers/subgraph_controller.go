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
	"crypto/sha256"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/apollo-controller/api/v1beta1"
)

// SubGraph reconciles a SubGraph object
type SubGraphReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

type SubGraphReconcilerOptions struct {
	MaxConcurrentReconciles int
}

// SetupWithManager adding controllers
func (r *SubGraphReconciler) SetupWithManager(mgr ctrl.Manager, opts SubGraphReconcilerOptions) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1beta1.SubGraph{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
		)).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &infrav1beta1.SubGraph{}, handler.OnlyControllerOwner()),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

// Reconcile SubGraphs
func (r *SubGraphReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "name", req.NamespacedName)
	logger.Info("reconciling SubGraph")

	// Fetch the SubGraph instance
	subgraph := infrav1beta1.SubGraph{}

	err := r.Get(ctx, req.NamespacedName, &subgraph)
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

	if subgraph.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	subgraph, result, err := r.reconcile(ctx, subgraph)
	subgraph.Status.ObservedGeneration = subgraph.GetGeneration()

	if err != nil {
		logger.Error(err, "reconcile error occurred")
		subgraph = infrav1beta1.SubGraphReady(subgraph, metav1.ConditionFalse, "ReconciliationFailed", err.Error())
		r.Recorder.Event(&subgraph, "Normal", "error", err.Error())
	}

	// Update status after reconciliation.
	if err := r.patchStatus(ctx, &subgraph); err != nil {
		logger.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{Requeue: true}, err
	}

	return result, err
}

func (r *SubGraphReconciler) reconcile(ctx context.Context, subgraph infrav1beta1.SubGraph) (infrav1beta1.SubGraph, ctrl.Result, error) {
	if subgraph.Spec.Schema == nil {
		return subgraph, ctrl.Result{}, fmt.Errorf("no schema defined")
	}

	checksumSha := sha256.New()
	checksumSha.Write([]byte(subgraph.Spec.Schema.SDL))
	checksum := fmt.Sprintf("%x", checksumSha.Sum(nil))
	subgraph.Status.SHA256Checksum = checksum

	subgraph = infrav1beta1.SubGraphReady(subgraph, metav1.ConditionTrue, "ReconciliationSuccessful", "schema available")
	return subgraph, ctrl.Result{}, nil
}

func (r *SubGraphReconciler) patchStatus(ctx context.Context, subgraph *infrav1beta1.SubGraph) error {
	key := client.ObjectKeyFromObject(subgraph)
	latest := &infrav1beta1.SubGraph{}
	if err := r.Get(ctx, key, latest); err != nil {
		return err
	}

	return r.Status().Patch(ctx, subgraph, client.MergeFrom(latest))
}
