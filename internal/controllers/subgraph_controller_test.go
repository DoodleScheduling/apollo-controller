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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("SubGraph controller", func() {
	const (
		timeout  = time.Second * 3
		interval = time.Millisecond * 200
	)

	var eventuallyMatchExactConditions = func(ctx context.Context, instanceLookupKey types.NamespacedName, reconciledInstance *v1beta1.SubGraph, expectedStatus *v1beta1.SubGraphStatus) {
		Eventually(func() error {
			err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			if err != nil {
				return err
			}

			return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
		}, timeout, interval).Should(BeNil())
	}

	When("reconciling a suspended SubGraph", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		schemaSDL := "type Query { hello: String }"

		It("should not update the status", func() {
			By("creating a new SubGraph")
			ctx := context.Background()

			subgraph := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Suspend: true,
					Schema: v1beta1.Schema{
						SDL: &schemaSDL,
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SubGraph{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.SubGraphStatus{})
		})
	})

	When("it reconciles a subgraph with an inline schema", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		var subgraph *v1beta1.SubGraph
		schema := "type Query { hello: String }"

		It("creates a new subgraph", func() {
			ctx := context.Background()

			subgraph = &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: v1beta1.Schema{
						SDL: &schema,
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())
		})

		It("should update the subgraph status", func() {
			ctx := context.Background()
			reconciledInstance := &v1beta1.SubGraph{}
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}

			expectedStatus := &v1beta1.SubGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: "schema available",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.Schema).To(Equal(schema))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})

	When("it reconciles a subgraph with an http schema", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		var subgraph *v1beta1.SubGraph

		It("creates a new subgraph", func() {
			ctx := context.Background()

			subgraph = &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: v1beta1.Schema{
						HTTP: &v1beta1.SchemaHTTP{
							Endpoint: "http://127.0.0.1:29001",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())
		})

		It("should update the subgraph status", func() {
			schemaSDL := `type Query { hello: String }`

			By("setting up HTTP server to serve schema")
			listener, err := net.Listen("tcp", "127.0.0.1:29001")
			Expect(err).NotTo(HaveOccurred())
			httpServer := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(schemaSDL))
				}),
			}

			go func() {
				_ = httpServer.Serve(listener)
			}()

			defer func() {
				_ = httpServer.Shutdown(context.Background())
				_ = listener.Close()
			}()

			ctx := context.Background()
			reconciledInstance := &v1beta1.SubGraph{}
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}

			expectedStatus := &v1beta1.SubGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: "schema available",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.Schema).To(Equal(schemaSDL))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})

	When("reconciling a SubGraph with a schema from an http endpoint runs into a specified timeout", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		var subgraph *v1beta1.SubGraph

		It("should not update the status", func() {
			By("creating a new SubGraph")
			ctx := context.Background()

			subgraph = &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Timeout: &metav1.Duration{},
					Schema: v1beta1.Schema{
						HTTP: &v1beta1.SchemaHTTP{
							Endpoint: "http://127.0.0.1:29001",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())
		})

		It("should update the subgraph status", func() {
			ctx := context.Background()
			reconciledInstance := &v1beta1.SubGraph{}
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}

			expectedStatus := &v1beta1.SubGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionFalse,
						Reason:  "ReconciliationFailed",
						Message: "failed to fetch schema from http endpoint: Get \"http://127.0.0.1:29001\": context deadline exceeded",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})

	When("it reconciles a subgraph with an invalid schema", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		var subgraph *v1beta1.SubGraph
		schema := "not a valid graphql schema"

		It("creates a new subgraph", func() {
			ctx := context.Background()

			subgraph = &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Schema: v1beta1.Schema{
						SDL: &schema,
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())
		})

		It("should update the subgraph status", func() {
			ctx := context.Background()
			reconciledInstance := &v1beta1.SubGraph{}
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}

			expectedStatus := &v1beta1.SubGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionFalse,
						Reason:  "ReconciliationFailed",
						Message: "schema is invalid: schema.graphql:1:1: Unexpected Name \"not\"",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.Schema).To(Equal(""))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})

	When("it reconciles a subgraph with an invalid schema but schema validation is disabled", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		var subgraph *v1beta1.SubGraph
		schema := "not a valid graphql schema"

		It("creates a new subgraph", func() {
			ctx := context.Background()

			subgraph = &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					SkipSchemaValidation: true,
					Schema: v1beta1.Schema{
						SDL: &schema,
					},
				},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())
		})

		It("should update the subgraph status", func() {
			ctx := context.Background()
			reconciledInstance := &v1beta1.SubGraph{}
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}

			expectedStatus := &v1beta1.SubGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: "schema available",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
			Expect(reconciledInstance.Status.Schema).To(Equal(schema))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})
})
