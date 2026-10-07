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
	"testing"

	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/Azure/karpenter-provider-azure/pkg/controllers/nodeclass/hash"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestHashMigrationContract(t *testing.T) {
	if err := v1beta1.SchemeBuilder.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"", "NodeClassDrift", "ClusterConfigDrift"} {
		t.Run("existing-drift-"+reason, func(t *testing.T) {
			nodeClass := &v1beta1.AKSNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "class", Annotations: map[string]string{
					v1beta1.AnnotationAKSNodeClassHash:        "test-hash-1",
					v1beta1.AnnotationAKSNodeClassHashVersion: "old-version",
				}},
				Spec: v1beta1.AKSNodeClassSpec{
					ImageFamily:  lo.ToPtr(v1beta1.AzureContainerLinuxImageFamily),
					MaxPods:      lo.ToPtr[int32](10),
					OSDiskSizeGB: lo.ToPtr[int32](128),
				},
			}
			claim := &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim", Annotations: map[string]string{
					v1beta1.AnnotationAKSNodeClassHash:        "test-hash-2",
					v1beta1.AnnotationAKSNodeClassHashVersion: "old-version",
				}},
				Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Group: v1beta1.Group, Kind: v1beta1.AKSNodeClassKind, Name: nodeClass.Name}},
			}
			if reason != "" {
				claim.StatusConditions().SetTrueWithReason(karpv1.ConditionTypeDrifted, reason, reason)
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(nodeClass, claim).
				WithIndex(&karpv1.NodeClaim{}, "spec.nodeClassRef.name", func(obj client.Object) []string {
					return []string{obj.(*karpv1.NodeClaim).Spec.NodeClassRef.Name}
				}).Build()
			controller := hash.NewController(kubeClient)
			if _, err := controller.Reconcile(t.Context(), nodeClass); err != nil {
				t.Fatal(err)
			}
			current := &karpv1.NodeClaim{}
			if err := kubeClient.Get(t.Context(), client.ObjectKey{Name: claim.Name}, current); err != nil {
				t.Fatal(err)
			}
			expectedHash := nodeClass.Hash()
			if reason != "" {
				expectedHash = "test-hash-2"
			}
			if current.Annotations[v1beta1.AnnotationAKSNodeClassHash] != expectedHash ||
				current.Annotations[v1beta1.AnnotationAKSNodeClassHashVersion] != v1beta1.AKSNodeClassHashVersion {
				t.Fatalf("unexpected migration result: %v; expected hash %s", current.Annotations, expectedHash)
			}
		})
	}
}
