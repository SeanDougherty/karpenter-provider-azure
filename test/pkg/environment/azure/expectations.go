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
	"os"
	"slices"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	containerservice "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v9"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/Azure/karpenter-provider-azure/pkg/auth"
	"github.com/Azure/karpenter-provider-azure/pkg/consts"
	"github.com/Azure/karpenter-provider-azure/pkg/providers/instance"
)

func (env *Environment) EventuallyExpectKarpenterNicsToBeDeleted() {
	GinkgoHelper()
	Eventually(func() bool {
		pager := env.interfacesClient.NewListPager(env.NodeResourceGroup, nil)
		for pager.More() {
			resp, err := pager.NextPage(env.Context)
			if err != nil {
				return false
			}

			for _, nic := range resp.Value {
				if nic.Tags != nil {
					if _, exists := nic.Tags[strings.ReplaceAll(karpv1.NodePoolLabelKey, "/", "_")]; exists {
						return false
					}
				}
			}
		}
		return true
	}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "Expected all orphan NICs to be deleted")
}

func (env *Environment) ExpectCreatedInterface(networkInterface armnetwork.Interface) {
	GinkgoHelper()
	poller, err := env.interfacesClient.BeginCreateOrUpdate(env.Context, env.NodeResourceGroup, lo.FromPtr(networkInterface.Name), networkInterface, nil)
	Expect(err).ToNot(HaveOccurred())
	resp, err := poller.PollUntilDone(env.Context, nil)
	Expect(err).ToNot(HaveOccurred())
	env.tracker.Add(lo.FromPtr(resp.ID), func() error {
		deletePoller, err := env.interfacesClient.BeginDelete(env.Context, env.NodeResourceGroup, lo.FromPtr(networkInterface.Name), nil)
		if err != nil {
			return fmt.Errorf("failed to delete network interface %s: %w", lo.FromPtr(networkInterface.Name), err)
		}
		_, err = deletePoller.PollUntilDone(env.Context, nil)
		if err != nil {
			return fmt.Errorf("failed to delete network interface %s: %w", lo.FromPtr(networkInterface.Name), err)
		}
		return nil
	})
}

func (env *Environment) ExpectCreatedOrphanInterface(networkInterface armnetwork.Interface) {
	GinkgoHelper()
	if env.TestImageFamily != v1beta1.AzureContainerLinuxImageFamily {
		env.ExpectCreatedInterface(networkInterface)
		return
	}
	Expect(env.createOrphanInterface(networkInterface)).To(Succeed())
}

func (env *Environment) createOrphanInterface(networkInterface armnetwork.Interface) error {
	name := lo.FromPtr(networkInterface.Name)
	if name == "" || env.SubscriptionID == "" || env.Region == "" || env.ClusterResourceGroup == "" || env.NodeResourceGroup == "" ||
		strings.EqualFold(env.ClusterResourceGroup, env.NodeResourceGroup) {
		return fmt.Errorf("orphan NIC fixture requires a named interface and separate owning cluster/node resource groups")
	}
	if networkInterface.Properties == nil || len(networkInterface.Properties.IPConfigurations) == 0 {
		return fmt.Errorf("orphan NIC fixture requires an IP configuration")
	}
	for _, config := range networkInterface.Properties.IPConfigurations {
		if config == nil || config.Properties == nil {
			return fmt.Errorf("orphan NIC fixture contains an invalid IP configuration")
		}
	}
	vnetName := name + "-vnet"
	vnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s",
		env.SubscriptionID, env.ClusterResourceGroup, vnetName)
	// The tracker runs callbacks concurrently. Keep dependent NIC/VNet cleanup
	// in one callback, registered before either create can fail.
	env.tracker.Add(vnetID, func() error {
		nicDelete, err := env.interfacesClient.BeginDelete(env.Context, env.NodeResourceGroup, name, nil)
		if err != nil {
			return fmt.Errorf("deleting orphan NIC %s: %w", name, err)
		}
		if _, err = nicDelete.PollUntilDone(env.Context, nil); err != nil {
			return fmt.Errorf("waiting for orphan NIC %s deletion: %w", name, err)
		}
		vnetDelete, err := env.vnetClient.BeginDelete(env.Context, env.ClusterResourceGroup, vnetName, nil)
		if err != nil {
			return fmt.Errorf("deleting orphan NIC test VNet %s: %w", vnetName, err)
		}
		_, err = vnetDelete.PollUntilDone(env.Context, nil)
		return err
	})
	vnetCreate, err := env.vnetClient.BeginCreateOrUpdate(env.Context, env.ClusterResourceGroup, vnetName, armnetwork.VirtualNetwork{
		Location: lo.ToPtr(env.Region),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{lo.ToPtr("10.250.0.0/24")}},
			Subnets: []*armnetwork.Subnet{{
				Name:       lo.ToPtr("orphan"),
				Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: lo.ToPtr("10.250.0.0/24")},
			}},
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("creating orphan NIC test VNet: %w", err)
	}
	if _, err = vnetCreate.PollUntilDone(env.Context, nil); err != nil {
		return fmt.Errorf("waiting for orphan NIC test VNet: %w", err)
	}
	// Leave Automatic's protected cluster subnet untouched. The orphan stays
	// in the node resource group with its original tags, as required by NIC GC.
	for _, config := range networkInterface.Properties.IPConfigurations {
		config.Properties.Subnet = &armnetwork.Subnet{ID: lo.ToPtr(vnetID + "/subnets/orphan")}
	}
	nicCreate, err := env.interfacesClient.BeginCreateOrUpdate(env.Context, env.NodeResourceGroup, name, networkInterface, nil)
	if err != nil {
		return fmt.Errorf("creating orphan NIC in the node resource group: %w", err)
	}
	_, err = nicCreate.PollUntilDone(env.Context, nil)
	return err
}

