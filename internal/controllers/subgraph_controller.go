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
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1beta1 "github.com/DoodleScheduling/apollo-controller/api/v1beta1"
)

// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=subgraphs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apollo.infra.doodle.com,resources=subgraphs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// SubGraph reconciles a SubGraph object
type SubGraphReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	HTTPClient httpClient
	Recorder   record.EventRecorder
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

	if subgraph.Spec.Timeout != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, subgraph.Spec.Timeout.Duration)
		defer cancel()
	}

	subgraph, result, err := r.reconcile(ctx, subgraph, logger)
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

	if err != nil {
		return result, err
	}

	if subgraph.Spec.Interval != nil {
		return ctrl.Result{RequeueAfter: subgraph.Spec.Interval.Duration}, nil
	}

	return result, err
}

func (r *SubGraphReconciler) reconcile(ctx context.Context, subgraph infrav1beta1.SubGraph, logger logr.Logger) (infrav1beta1.SubGraph, ctrl.Result, error) {
	var schema string
	switch {
	case subgraph.Spec.Schema.SDL != nil:
		schema = *subgraph.Spec.Schema.SDL
	case subgraph.Spec.Schema.HTTP != nil:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, subgraph.Spec.Schema.HTTP.Endpoint, nil)
		if err != nil {
			return subgraph, ctrl.Result{}, fmt.Errorf("failed to build http schema request: %w", err)
		}

		resp, err := r.HTTPClient.Do(req)
		if err != nil {
			return subgraph, ctrl.Result{}, fmt.Errorf("failed to fetch schema from http endpoint: %w", err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		logger.Info("schema fetch result", "url", subgraph.Spec.Schema.HTTP.Endpoint, "http-code", resp.StatusCode, "content-length", resp.ContentLength)
		if resp.StatusCode != http.StatusOK {
			return subgraph, ctrl.Result{}, fmt.Errorf("failed to fetch schema from http endpoint, unexpected status: %s", resp.Status)
		}

		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return subgraph, ctrl.Result{}, fmt.Errorf("failed to read schema body from http request: %w", err)
		}

		schema = string(b)
	default:
		return subgraph, ctrl.Result{}, errors.New("exactly one schema source is required")
	}

	checksumSha := sha256.New()
	checksumSha.Write([]byte(schema))
	checksum := fmt.Sprintf("%x", checksumSha.Sum(nil))
	subgraph.Status.SHA256Checksum = checksum
	subgraph.Status.Schema = schema

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
