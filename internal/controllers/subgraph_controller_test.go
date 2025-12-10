package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/DoodleScheduling/apollo-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("SubGraph controller", func() {
	const (
		timeout  = time.Second * 4
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

		It("should not update the status", func() {
			By("creating a new SubGraph")
			ctx := context.Background()

			gi := &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, gi)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: subgraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SubGraph{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.SubGraphStatus{})
		})
	})

	When("it reconciles a subgraph without a schema", func() {
		subgraphName := fmt.Sprintf("subgraph-%s", randStringRunes(5))
		var subgraph *v1beta1.SubGraph

		It("creates a new subgraph", func() {
			ctx := context.Background()

			subgraph = &v1beta1.SubGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      subgraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SubGraphSpec{},
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
						Message: "no schema defined",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("should not create a configmap", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("subgraph-schema-%s", subgraphName), Namespace: "default"}
			reconciledInstance := &corev1.ConfigMap{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).ShouldNot(BeNil())
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})

	When("it reconciles a subgraph with an inline schema", func() {
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
					Schema: &v1beta1.Schema{
						SDL: "type Query { hello: String }",
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
						Message: fmt.Sprintf("configmap/subgraph-schema-%s created", subgraphName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("should create a configmap", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("subgraph-schema-%s", subgraphName), Namespace: "default"}
			reconciledInstance := &corev1.ConfigMap{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(subgraphName))
			Expect(reconciledInstance.Data).Should(HaveKey("schema.graphql"))
			Expect(reconciledInstance.Data["schema.graphql"]).Should(Equal("type Query { hello: String }"))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, subgraph)).Should(Succeed())
		})
	})
})
