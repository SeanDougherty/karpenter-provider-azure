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

package nodeclaim_test

import (
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/test"
	"strconv"
	"testing"
)

func ephemeralFixtureDiskSize(nodeClass *v1beta1.AKSNodeClass) int32 {
	if lo.FromPtr(nodeClass.Spec.ImageFamily) == v1beta1.AzureContainerLinuxImageFamily {
		return 64
	}
	return 50
}

func TestEphemeralFixtureDiskSize(t *testing.T) {
	for _, tc := range []struct {
		family string
		size   int32
	}{
		{v1beta1.AzureContainerLinuxImageFamily, 64},
		{v1beta1.Ubuntu2204ImageFamily, 50},
		{v1beta1.AzureLinuxImageFamily, 50},
	} {
		nc := &v1beta1.AKSNodeClass{}
		nc.Spec.ImageFamily = &tc.family
		if got := ephemeralFixtureDiskSize(nc); got != tc.size {
			t.Fatalf("%s: got disk size %d, want %d", tc.family, got, tc.size)
		}
	}
}

var _ = Describe("Ephemeral OS Disk", func() {
	It("should use a node with an ephemeral os disk", func() {
		diskSize := ephemeralFixtureDiskSize(nodeClass)
		test.ReplaceRequirements(nodePool, karpv1.NodeSelectorRequirementWithMinValues{
			Key:      v1beta1.LabelSKUStorageEphemeralOSMaxSize,
			Operator: corev1.NodeSelectorOpGt,
			// NOTE: this is the size of our nodeclass OSDiskSizeGB.
			// If the size of the ephemeral disk requested is lower than AKSNodeClass OSDiskGB
			// we fallback to managed disks, honoring OSDiskSizeGB
			Values: []string{strconv.Itoa(int(diskSize))},
		})

		deployment := test.Deployment(test.DeploymentOptions{Replicas: 1})
		nodeClass.Spec.OSDiskSizeGB = &diskSize
		env.ExpectCreated(nodeClass, nodePool, deployment)
		pods := env.EventuallyExpectHealthyDeployment(deployment)
		env.ExpectCreatedNodeCount("==", 1)

		vm := env.GetVM(pods[0].Spec.NodeName)
		Expect(vm.Properties.StorageProfile.OSDisk).ToNot(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings).ToNot(BeNil())
		// We should be specifying os disk placement now
		Expect(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings.Placement).ToNot(BeNil())
		Expect(string(lo.FromPtr(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings.Option))).To(Equal("Local"))
	})

	It("should provision VM with SKU that does not support ephemeral OS disk", func() {
		test.ReplaceRequirements(nodePool, karpv1.NodeSelectorRequirementWithMinValues{
			Key:      v1beta1.LabelSKUStorageEphemeralOSMaxSize,
			Operator: corev1.NodeSelectorOpDoesNotExist,
		})

		deployment := test.Deployment(test.DeploymentOptions{Replicas: 1})
		env.ExpectCreated(nodeClass, nodePool, deployment)
		pods := env.EventuallyExpectHealthyDeployment(deployment)
		env.ExpectCreatedNodeCount("==", 1)
		vm := env.GetVM(pods[0].Spec.NodeName)
		Expect(vm.Properties.StorageProfile.OSDisk).ToNot(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings).To(BeNil())
	})

	It("should provision VM with SKU that does not support ephemeral OS disk, even if OS disk fits on cache disk", func() {
		test.ReplaceRequirements(nodePool,
			karpv1.NodeSelectorRequirementWithMinValues{
				Key:      corev1.LabelArchStable,
				Operator: corev1.NodeSelectorOpExists, // relax to allow arm
			},
			karpv1.NodeSelectorRequirementWithMinValues{
				Key:      corev1.LabelInstanceTypeStable,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"Standard_D2pls_v5"}, // 53GB cache disk, does not support ephemeral OS disk
			},
		)

		nodeClass.Spec.OSDiskSizeGB = lo.ToPtr[int32](40) // < 53GB cache disk
		if lo.FromPtr(nodeClass.Spec.ImageFamily) == v1beta1.AzureContainerLinuxImageFamily {
			// This 53-GiB cache boundary cannot hold ACL's minimum disk.
			// Retain the owning small-cache test as an explicit OS control.
			nodeClass.Spec.ImageFamily = lo.ToPtr(v1beta1.Ubuntu2204ImageFamily)
		}

		deployment := test.Deployment(test.DeploymentOptions{Replicas: 1})
		env.ExpectCreated(nodeClass, nodePool, deployment)
		pods := env.EventuallyExpectHealthyDeployment(deployment)
		env.ExpectCreatedNodeCount("==", 1)
		vm := env.GetVM(pods[0].Spec.NodeName)
		Expect(vm.Properties.StorageProfile.OSDisk).ToNot(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings).To(BeNil())
	})

	It("should use a managed OS disk when osDiskType is Managed, even on a SKU that supports ephemeral", func() {
		diskSize := ephemeralFixtureDiskSize(nodeClass)
		test.ReplaceRequirements(nodePool, karpv1.NodeSelectorRequirementWithMinValues{
			Key:      v1beta1.LabelSKUStorageEphemeralOSMaxSize,
			Operator: corev1.NodeSelectorOpGt,
			Values:   []string{strconv.Itoa(int(diskSize))},
		})

		deployment := test.Deployment(test.DeploymentOptions{Replicas: 1})
		nodeClass.Spec.OSDiskType = lo.ToPtr(v1beta1.OSDiskTypeManaged)
		nodeClass.Spec.OSDiskSizeGB = &diskSize
		env.ExpectCreated(nodeClass, nodePool, deployment)
		pods := env.EventuallyExpectHealthyDeployment(deployment)
		env.ExpectCreatedNodeCount("==", 1)

		vm := env.GetVM(pods[0].Spec.NodeName)
		Expect(vm.Properties.StorageProfile.OSDisk).ToNot(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings).To(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.ManagedDisk).ToNot(BeNil())
	})

	It("should fall back to a managed OS disk on a SKU whose ephemeral disk is smaller than osDiskSizeGB", func() {
		test.ReplaceRequirements(nodePool, karpv1.NodeSelectorRequirementWithMinValues{
			Key:      v1beta1.LabelSKUStorageEphemeralOSMaxSize,
			Operator: corev1.NodeSelectorOpLt,
			Values:   []string{"128"},
		})

		deployment := test.Deployment(test.DeploymentOptions{Replicas: 1})
		nodeClass.Spec.OSDiskSizeGB = lo.ToPtr[int32](128)
		env.ExpectCreated(nodeClass, nodePool, deployment)
		pods := env.EventuallyExpectHealthyDeployment(deployment)
		env.ExpectCreatedNodeCount("==", 1)

		vm := env.GetVM(pods[0].Spec.NodeName)
		Expect(vm.Properties.StorageProfile.OSDisk).ToNot(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.DiffDiskSettings).To(BeNil())
		Expect(vm.Properties.StorageProfile.OSDisk.ManagedDisk).ToNot(BeNil())
	})
})