func (env *Environment) ExpectGetManagedCluster() *containerservice.ManagedCluster {
	GinkgoHelper()
	managedClusterResponse, err := env.managedClusterClient.Get(env.Context, env.ClusterResourceGroup, env.ClusterName, nil)
	Expect(err).ToNot(HaveOccurred())
	return &managedClusterResponse.ManagedCluster
}

// ExpectClusterProvisioningState checks that the cluster's provisioning state matches the expected state,
// and fails the test if it does not.
func (env *Environment) ExpectClusterProvisioningState(expectedProvisioningState string) *containerservice.ManagedCluster {
	GinkgoHelper()
	managedCluster := env.ExpectGetManagedCluster()
	Expect(managedCluster.Properties).ToNot(BeNil())
	Expect(managedCluster.Properties.ProvisioningState).ToNot(BeNil())
	Expect(*managedCluster.Properties.ProvisioningState).To(Equal(expectedProvisioningState),
		fmt.Sprintf("expected cluster provisioning state '%s', got '%s'", expectedProvisioningState, *managedCluster.Properties.ProvisioningState))
	return managedCluster
}

func (env *Environment) ExpectSuccessfulGetOfAvailableKubernetesVersionUpgradesForManagedCluster() []*containerservice.ManagedClusterPoolUpgradeProfileUpgradesItem {
	GinkgoHelper()
	upgradeProfile, err := env.managedClusterClient.GetUpgradeProfile(env.Context, env.ClusterResourceGroup, env.ClusterName, nil)
	Expect(err).ToNot(HaveOccurred())
	return upgradeProfile.Properties.ControlPlaneProfile.Upgrades
}

func (env *Environment) ExpectKubernetesUpgradeVersion() string {
	GinkgoHelper()
	upgrades := env.ExpectSuccessfulGetOfAvailableKubernetesVersionUpgradesForManagedCluster()
	cluster := env.ExpectGetManagedCluster()
	Expect(cluster.Properties).ToNot(BeNil())
	version, err := selectKubernetesUpgradeVersion(upgrades,
		lo.FromPtr(cluster.Properties.CurrentKubernetesVersion), os.Getenv("TEST_AKS_KUBERNETES_UPGRADE_TARGET"))
	Expect(err).ToNot(HaveOccurred())
	return version
}

func selectKubernetesUpgradeVersion(upgrades []*containerservice.ManagedClusterPoolUpgradeProfileUpgradesItem, current, requested string) (string, error) {
	currentVersion, err := semver.Parse(current)
	if err != nil {
		return "", fmt.Errorf("invalid current Kubernetes version %q: %w", current, err)
	}
	var selected string
	var selectedVersion semver.Version
	for _, upgrade := range upgrades {
		if upgrade == nil || upgrade.KubernetesVersion == nil {
			return "", fmt.Errorf("upgrade profile contains an entry without a Kubernetes version")
		}
		version, err := semver.Parse(*upgrade.KubernetesVersion)
		if err != nil {
			return "", fmt.Errorf("invalid advertised Kubernetes upgrade %q: %w", *upgrade.KubernetesVersion, err)
		}
		if requested != "" && *upgrade.KubernetesVersion != requested {
			continue
		}
		if selected == "" || version.GT(selectedVersion) {
			selected, selectedVersion = *upgrade.KubernetesVersion, version
		}
	}
	if selected == "" {
		return "", fmt.Errorf("no advertised Kubernetes upgrade matches target %q", requested)
	}
	if !selectedVersion.GT(currentVersion) {
		return "", fmt.Errorf("Kubernetes target %q must be newer than current version %q", selected, current)
	}
	return selected, nil
}

