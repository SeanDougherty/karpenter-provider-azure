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
package debug

import (
	"strings"
	"testing"

	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestNodeClaimInfoCapturesDriftTransitions(t *testing.T) {
	controller := &NodeClaimController{}
	claim := &karpv1.NodeClaim{}
	before := controller.GetInfo(claim)
	claim.StatusConditions().SetTrueWithReason(karpv1.ConditionTypeDrifted, "ClusterConfigDrift", "machine requires recreation")
	drifted := controller.GetInfo(claim)
	if before == drifted || !strings.Contains(drifted, `driftReason="ClusterConfigDrift"`) {
		t.Fatal("debug update predicate would miss drift transition")
	}
	claim.Annotations = map[string]string{v1beta1.AnnotationAKSNodeClassHash: "old-hash", v1beta1.AnnotationAKSNodeClassHashVersion: "v3"}
	hashed := controller.GetInfo(claim)
	if hashed == drifted || !strings.Contains(hashed, `nodeClassHash="old-hash"`) {
		t.Fatal("debug update predicate would miss hash transition")
	}
	now := metav1.Now()
	claim.DeletionTimestamp = &now
	deleting := controller.GetInfo(claim)
	if deleting == hashed || !strings.Contains(deleting, "deleting=true") {
		t.Fatal("debug update predicate would miss deletion transition")
	}
	claim.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("7800m")}
	claim.Finalizers = []string{"testing/finalizer"}
	capacity := controller.GetInfo(claim)
	if capacity == deleting || !strings.Contains(capacity, "allocatable=") || !strings.Contains(capacity, "testing/finalizer") {
		t.Fatal("diagnostics must capture budget fixture capacity and finalizers")
	}
}
