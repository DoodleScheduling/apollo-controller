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

			/*By("validating the schema secret")
			secret := corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())
			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","components":null,"requiredActions":null}`, schema.Name)))*/
		})

		It("transitions into ready once the reconciler pod terminates", func() {
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(Succeed())

			By("setting the reconciler pod as done")
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Succeed())

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
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, pod)).Should(Not(BeNil()))

			Expect(reconciledInstance.Status.Reconciler.Name).Should(Equal(""))

			By("making sure the schema secret is gone")
			var secret *corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      reconciledInstance.Status.Reconciler.Name,
				Namespace: reconciledInstance.Namespace,
			}, secret)).Should(Not(BeNil()))
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
				Spec: v1beta1.SubGraphSpec{},
			}
			Expect(k8sClient.Create(ctx, subgraph)).Should(Succeed())

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

			/*By("validating the schema secret")
			secret := corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())

			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","users":[{"username":"%s"}],"clients":[{"clientId":"%s"}],"components":null,"requiredActions":null}`, schema.Name, userName, clientName)))*/
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
				Spec: v1beta1.SubGraphSpec{},
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
		subName1 := fmt.Sprintf("sub-%s", rand.String(5))
		subName2 := fmt.Sprintf("sub-%s", rand.String(5))

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
				Spec: v1beta1.SubGraphSpec{},
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
				Spec: v1beta1.SubGraphSpec{},
			}
			Expect(k8sClient.Create(ctx, sub2)).Should(Succeed())

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

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName1,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			/*By("validating the schema secret")
			secret := corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())

			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","clients":[{"clientId":"%s"}],"components":null,"requiredActions":null}`, schema.Name, clientName)))*/
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
				Spec: v1beta1.SubGraphSpec{},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

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

			Expect(reconciledInstance.Status.Reconciler.Name).Should(Equal(int64(2)))

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			/*By("validating the schema secret")
			secret := corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())

			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","users":[{"username":"%s"}],"components":null,"requiredActions":null}`, schema.Name, subGraph)))*/
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
				Spec: v1beta1.SubGraphSpec{},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

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

			sub.Spec.Endpoint = "https://example2.com"
			Expect(k8sClient.Update(ctx, sub)).Should(Succeed())

			Eventually(func() error {
				err := k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)
				if err != nil {
					return err
				}

				if reconciledInstance.Status.Reconciler.Name == beforeUpdateStatus.Reconciler.Name || reconciledInstance.Status.Reconciler.Name == "" {
					return fmt.Errorf("%s == %s", reconciledInstance.Status.Reconciler.Name, beforeUpdateStatus.Reconciler.Name)
				}

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(BeTrue())

			By("making sure the resource catalog is correct")
			catalog := []v1beta1.ResourceReference{
				{
					Kind:       "SubGraph",
					Name:       subName,
					APIVersion: v1beta1.GroupVersion.String(),
				},
			}

			Expect(reconciledInstance.Status.SubResourceCatalog).Should(Equal(catalog))

			/*By("validating the schema secret")
			secret = corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())

			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","users":[{"username":"%s","enabled":true}],"components":null,"requiredActions":null}`, schema.Name, userName)))*/
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
			Expect(pod.Spec.Containers[1].Name).Should(Equal("sidecar"))
			Expect(pod.Spec.Containers[1].Image).Should(Equal("sidecar:1"))
			Expect(pod.Spec.Containers[0].Image).Should(Equal("custom-image:1"))
			Expect(pod.Spec.Containers[0].Env).Should(Equal(envs))
			Expect(reconciledInstance.Status.SubResourceCatalog).Should(HaveLen(0))
		})

		It("recreates the running reconciler pod if the spec changed", func() {
			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(BeNil())

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
			}, timeout, interval).Should(BeNil())

			Expect(pod.Spec.Containers[1].Image).Should(Equal("new-image:v1"))
		})

		It("recreates the running reconciler pod if the spec annotation is not present", func() {
			By("waiting for the reconciliation")
			instanceLookupKey := types.NamespacedName{Name: schemaName, Namespace: "default"}
			reconciledInstance := &v1beta1.SuperGraphSchema{}
			Expect(k8sClient.Get(ctx, instanceLookupKey, reconciledInstance)).Should(BeNil())
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
				Spec: v1beta1.SubGraphSpec{},
			}
			Expect(k8sClient.Create(ctx, sub)).Should(Succeed())

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

				return needsExactConditions(expectedStatus.Conditions, reconciledInstance.Status.Conditions)
			}, timeout, interval).Should(Not(HaveOccurred()))

			/*By("validating the schema secret")
			secret := corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())

			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","users":[{"username":"%s"}],"components":null,"requiredActions":null}`, schema.Name, userName)))*/

			beforeUpdateStatus := reconciledInstance.Status

			sub.Spec.Endpoint = "changed"
			Expect(k8sClient.Update(ctx, sub)).Should(Succeed())

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

			/*By("validating the schema secret")
			secret = corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      reconciledInstance.Status.Reconciler.Name,
					Namespace: reconciledInstance.Namespace,
				}, &secret)
			}, timeout, interval).Should(BeNil())

			Expect(string(secret.Data["schema.json"])).Should(Equal(fmt.Sprintf(`{"schema":"%s","users":[{"username":"%s","enabled":true}],"components":null,"requiredActions":null}`, schema.Name, userName)))*/
		})
	})
})
