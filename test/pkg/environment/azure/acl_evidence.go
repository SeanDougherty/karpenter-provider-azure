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
	"fmt"
	"strings"

	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	pool := &karpv1.NodePool{}
	Expect(env.Client.Get(env, client.ObjectKey{Name: node.Labels[karpv1.NodePoolLabelKey]}, pool)).To(Succeed())
	nodeClass := &v1beta1.AKSNodeClass{}
	Expect(env.Client.Get(env, client.ObjectKey{Name: pool.Spec.Template.Spec.NodeClassRef.Name}, nodeClass)).To(Succeed())
	// Explicit other-OS test cases remain compatibility controls.
	if lo.FromPtr(nodeClass.Spec.ImageFamily) != v1beta1.AzureContainerLinuxImageFamily {
		return
	}
	Expect(validateACLNodeIdentity(node)).To(Succeed())
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
	fmt.Fprintf(GinkgoWriter, "ACL lifecycle evidence: node=%s pool=%s nodeClass=%s image=%s machine=%s CNS/Cilium=Ready\n",
		node.Name, pool.Name, nodeClass.Name, node.Labels["kubernetes.azure.com/node-image-version"],
		node.Annotations[v1beta1.AnnotationAKSMachineResourceID])
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
