package controllers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/DoodleScheduling/apollo-controller/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("SuperGraph controller", func() {
	const (
		timeout  = time.Second * 3
		interval = time.Millisecond * 200
	)

	var eventuallyMatchExactConditions = func(ctx context.Context, instanceLookupKey types.NamespacedName, reconciledInstance *v1beta1.SuperGraph, expectedStatus *v1beta1.SuperGraphStatus) {
		Eventually(func() error {
			err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			if err != nil {
				return err
			}

			return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
		}, timeout, interval).Should(BeNil())
	}

	When("reconciling a suspended SuperGraph", func() {
		supergraphName := fmt.Sprintf("supergraph-%s", randStringRunes(5))

		It("should not update the status", func() {
			By("creating a new SuperGraph")
			ctx := context.Background()

			supergraph := &v1beta1.SuperGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      supergraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSpec{
					Suspend: true,
				},
			}
			Expect(k8sClient.Create(ctx, supergraph)).Should(Succeed())

			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: supergraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraph{}

			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, &v1beta1.SuperGraphStatus{})
		})
	})

	When("it reconciles a supergraph without a schema", func() {
		supergraphName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		var supergraph *v1beta1.SuperGraph

		It("creates a new supergraph", func() {
			ctx := context.Background()

			supergraph = &v1beta1.SuperGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      supergraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSpec{},
			}
			Expect(k8sClient.Create(ctx, supergraph)).Should(Succeed())
		})

		It("should update the supergraph status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: supergraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraph{}

			expectedStatus := &v1beta1.SuperGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionFalse,
						Reason:  "ReconciliationFailed",
						Message: "schema not found",
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("should not create a service", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			reconciledInstance := &corev1.Service{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).ShouldNot(BeNil())
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, supergraph)).Should(Succeed())
		})
	})

	When("it reconciles a supergraph with a schema", func() {
		schemaName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		supergraphName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		var schema *v1beta1.SuperGraphSchema
		var supergraph *v1beta1.SuperGraph

		It("creates a new schema", func() {
			ctx := context.Background()

			schema = &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{},
			}

			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())
			configMapName := fmt.Sprintf("supergraph-schema-%s", schemaName)
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, schema)
				if err != nil {
					return err
				}

				schema.Status.ConfigMap.Name = configMapName
				schema.Status.ObservedSHA256Checksum = "dummy-check"

				return k8sClient.Status().Update(ctx, schema)
			}, timeout, interval).Should(Not(HaveOccurred()))

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: "default",
				},
				Data: map[string]string{
					"schema.graphql": "type Query { hello: String }",
				},
			}

			Expect(k8sClient.Create(ctx, cm)).Should(Succeed())
		})

		It("creates a new supergraph", func() {
			ctx := context.Background()

			supergraph = &v1beta1.SuperGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      supergraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSpec{
					Schema: corev1.LocalObjectReference{
						Name: schemaName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, supergraph)).Should(Succeed())
		})

		It("should update the supergraph status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: supergraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraph{}

			expectedStatus := &v1beta1.SuperGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/apollo-router-%s created", supergraphName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("should create a service", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			reconciledInstance := &corev1.Service{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Selector).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(supergraphName))
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))

			configChecksum := sha256.New()
			configChecksum.Write([]byte(""))

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Annotations).To(Equal(map[string]string{
				"apollo-controller/schema-checksum": schema.Status.ObservedSHA256Checksum,
				"apollo-controller/config-checksum": fmt.Sprintf("%x", configChecksum.Sum(nil)),
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("router"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/apollographql/router:v2.10.0"))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(supergraphName))
		})

		It("updates the deployment if the referenced schema checksum changes", func() {
			ctx := context.Background()

			schema = &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{},
			}
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, schema)
				if err != nil {
					return err
				}

				schema.Status.ObservedSHA256Checksum = "dummy-check-v2"
				return k8sClient.Status().Update(ctx, schema)
			}, timeout, interval).Should(Not(HaveOccurred()))

			instanceLookupKey = types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Spec.Template.ObjectMeta.Annotations["apollo-controller/schema-checksum"] != "dummy-check-v2" {
					return errors.New("schema checksum not updated")
				}

				return nil
			}, timeout, interval).Should(BeNil())
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, supergraph)).Should(Succeed())
		})
	})

	When("it reconciles a supergraph with a custom template", func() {
		schemaName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		supergraphName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		var schema *v1beta1.SuperGraphSchema
		var supergraph *v1beta1.SuperGraph
		var replicas int32 = 3

		It("creates a new schema", func() {
			ctx := context.Background()

			schema = &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())
			configMapName := fmt.Sprintf("supergraph-schema-%s", schemaName)
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, schema)
				if err != nil {
					return err
				}

				schema.Status.ConfigMap.Name = configMapName
				return k8sClient.Status().Update(ctx, schema)
			}, timeout, interval).Should(Not(HaveOccurred()))

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: "default",
				},
				Data: map[string]string{
					"schema.graphql": "type Query { hello: String }",
				},
			}

			Expect(k8sClient.Create(ctx, cm)).Should(Succeed())
		})

		It("creates a new supergraph", func() {
			ctx := context.Background()

			supergraph = &v1beta1.SuperGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      supergraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSpec{
					Schema: corev1.LocalObjectReference{
						Name: schemaName,
					},
					DeploymentTemplate: &v1beta1.DeploymentTemplate{
						Spec: v1beta1.DeploymentSpec{
							Replicas: &replicas,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name: "router",
											Env: []corev1.EnvVar{
												{
													Name:  "FOO",
													Value: "bar",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, supergraph)).Should(Succeed())
		})

		It("should update the supergraph status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: supergraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraph{}

			expectedStatus := &v1beta1.SuperGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/apollo-router-%s created", supergraphName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))

			configChecksum := sha256.New()
			configChecksum.Write([]byte(""))

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Annotations).To(Equal(map[string]string{
				"apollo-controller/schema-checksum": schema.Status.ObservedSHA256Checksum,
				"apollo-controller/config-checksum": fmt.Sprintf("%x", configChecksum.Sum(nil)),
			}))

			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Name).To(Equal("router"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/apollographql/router:v2.10.0"))
			Expect(reconciledInstance.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
				{
					Name:  "FOO",
					Value: "bar",
				},
			}))
			Expect(reconciledInstance.OwnerReferences[0].Name).Should(Equal(supergraphName))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, supergraph)).Should(Succeed())
		})
	})

	When("it reconciles a supergraph with a custom router config", func() {
		schemaName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		supergraphName := fmt.Sprintf("supergraph-%s", randStringRunes(5))
		var schema *v1beta1.SuperGraphSchema
		var supergraph *v1beta1.SuperGraph

		It("creates a new schema", func() {
			ctx := context.Background()

			schema = &v1beta1.SuperGraphSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:      schemaName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSchemaSpec{},
			}
			Expect(k8sClient.Create(ctx, schema)).Should(Succeed())
			configMapName := fmt.Sprintf("supergraph-schema-%s", schemaName)
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, schema)
				if err != nil {
					return err
				}

				schema.Status.ConfigMap.Name = configMapName
				schema.Status.ObservedSHA256Checksum = "dummy-checksum"
				return k8sClient.Status().Update(ctx, schema)
			}, timeout, interval).Should(Not(HaveOccurred()))

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: "default",
				},
				Data: map[string]string{
					"schema.graphql": "type Query { hello: String }",
				},
			}

			Expect(k8sClient.Create(ctx, cm)).Should(Succeed())
		})

		It("creates a new supergraph", func() {
			ctx := context.Background()

			supergraph = &v1beta1.SuperGraph{
				ObjectMeta: metav1.ObjectMeta{
					Name:      supergraphName,
					Namespace: "default",
				},
				Spec: v1beta1.SuperGraphSpec{
					Schema: corev1.LocalObjectReference{
						Name: schemaName,
					},
					RouterConfig: runtime.RawExtension{
						Raw: []byte(`{"foo": "bar"}`),
					},
				},
			}
			Expect(k8sClient.Create(ctx, supergraph)).Should(Succeed())
		})

		It("should update the supergraph status", func() {
			ctx := context.Background()
			instanceLookupKey := types.NamespacedName{Name: supergraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraph{}

			expectedStatus := &v1beta1.SuperGraphStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:    v1beta1.ConditionReady,
						Status:  metav1.ConditionTrue,
						Reason:  "ReconciliationSuccessful",
						Message: fmt.Sprintf("deployment/apollo-router-%s created", supergraphName),
					},
				},
			}
			eventuallyMatchExactConditions(ctx, instanceLookupKey, reconciledInstance, expectedStatus)
		})

		It("should create a deployment", func() {
			instanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			reconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				return k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
			}, timeout, interval).Should(BeNil())

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Labels).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))

			Expect(reconciledInstance.ObjectMeta.Labels).To(Equal(map[string]string{
				"apollo-controller/supergraph": supergraphName,
				"app.kubernetes.io/instance":   "apollo-router",
				"app.kubernetes.io/name":       "apollo-router",
			}))

			configChecksum := sha256.New()
			configChecksum.Write(supergraph.Spec.RouterConfig.Raw)

			Expect(reconciledInstance.Spec.Template.ObjectMeta.Annotations).To(Equal(map[string]string{
				"apollo-controller/schema-checksum": schema.Status.ObservedSHA256Checksum,
				"apollo-controller/config-checksum": fmt.Sprintf("%x", configChecksum.Sum(nil)),
			}))
		})

		It("updates the deployment config checksum if the config is changed", func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: supergraphName, Namespace: "default"}, supergraph)).Should(Succeed())
			supergraph.Spec.RouterConfig.Raw = []byte(`{"foo": "baz"}`)
			Expect(k8sClient.Update(ctx, supergraph)).Should(Succeed())

			instanceLookupKey := types.NamespacedName{Name: supergraphName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraph{}

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.ObservedSHA256Checksum == supergraph.Status.ObservedSHA256Checksum {
					return errors.New("config checksum not updated")
				}

				return nil
			}, timeout, interval).Should(Not(HaveOccurred()))

			deploymentInstanceLookupKey := types.NamespacedName{Name: fmt.Sprintf("apollo-router-%s", supergraphName), Namespace: "default"}
			deploymentReconciledInstance := &appsv1.Deployment{}

			Eventually(func() error {
				err := k8sClient.Get(ctx, deploymentInstanceLookupKey, deploymentReconciledInstance)
				if err != nil {
					return err
				}

				if deploymentReconciledInstance.Spec.Template.ObjectMeta.Annotations["apollo-controller/config-checksum"] != reconciledInstance.Status.ObservedSHA256Checksum {
					return errors.New("schema checksum not updated")
				}

				return nil
			}, timeout, interval).Should(BeNil())

			configChecksum := sha256.New()
			configChecksum.Write(supergraph.Spec.RouterConfig.Raw)

			Expect(deploymentReconciledInstance.Spec.Template.ObjectMeta.Annotations).To(Equal(map[string]string{
				"apollo-controller/schema-checksum": "dummy-checksum",
				"apollo-controller/config-checksum": fmt.Sprintf("%x", configChecksum.Sum(nil)),
			}))
		})

		It("cleans up", func() {
			ctx := context.Background()
			Expect(k8sClient.Delete(ctx, supergraph)).Should(Succeed())
		})
	})
})
