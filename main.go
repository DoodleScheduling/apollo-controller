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

package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	infrav1beta1 "github.com/DoodleScheduling/apollo-controller/api/v1beta1"
	"github.com/DoodleScheduling/apollo-controller/internal/controllers"
	"github.com/fluxcd/pkg/runtime/client"
	helper "github.com/fluxcd/pkg/runtime/controller"
	"github.com/fluxcd/pkg/runtime/leaderelection"
	"github.com/fluxcd/pkg/runtime/logger"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	// +kubebuilder:scaffold:imports
)

const controllerName = "apollo-controller"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = corev1.AddToScheme(scheme)
	_ = infrav1beta1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

var (
	metricsAddr             string
	healthAddr              string
	concurrent              int
	defaultSuperGraphImage  string
	defaultHTTPDImage       string
	gracefulShutdownTimeout time.Duration
	clientOptions           client.Options
	kubeConfigOpts          client.KubeConfigOptions
	logOptions              logger.Options
	leaderElectionOptions   leaderelection.Options
	rateLimiterOptions      helper.RateLimiterOptions
	watchOptions            helper.WatchOptions
)

func main() {
	flag.StringVar(&metricsAddr, "metrics-addr", ":9556",
		"The address the metric endpoint binds to.")
	flag.StringVar(&healthAddr, "health-addr", ":9557",
		"The address the health endpoint binds to.")
	flag.IntVar(&concurrent, "concurrent", 4,
		"The number of concurrent SwaggerHub reconciles.")
	flag.DurationVar(&gracefulShutdownTimeout, "graceful-shutdown-timeout", 600*time.Second,
		"The duration given to the reconciler to finish before forcibly stopping.")
	flag.StringVar(&defaultSuperGraphImage, "default-supergraph-image", "ghcr.io/doodlescheduling/supergraph:v0",
		"The default rover cli image.")
	flag.StringVar(&defaultHTTPDImage, "default-httpd-image", "busybox:1",
		"The default image which provides an http server to serve the directory /output. By default httpd (busybox) is used.")

	clientOptions.BindFlags(flag.CommandLine)
	logOptions.BindFlags(flag.CommandLine)
	leaderElectionOptions.BindFlags(flag.CommandLine)
	rateLimiterOptions.BindFlags(flag.CommandLine)
	kubeConfigOpts.BindFlags(flag.CommandLine)
	watchOptions.BindFlags(flag.CommandLine)

	flag.Parse()
	logger.SetLogger(logger.NewLogger(logOptions))

	leaderElectionId := fmt.Sprintf("%s-%s", controllerName, "leader-election")
	if watchOptions.LabelSelector != "" {
		leaderElectionId = leaderelection.GenerateID(leaderElectionId, watchOptions.LabelSelector)
	}

	watchNamespace := ""
	if !watchOptions.AllNamespaces {
		watchNamespace = os.Getenv("RUNTIME_NAMESPACE")
	}

	watchSelector, err := helper.GetWatchSelector(watchOptions)
	if err != nil {
		setupLog.Error(err, "unable to configure watch label selector for manager")
		os.Exit(1)
	}

	opts := ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:        healthAddr,
		LeaderElection:                leaderElectionOptions.Enable,
		LeaderElectionReleaseOnCancel: leaderElectionOptions.ReleaseOnCancel,
		LeaseDuration:                 &leaderElectionOptions.LeaseDuration,
		RenewDeadline:                 &leaderElectionOptions.RenewDeadline,
		RetryPeriod:                   &leaderElectionOptions.RetryPeriod,
		GracefulShutdownTimeout:       &gracefulShutdownTimeout,
		LeaderElectionID:              leaderElectionId,
		Cache: ctrlcache.Options{
			ByObject: map[ctrlclient.Object]ctrlcache.ByObject{
				&infrav1beta1.SuperGraph{}:       {Label: watchSelector},
				&infrav1beta1.SuperGraphSchema{}: {Label: watchSelector},
				&infrav1beta1.SubGraph{}:         {Label: watchSelector},
			},
		},
	}

	if watchNamespace != "" {
		opts.Cache.DefaultNamespaces = map[string]ctrlcache.Config{
			watchNamespace: ctrlcache.Config{},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Add liveness probe
	err = mgr.AddHealthzCheck("healthz", healthz.Ping)
	if err != nil {
		setupLog.Error(err, "Could not add liveness probe")
		os.Exit(1)
	}

	// Add readiness probe
	err = mgr.AddReadyzCheck("readyz", healthz.Ping)
	if err != nil {
		setupLog.Error(err, "Could not add readiness probe")
		os.Exit(1)
	}

	supergraphReconciler := &controllers.SuperGraphReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("SuperGraph"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("SuperGraph"),
	}

	if err = supergraphReconciler.SetupWithManager(mgr, controllers.SuperGraphReconcilerOptions{
		MaxConcurrentReconciles: concurrent,
	}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SuperGraph")
		os.Exit(1)
	}

	subgraphReconciler := &controllers.SubGraphReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("SubGraph"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("SubGraph"),
	}

	if err = subgraphReconciler.SetupWithManager(mgr, controllers.SubGraphReconcilerOptions{
		MaxConcurrentReconciles: concurrent,
	}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SubGraph")
		os.Exit(1)
	}

	supergraphschemaReconciler := &controllers.SuperGraphSchemaReconciler{
		Client:                 mgr.GetClient(),
		Log:                    ctrl.Log.WithName("controllers").WithName("SuperGraphSchema"),
		Scheme:                 mgr.GetScheme(),
		Recorder:               mgr.GetEventRecorderFor("SuperGraphSchema"),
		DefaultSuperGraphImage: defaultSuperGraphImage,
		DefaultHTTPDImage:      defaultHTTPDImage,
		HTTPGetter:             http.DefaultClient,
	}

	if err = supergraphschemaReconciler.SetupWithManager(mgr, controllers.SuperGraphSchemaReconcilerOptions{
		MaxConcurrentReconciles: concurrent,
	}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SuperGraphSchema")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder
	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