func (env *Environment) ExpectSuccessfulUpgradeOfManagedCluster(kubernetesUpgradeVersion string) *containerservice.ManagedCluster {
	GinkgoHelper()
	poller := env.ExpectUpgradeOfManagedCluster(kubernetesUpgradeVersion)
	resp, err := poller.PollUntilDone(env.Context, nil)
	Expect(err).ToNot(HaveOccurred())
	return &resp.ManagedCluster
}

func (env *Environment) ExpectUpgradeOfManagedCluster(kubernetesUpgradeVersion string) *runtime.Poller[containerservice.ManagedClustersClientCreateOrUpdateResponse] {
	GinkgoHelper()
	managedCluster := env.ExpectGetManagedCluster()

	// See documentation for KubernetesVersion (client specified) and CurrentKubernetesVersion (version under use):
	// https://learn.microsoft.com/en-us/rest/api/aks/managed-clusters/get?view=rest-aks-2025-01-01&tabs=HTTP
	By(fmt.Sprintf("upgrading from kubernetes version %s to kubernetes version %s", *managedCluster.Properties.CurrentKubernetesVersion, kubernetesUpgradeVersion))
	managedCluster.Properties.KubernetesVersion = &kubernetesUpgradeVersion
	// Note that this is an update not a create so we don't need to add it to the tracker
	poller, err := env.managedClusterClient.BeginCreateOrUpdate(env.Context, env.ClusterResourceGroup, env.ClusterName, *managedCluster, nil)
	Expect(err).ToNot(HaveOccurred())
	return poller
}

func (env *Environment) ExpectParsedProviderID(providerID string) string {
	GinkgoHelper()
	providerIDSplit := strings.Split(providerID, "/")
	Expect(len(providerIDSplit)).ToNot(Equal(0))
	return providerIDSplit[len(providerIDSplit)-1]
}

func (env *Environment) ExpectCreatedSubnet(vnetName string, subnet *armnetwork.Subnet) {
	GinkgoHelper()
	poller, err := env.subnetClient.BeginCreateOrUpdate(env.Context, env.VNETResourceGroup, vnetName, lo.FromPtr(subnet.Name), *subnet, nil)
	Expect(err).ToNot(HaveOccurred())
	resp, err := poller.PollUntilDone(env.Context, nil)
	Expect(err).ToNot(HaveOccurred())
	*subnet = resp.Subnet
	env.tracker.Add(lo.FromPtr(resp.ID), func() error {
		deletePoller, err := env.subnetClient.BeginDelete(env.Context, env.VNETResourceGroup, vnetName, lo.FromPtr(subnet.Name), nil)
		if err != nil {
			return fmt.Errorf("failed to delete subnet %s: %w", lo.FromPtr(subnet.Name), err)
		}
		_, err = deletePoller.PollUntilDone(env.Context, nil)
		if err != nil {
			return fmt.Errorf("failed to delete subnet %s: %w", lo.FromPtr(subnet.Name), err)
		}
		return nil
	})
}

// EventuallyExpectTags checks that all of the resources in the resource group have the expected tags.
func (env *Environment) EventuallyExpectTags(expectedTags map[string]string) {
	GinkgoHelper()

	// Convert the expectedTags to ptrs
	expectedTagsPtr := lo.MapValues(expectedTags, func(v string, _ string) *string { return &v })

	By(fmt.Sprintf("waiting for Azure resources to have tags %s", expectedTags))

	env.EventuallyExpectAzureResources(
		func(nic *armnetwork.Interface) error {
			if !isMapSubset(nic.Tags, expectedTagsPtr, eqPtr) {
				return fmt.Errorf(
					"nic tags do not match expected tags. NIC %s, expectedTags: %s, actualTags: %s",
					lo.FromPtr(nic.ID),
					expectedTags,
					lo.MapValues(nic.Tags, func(v *string, _ string) string { return lo.FromPtr(v) }),
				)
			}
			return nil
		},
		func(vm *armcompute.VirtualMachine) error {
			if !isMapSubset(vm.Tags, expectedTagsPtr, eqPtr) {
				return fmt.Errorf(
					"vm tags do not match expected tags. VM %s, expectedTags: %s, actualTags: %s",
					lo.FromPtr(vm.ID),
					expectedTags,
					lo.MapValues(vm.Tags, func(v *string, _ string) string { return lo.FromPtr(v) }),
				)
			}
			return nil
		},
		func(ext *armcompute.VirtualMachineExtension) error {
			// Check extension tags
			if !isMapSubset(ext.Tags, expectedTagsPtr, eqPtr) {
				return fmt.Errorf(
					"extension tags do not match expected tags. Extension %s, expectedTags: %s, actualTags: %s",
					lo.FromPtr(ext.ID),
					expectedTags,
					lo.MapValues(ext.Tags, func(v *string, _ string) string { return lo.FromPtr(v) }),
				)
			}
			return nil
		},
	)
}

