package controllers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/DoodleScheduling/apollo-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
)

var _ = Describe("SuperGraphSchema controller", func() {
	const (
		timeout  = time.Second * 4
		interval = time.Millisecond * 50
	)

	When("reconciling a suspended SuperGraphSchema", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))

		It("should not update the status", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return false
				}

				return len(reconciledInstance.Status.Conditions) == 0
			}, timeout, interval).Should(BeTrue())
		})
	})

	When("a simple schema is reconciled", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))

		It("should transition into progressing", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()
			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure there is a reconciler pod")
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Succeed())

			By("validating the reconciler pod")
			Expect(pod.Spec.Containers).Should(HaveLen(2))
			Expect(pod.Spec.Containers[0].Image).Should(Equal("rover:v0"))
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(HaveLen(0))
			Expect(pod.Spec.Containers[1].Image).Should(Equal("busybox:v0"))

			pod.Status.PodIP = "127.0.0.1"
			Expect(k8sClient.Status().Update(ctx, pod)).Should(Succeed())

			By("validating the composeConfig ConfigMap")
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			Expect(configMap.OwnerReferences[0].Name).Should(Equal(reconciledInstance.Status.Reconciler.Name))
			Expect(configMap.Data).Should(HaveKey("supergraph.yaml"))
			expectedYAML := `federation_version: 2
subgraphs: {}
`
			Expect(configMap.Data["supergraph.yaml"]).Should(Equal(expectedYAML))
		})

		It("transitions into ready once the reconciler pod terminates", func() {
			ctx := context.Background()
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(Succeed())

			By("setting up HTTP server to serve schema")
			listener, err := net.Listen("tcp", "127.0.0.1:29000")
			Expect(err).NotTo(HaveOccurred())
			httpServer := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/schema.graphql" {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("type Query { hello: String }"))
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}),
			}
			go func() {
				_ = httpServer.Serve(listener)
			}()

			By("setting the reconciler pod as done")
			reconcilerPodName := reconciledInstance.Status.Reconciler.Name
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Succeed())

			// Set PodIP so the controller can fetch the schema
			pod.Status.PodIP = "127.0.0.1"
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "rover",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
			}

			Expect(k8sClient.Status().Update(ctx, pod)).Should(Succeed())

			By("waiting for the reconciliation")
			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSucceeded",
						Message: fmt.Sprintf("reconciler %s terminated with code 0", pod.Name),
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			Expect(reconciledInstance.Status.Reconciler.Name).Should(Equal(""))

			By("making sure the reconciler pod is gone")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconcilerPodName,
				Namespace: reconciledInstance.Namespace,
			}, pod)).ShouldNot(BeNil())

			Expect(reconciledInstance.Status.Reconciler.Name).Should(Equal(""))

			By("cleaning up")
			Expect(httpServer.Shutdown(context.Background())).Should(BeNil())
			_ = listener.Close()
		})
	})

	When("a schema is reconciled with sub resources", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))
		subName := fmt.Sprintf("sub-%s", rand.String(5))

		It("should transition into progressing", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()
			subgraph := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())

			By("waiting for the subgraph to be ready")
			Eventually(func() bool {
				subgraphReady := &v1beta1.SubGraph{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: subName, Namespace: "default"}, subgraphReady); err != nil {
					return false
				}
				return subgraphReady.Status.ConfigMap.Name != ""
			}, timeout, interval).Should(BeTrue())

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					SubGraphSelector: &metav1.LabelSelector{},
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			By("validating the composeConfig ConfigMap")
			configMap := &corev1.ConfigMap{}
			expectedYAML := fmt.Sprintf(`federation_version: 2
subgraphs:
    %s:
        routing_url: ""
        schema:
            file: /schemas/default.%s.graphql
`, subName, subName)
			Eventually(func() error {
				// Re-fetch the instance to get the latest reconciler name (it might have changed)
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("reconciler name is empty")
				}

				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMap)
				if err != nil {
					return err
				}

				if configMap.Data["supergraph.yaml"] != expectedYAML {
					return fmt.Errorf("expected yaml %s, got %s", expectedYAML, configMap.Data["supergraph.yaml"])
				}

				return nil
			}, timeout, interval).Should(Succeed())

			Expect(configMap.OwnerReferences[0].Name).Should(Equal(reconciledInstance.Status.Reconciler.Name))
			Expect(configMap.Data).Should(HaveKey("supergraph.yaml"))
		})

		It("transitions into ready once the reconciler pod terminates", func() {
			ctx := context.Background()
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(Succeed())

			By("waiting for the reconciler pod to be created")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return false
				}
				return reconciledInstance.Status.Reconciler.Name != ""
			}, timeout, interval).Should(BeTrue())

			By("setting up HTTP server to serve schema")
			listener, err := net.Listen("tcp", "127.0.0.1:29000")
			Expect(err).NotTo(HaveOccurred())
			httpServer := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/schema.graphql" {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("type Query { hello: String }"))
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}),
			}
			go func() {
				_ = httpServer.Serve(listener)
			}()

			By("setting the reconciler pod as done")
			reconcilerPodName := reconciledInstance.Status.Reconciler.Name

			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Succeed())

			// Set PodIP so the controller can fetch the schema
			pod.Status.PodIP = "127.0.0.1"
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "rover",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
			}

			Expect(k8sClient.Status().Update(ctx, pod)).Should(Succeed())

			By("waiting for the reconciliation")
			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSucceeded",
						Message: fmt.Sprintf("reconciler %s terminated with code 0", pod.Name),
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				// Wait for the reconciler name to be cleared, indicating the pod was deleted and status updated
				if reconciledInstance.Status.Reconciler.Name != "" {
					return fmt.Errorf("reconciler name is still set: %s", reconciledInstance.Status.Reconciler.Name)
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			Expect(reconciledInstance.Status.Reconciler.Name).Should(Equal(""))

			By("making sure the reconciler pod is gone")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconcilerPodName,
				Namespace: reconciledInstance.Namespace,
			}, pod)
			Expect(apierrors.IsNotFound(err)).Should(BeTrue())

			Expect(reconciledInstance.Status.Reconciler.Name).Should(Equal(""))

			By("making sure the composed schema ConfigMap exists")
			composedSchema := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.ConfigMap.Name,
				Namespace: reconciledInstance.Namespace,
			}, composedSchema)).Should(Succeed())

			Expect(composedSchema.OwnerReferences[0].Name).Should(Equal(schemaName))
			Expect(composedSchema.Data).Should(HaveKey("schema.graphql"))
			expectedSchema := `type Query { hello: String }`
			Expect(composedSchema.Data["schema.graphql"]).Should(Equal(expectedSchema))

			By("cleaning up")
			Expect(httpServer.Shutdown(context.Background())).Should(BeNil())
			_ = listener.Close()
		})
	})

	When("a schema which has no resource selector will not select any sub resources", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))
		subName := fmt.Sprintf("schema-%s", rand.String(5))

		It("should transition into progressing", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()
			sub := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure the resource catalog is correct")
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(HaveLen(0))
		})
	})

	When("a schema is reconciled with sub resources but limits the resources selected", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))
		subName1 := fmt.Sprintf("sub-a-%s", rand.String(5))
		subName2 := fmt.Sprintf("sub-b-%s", rand.String(5))

		It("should transition into progressing", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()

			sub1 := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName1,
					Namespace: "default",
					Labels: map[string]string{
						"selectable": "yes",
					},
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sub1)).Should(Succeed())

			sub2 := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName2,
					Namespace: "default",
					Labels: map[string]string{
						"selectable": "yes",
					},
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { world: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sub2)).Should(Succeed())

			By("waiting for both subgraphs to be ready")
			Eventually(func() bool {
				sub1Ready := &v1beta1.SubGraph{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: subName1, Namespace: "default"}, sub1Ready); err != nil {
					return false
				}
				sub2Ready := &v1beta1.SubGraph{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: subName2, Namespace: "default"}, sub2Ready); err != nil {
					return false
				}
				return sub1Ready.Status.ConfigMap.Name != "" && sub2Ready.Status.ConfigMap.Name != ""
			}, timeout, interval).Should(BeTrue())

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					SubGraphSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"selectable": "yes",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if len(reconciledInstance.Status.SubResourceCatalog) != 2 {
					return fmt.Errorf("expected catalog length %d, got %d", 2, len(reconciledInstance.Status.SubResourceCatalog))
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure the resource catalog is correct")
			// Both subgraphs match the selector, so both should be in the catalog
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName1,
					APIVersion: v1beta1.GroupVersion.String(),
				},
				{
					Kind:       "SubGraph",
					Name:       subName2,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))
			expectedYAML := fmt.Sprintf(`federation_version: 2
subgraphs:
    %s:
        routing_url: ""
        schema:
            file: /schemas/default.%s.graphql
    %s:
        routing_url: ""
        schema:
            file: /schemas/default.%s.graphql
`, subName1, subName1, subName2, subName2)

			By("validating the composeConfig ConfigMap")
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				// Re-fetch the instance to get the latest reconciler name (it might have changed)
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("reconciler name is empty")
				}

				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMap)

				if err != nil {
					return err
				}

				if configMap.Data["supergraph.yaml"] != expectedYAML {
					return fmt.Errorf("expected yaml %s, got %s", expectedYAML, configMap.Data["supergraph.yaml"])
				}

				return nil
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a schema is updated while a reconciler pod is running", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))
		subName := fmt.Sprintf("sub-%s", rand.String(5))

		It("recreates the reconciler with a new secret", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()
			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					SubGraphSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"match": "yes",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			beforeUpdateStatus := reconciledInstance.Status

			sub := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName,
					Namespace: "default",
					Labels: map[string]string{
						"match": "yes",
					},
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

			By("waiting for the subgraph to be ready")
			Eventually(func() bool {
				subgraphReady := &v1beta1.SubGraph{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: subName, Namespace: "default"}, subgraphReady); err != nil {
					return false
				}
				return subgraphReady.Status.ConfigMap.Name != ""
			}, timeout, interval).Should(BeTrue())

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == beforeUpdateStatus.Reconciler.Name || reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("%s == %s", reconciledInstance.Status.Reconciler.Name, beforeUpdateStatus.Reconciler.Name)
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			// The reconciler name should be different from before (a new pod was created)
			Expect(reconciledInstance.Status.Reconciler.Name).ShouldNot(Equal(""))

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			By("validating the composeConfig ConfigMap")
			// Subgraph created without endpoint, so routing_url should be empty
			expectedYAML := fmt.Sprintf("federation_version: 2\nsubgraphs:\n    %s:\n        routing_url: \"\"\n        schema:\n            file: /schemas/default.%s.graphql\n", subName, subName)
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				// Re-fetch the instance to get the latest reconciler name (it might have changed)
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("reconciler name is empty")
				}

				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMap)
				if err != nil {
					return err
				}

				if configMap.Data["supergraph.yaml"] != expectedYAML {
					return fmt.Errorf("expected yaml %s, got %s", expectedYAML, configMap.Data["supergraph.yaml"])
				}

				return nil
			}, timeout, interval).Should(Succeed())

			Expect(configMap.Data).Should(HaveKey("supergraph.yaml"))
		})
	})

	When("a schema reconciliation is triggered if a subgraph is changed", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))
		subName := fmt.Sprintf("sub-%s", rand.String(5))

		It("recreates the reconciler with a new secret", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()
			sub := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName,
					Namespace: "default",
					Labels: map[string]string{
						"trigger-subs": "yes",
					},
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

			By("waiting for the subgraph to be ready")
			Eventually(func() bool {
				subgraphReady := &v1beta1.SubGraph{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: subName, Namespace: "default"}, subgraphReady); err != nil {
					return false
				}
				return subgraphReady.Status.ConfigMap.Name != ""
			}, timeout, interval).Should(BeTrue())

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					SubGraphSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"trigger-subs": "yes",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))
			beforeUpdateStatus := reconciledInstance.Status

			// Retry update in case of conflicts with SubGraph controller
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}, sub)
				if err != nil {
					return err
				}
				sub.Spec.Endpoint = "https://example2.com"
				return k8sClient.Update(ctx, sub)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == beforeUpdateStatus.Reconciler.Name || reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("%s == %s", reconciledInstance.Status.Reconciler.Name, beforeUpdateStatus.Reconciler.Name)
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			By("validating the composeConfig ConfigMap")
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			Expect(configMap.Data).Should(HaveKey("supergraph.yaml"))
			// After endpoint update to "https://example2.com", routing_url should be "https://example2.com"
			expectedYAML := fmt.Sprintf(`federation_version: 2
subgraphs:
    %s:
        routing_url: https://example2.com
        schema:
            file: /schemas/default.%s.graphql
`, subName, subName)
			Expect(configMap.Data["supergraph.yaml"]).Should(Equal(expectedYAML))
		})
	})

	When("a schema with a custom reconciler pod template is reconciled", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))

		It("should transition into progressing", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					ReconcilerTemplate: &v1beta1.ReconcilerTemplate{
						ObjectMetadata: v1beta1.ObjectMetadata{
							Labels: map[string]string{
								"test": "label",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "rover",
									Image: "custom-image:1",
									Env: []corev1.EnvVar{
										{
											Name:  "TEST",
											Value: "TEST",
										},
									},
								},
								{
									Name:  "sidecar",
									Image: "sidecar:1",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure there is a reconciler pod")
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Succeed())

			By("validating the reconciler pod")
			envs := []corev1.EnvVar{
				{
					Name:  "TEST",
					Value: "TEST",
				},
			}

			Expect(pod.Labels["test"]).Should(Equal("label"))
			Expect(pod.Spec.Containers[2].Name).Should(Equal("sidecar"))
			Expect(pod.Spec.Containers[2].Image).Should(Equal("sidecar:1"))
			Expect(pod.Spec.Containers[0].Image).Should(Equal("custom-image:1"))
			Expect(pod.Spec.Containers[0].Env).Should(Equal(envs))
			Expect(pod.Spec.Containers[1].Image).Should(Equal("busybox:v0"))
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(HaveLen(0))
		})

		It("recreates the running reconciler pod if the spec changed", func() {
			By("waiting for the reconciliation")
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(Succeed())

			beforeChangeStatus := reconciledInstance.Status
			reconciledInstance.Spec.ReconcilerTemplate.Spec.Containers[1].Image = "new-image:v1"
			Expect(k8sClient.Update(ctx, reconciledInstance)).Should(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return false
				}

				return beforeChangeStatus.Reconciler.Name != reconciledInstance.Status.Reconciler.Name && reconciledInstance.Status.Reconciler.Name != ""
			}, timeout, interval).Should(BeTrue())

			By("making sure there is a reconciler pod")
			pod := &corev1.Pod{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, pod)
			}, timeout, interval).Should(Succeed())

			// The sidecar container is at index 2 after merge (rover=0, httpd=1, sidecar=2)
			Expect(pod.Spec.Containers[2].Image).Should(Equal("new-image:v1"))
		})

		It("recreates the running reconciler pod if the spec annotation is not present", func() {
			By("waiting for the reconciliation")
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(Succeed())
			beforeChangeStatus := reconciledInstance.Status

			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Succeed())
			pod.Annotations = nil
			Expect(k8sClient.Update(ctx, pod)).Should(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return false
				}

				return beforeChangeStatus.Reconciler.Name != reconciledInstance.Status.Reconciler.Name && reconciledInstance.Status.Reconciler.Name != ""
			}, timeout, interval).Should(BeTrue())
		})
	})

	When("a schema with an interval > 0 is triggered if a subgraph is changed", func() {
		schemaName := fmt.Sprintf("schema-%s", rand.String(5))
		subName := fmt.Sprintf("sub-%s", rand.String(5))

		It("recreates the reconciler with a new secret", func() {
			By("creating a new SuperGraphSchema")
			ctx := context.Background()

			sub := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subName,
					Namespace: "default",
					Labels: map[string]string{
						"trigger-checksum-subgraphs": "yes",
					},
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
					},
				},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

			By("waiting for the subgraph to be ready")
			Eventually(func() bool {
				subgraphReady := &v1beta1.SubGraph{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: subName, Namespace: "default"}, subgraphReady); err != nil {
					return false
				}
				return subgraphReady.Status.ConfigMap.Name != ""
			}, timeout, interval).Should(BeTrue())

			schema := &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{
					Interval: &metav1.Duration{Duration: time.Second * 100},
					SubGraphSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"trigger-checksum-subgraphs": "yes",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}

			expectedStatus := &v1beta1.SuperGraphSchemaStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionUnknown,
						Reason:  "Progressing",
						Message: "Reconciliation in progress",
					},
					{
						Type:   v1beta1.ConditionReconciling,
						Status: metav1.ConditionTrue,
						Reason: "Progressing",
					},
				},
			}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if len(reconciledInstance.Status.SubResourceCatalog) != 1 {
					return fmt.Errorf("SubResourceCatalog is empty")
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("validating the composeConfig ConfigMap")
			expectedYAML := fmt.Sprintf(`federation_version: 2
subgraphs:
    %s:
        routing_url: ""
        schema:
            file: /schemas/default.%s.graphql
`, subName, subName)

			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				// Re-fetch the instance to get the latest reconciler name (it might have changed)
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("reconciler name is empty")
				}

				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMap)

				if err != nil {
					return err
				}

				if configMap.Data["supergraph.yaml"] != expectedYAML {
					return fmt.Errorf("expected yaml %s, got %s", expectedYAML, configMap.Data["supergraph.yaml"])
				}

				return nil
			}, timeout, interval).Should(Succeed())

			beforeUpdateStatus := reconciledInstance.Status

			// Retry update in case of conflicts with SubGraph controller
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: sub.Name, Namespace: sub.Namespace}, sub)
				if err != nil {
					return err
				}
				sub.Spec.Endpoint = "changed"
				return k8sClient.Update(ctx, sub)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == beforeUpdateStatus.Reconciler.Name || reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("%s == %s", reconciledInstance.Status.Reconciler.Name, beforeUpdateStatus.Reconciler.Name)
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			By("validating the composeConfig ConfigMap after update")
			configMapAfterUpdate := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, configMapAfterUpdate)
			}, timeout, interval).Should(Succeed())

			Expect(configMapAfterUpdate.Data).Should(HaveKey("supergraph.yaml"))
			// After endpoint update to "changed", routing_url should be "changed"
			expectedYAMLAfterUpdate := fmt.Sprintf(`federation_version: 2
subgraphs:
    %s:
        routing_url: changed
        schema:
            file: /schemas/default.%s.graphql
`, subName, subName)
			Expect(configMapAfterUpdate.Data["supergraph.yaml"]).Should(Equal(expectedYAMLAfterUpdate))
		})
	})
})
