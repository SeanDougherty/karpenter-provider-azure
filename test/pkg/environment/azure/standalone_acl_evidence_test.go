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
	"testing"

	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestStandaloneACLNodeClassEvidence(t *testing.T) {
	for _, mutate := range []struct {
		name   string
		change func(*corev1.Node, *karpv1.NodeClaim)
		valid  bool
	}{
		{"standalone", func(*corev1.Node, *karpv1.NodeClaim) {}, true},
		{"missing owner", func(n *corev1.Node, _ *karpv1.NodeClaim) { n.OwnerReferences = nil }, false},
		{"wrong UID", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.UID = "different" }, false},
		{"wrong node", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.Status.NodeName = "different" }, false},
		{"wrong provider", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.Status.ProviderID = "different" }, false},
		{"missing reference", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.Spec.NodeClassRef = nil }, false},
		{"wrong kind", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.Spec.NodeClassRef.Kind = "Wrong" }, false},
		{"duplicate owner", func(n *corev1.Node, _ *karpv1.NodeClaim) {
			n.OwnerReferences = append(n.OwnerReferences, n.OwnerReferences[0])
		}, false},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
			require.NoError(t, v1beta1.SchemeBuilder.AddToScheme(scheme))
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node", OwnerReferences: []metav1.OwnerReference{
					{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "claim", UID: "claim-uid"},
				}},
				Spec: corev1.NodeSpec{ProviderID: "azure:///subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm"},
			}
			claim := &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: "claim-uid"},
				Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{
					Group: v1beta1.Group, Kind: v1beta1.AKSNodeClassKind, Name: "class",
				}},
				Status: karpv1.NodeClaimStatus{NodeName: node.Name, ProviderID: node.Spec.ProviderID},
			}
			nodeClass := &v1beta1.AKSNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class"}}
			mutate.change(node, claim)
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim, nodeClass).Build()
			actual, err := nodeClassForACLEvidence(context.Background(), kubeClient, node)
			if mutate.valid {
				require.NoError(t, err)
				require.Equal(t, nodeClass.Name, actual.Name)
			} else {
				require.Error(t, err)
			}
		})
	}
}