// EventuallyExpectMissingTags checks that all of the resources in the resource group are missing the expected tags.
func (env *Environment) EventuallyExpectMissingTags(expectedMissingTags map[string]string) {
	GinkgoHelper()

	expectedMissingTagsPtr := lo.MapValues(expectedMissingTags, func(v string, _ string) *string { return &v })

	By(fmt.Sprintf("waiting for Azure resources to lose tags %s", expectedMissingTags))

	env.EventuallyExpectAzureResources(
		func(nic *armnetwork.Interface) error {
			if isMapSubset(nic.Tags, expectedMissingTagsPtr, eqPtr) {
				return fmt.Errorf(
					"nic tags are not missing expected tags. NIC %s, expectedMissingTags: %s, actualTags: %s",
					lo.FromPtr(nic.ID),
					expectedMissingTags,
					lo.MapValues(nic.Tags, func(v *string, _ string) string { return lo.FromPtr(v) }),
				)
			}
			return nil
		},
		func(vm *armcompute.VirtualMachine) error {
			if isMapSubset(vm.Tags, expectedMissingTagsPtr, eqPtr) {
				return fmt.Errorf(
					"vm tags are not missing expected tags. VM %s, expectedMissingTags: %s, actualTags: %s",
					lo.FromPtr(vm.ID),
					expectedMissingTags,
					lo.MapValues(vm.Tags, func(v *string, _ string) string { return lo.FromPtr(v) }),
				)
			}
			return nil
		},
		func(ext *armcompute.VirtualMachineExtension) error {
			// Check extension tags
			if isMapSubset(ext.Tags, expectedMissingTagsPtr, eqPtr) {
				return fmt.Errorf(
					"extension tags are not missing expected tags. Extension %s, expectedMissingTags: %s, actualTags: %s",
					lo.FromPtr(ext.ID),
					expectedMissingTags,
					lo.MapValues(ext.Tags, func(v *string, _ string) string { return lo.FromPtr(v) }),
				)
			}
			return nil
		},
	)
}

func (env *Environment) EventuallyExpectAzureResources(
	verifyNIC func(nic *armnetwork.Interface) error,
	verifyVM func(vm *armcompute.VirtualMachine) error,
	verifyExt func(ext *armcompute.VirtualMachineExtension) error,
) {
	GinkgoHelper()

	Eventually(func(g Gomega) error {
		// NICs
		pager := env.interfacesClient.NewListPager(env.NodeResourceGroup, nil)
		for pager.More() {
			resp, err := pager.NextPage(env)
			g.Expect(err).ToNot(HaveOccurred(), "failed to get next page of NICs")

			for _, nic := range resp.Value {
				if _, exists := nic.Tags[strings.ReplaceAll(karpv1.NodePoolLabelKey, "/", "_")]; !exists {
					continue // Ignore nodes that don't have the expected Karpenter tag
				}

				err := verifyNIC(nic)
				g.Expect(err).ToNot(HaveOccurred())
			}
		}

		// Note that disks also exist, but are automatically created and managed by Azure so we don't check them here.

		// VMs
		managedExtensionNames := instance.GetManagedExtensionNames(
			lo.Ternary(env.InClusterController, consts.ProvisionModeAKSScriptless, consts.ProvisionModeBootstrappingClient),
			lo.Must(auth.EnvironmentFromName("AzurePublicCloud")),
		)
		vmPager := env.vmClient.NewListPager(env.NodeResourceGroup, nil)
		for vmPager.More() {
			resp, err := vmPager.NextPage(env.Context)
			g.Expect(err).ToNot(HaveOccurred(), "failed to get next page of VMs")

			for _, vm := range resp.Value {
				if _, exists := vm.Tags[strings.ReplaceAll(karpv1.NodePoolLabelKey, "/", "_")]; !exists {
					continue // Ignore nodes that don't have the expected Karpenter tag
				}

				err := verifyVM(vm)
				g.Expect(err).ToNot(HaveOccurred())

				// Extensions
				for _, ext := range vm.Resources {
					// Only check extensions are that managed by Karpenter
					if !slices.Contains(managedExtensionNames, lo.FromPtr(ext.Name)) {
						continue
					}

					err := verifyExt(ext)
					g.Expect(err).ToNot(HaveOccurred())
				}
			}
		}

		return nil
	}).WithTimeout(2 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
}

func isMapSubset[K comparable, V comparable](m map[K]V, subset map[K]V, eq func(a, b V) bool) bool {
	for k, v := range subset {
		if val, exists := m[k]; !exists || !eq(val, v) {
			return false
		}
	}
	return true
}

func eqPtr(v1, v2 *string) bool {
	if v1 == nil && v2 == nil {
		return true
	}
	if v1 == nil || v2 == nil {
		return false
	}
	return *v1 == *v2
}
