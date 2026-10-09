/*
Portions Copyright (c) Microsoft Corporation.

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
package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func (env *Environment) EventuallyExpectHealthyDeployment(deployment *appsv1.Deployment) []*corev1.Pod {
	GinkgoHelper()
	pods := env.Environment.EventuallyExpectHealthyDeployment(deployment)
	for _, pod := range pods {
		env.expectACLNodeEvidence(pod.Spec.NodeName)
	}
	return pods
}

func (env *Environment) EventuallyExpectHealthyPodCount(selector labels.Selector, count int) []*corev1.Pod {
	GinkgoHelper()
	pods := env.Environment.EventuallyExpectHealthyPodCount(selector, count)
	for _, pod := range pods {
		env.expectACLNodeEvidence(pod.Spec.NodeName)
	}
	return pods
}

func (env *Environment) EventuallyExpectInitializedNodeCount(comparator string, count int) []*corev1.Node {
	GinkgoHelper()
	nodes := env.Environment.EventuallyExpectInitializedNodeCount(comparator, count)
	for _, node := range nodes {
		env.expectACLNodeEvidence(node.Name)
	}
	return nodes
}

func (env *Environment) expectACLNodeEvidence(name string) {
	GinkgoHelper()
	if env.TestImageFamily == "" {
		return
	}
	node := &corev1.Node{}
	Expect(env.Client.Get(env, client.ObjectKey{Name: name}, node)).To(Succeed())
	nodeClass, err := nodeClassForACLEvidence(env.Context, env.Client, node)
	Expect(err).ToNot(HaveOccurred())
	// Explicit other-OS test cases remain compatibility controls.
	if lo.FromPtr(nodeClass.Spec.ImageFamily) != v1beta1.AzureContainerLinuxImageFamily {
		return
	}
	Expect(validateACLNodeIdentity(node)).To(Succeed())
	expectedVersion, err := nodeClass.GetKubernetesVersion()
	Expect(err).ToNot(HaveOccurred())
	Expect(validateACLNodeVersion(node, expectedVersion)).To(Succeed())
	Eventually(func(g Gomega) {
		pods, err := env.KubeClient.CoreV1().Pods("kube-system").List(env, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + node.Name,
		})
		g.Expect(err).ToNot(HaveOccurred())
		ready := map[string]bool{}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					ready[pod.Labels["k8s-app"]] = true
				}
			}
		}
		g.Expect(ready["azure-cns"]).To(BeTrue(), "CNS must be Ready on %s", node.Name)
		g.Expect(ready["cilium"]).To(BeTrue(), "Cilium must be Ready on %s", node.Name)
	}).Should(Succeed())
	machineID := node.Annotations[v1beta1.AnnotationAKSMachineResourceID]
	machine := env.ExpectMachineByID(machineID)
	driftAction, driftReason := "", ""
	if machine.Properties != nil && machine.Properties.Status != nil {
		driftAction = string(lo.FromPtr(machine.Properties.Status.DriftAction))
		driftReason = lo.FromPtr(machine.Properties.Status.DriftReason)
	}
	fmt.Fprintf(GinkgoWriter, "ACL lifecycle evidence: node=%s pool=%s nodeClass=%s image=%s machine=%s machineDriftAction=%q machineDriftReason=%q CNS/Cilium=Ready\n",
		node.Name, node.Labels[karpv1.NodePoolLabelKey], nodeClass.Name, node.Labels["kubernetes.azure.com/node-image-version"],
		machineID, driftAction, driftReason)
}

func nodeClassForACLEvidence(ctx context.Context, kubeClient client.Client, node *corev1.Node) (*v1beta1.AKSNodeClass, error) {
	var ref *karpv1.NodeClassReference
	if poolName := node.Labels[karpv1.NodePoolLabelKey]; poolName != "" {
		pool := &karpv1.NodePool{}
		if err := kubeClient.Get(ctx, client.ObjectKey{Name: poolName}, pool); err != nil {
			return nil, err
		}
		ref = pool.Spec.Template.Spec.NodeClassRef
	} else {
		// Standalone NodeClaims legitimately have no NodePool. Follow the
		// real Node owner and verify its identity instead of skipping proof.
		for _, owner := range node.OwnerReferences {
			if owner.APIVersion != "karpenter.sh/v1" || owner.Kind != "NodeClaim" {
				continue
			}
			claim := &karpv1.NodeClaim{}
			if err := kubeClient.Get(ctx, client.ObjectKey{Name: owner.Name}, claim); err != nil {
				return nil, err
			}
			if owner.UID == "" || claim.UID != owner.UID || claim.Status.NodeName != node.Name ||
				node.Spec.ProviderID == "" || claim.Status.ProviderID != node.Spec.ProviderID {
				return nil, fmt.Errorf("node %s does not match its NodeClaim owner %s", node.Name, owner.Name)
			}
			if ref != nil {
				return nil, fmt.Errorf("node %s has multiple NodeClaim owners", node.Name)
			}
			ref = claim.Spec.NodeClassRef
		}
	}
	if ref == nil || ref.Group != v1beta1.Group || ref.Kind != v1beta1.AKSNodeClassKind || ref.Name == "" {
		return nil, fmt.Errorf("node %s lacks an unambiguous AKSNodeClass reference", node.Name)
	}
	nodeClass := &v1beta1.AKSNodeClass{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: ref.Name}, nodeClass); err != nil {
		return nil, err
	}
	return nodeClass, nil
}

func validateACLNodeIdentity(node *corev1.Node) error {
	if node.Labels[v1beta1.AKSLabelOSSKU] != v1beta1.OSSKUAzureContainerLinux {
		return fmt.Errorf("ACL request produced node %s with OS %q", node.Name, node.Labels[v1beta1.AKSLabelOSSKU])
	}
	if !strings.Contains(strings.ToLower(node.Labels["kubernetes.azure.com/node-image-version"]), "aclgen2") {
		return fmt.Errorf("ACL node %s has unexpected image %q", node.Name, node.Labels["kubernetes.azure.com/node-image-version"])
	}
	if !strings.Contains(strings.ToLower(node.Annotations[v1beta1.AnnotationAKSMachineResourceID]), "/agentpools/aksmanagedap/machines/") {
		return fmt.Errorf("ACL node %s lacks a managed Machine resource annotation", node.Name)
	}
	return nil
}

func validateACLNodeVersion(node *corev1.Node, expected string) error {
	actual := strings.TrimPrefix(node.Status.NodeInfo.KubeletVersion, "v")
	if expected == "" || actual != expected {
		return fmt.Errorf("ACL node %s kubelet version %q differs from NodeClass version %q; lifecycle drift precondition is invalid", node.Name, actual, expected)
	}
	return nil
}
