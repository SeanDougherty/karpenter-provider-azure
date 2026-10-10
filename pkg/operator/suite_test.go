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

package operator

import (
	"context"
	"maps"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	autoscalingv1beta1 "sigs.k8s.io/karpenter/pkg/apis/autoscaling/v1beta1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpv1alpha1 "sigs.k8s.io/karpenter/pkg/apis/v1alpha1"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/karpenter-provider-azure/pkg/apis"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/Azure/karpenter-provider-azure/pkg/auth"
)

var (
	ctx context.Context
	env *coretest.Environment
)

func TestOperator(t *testing.T) {
	ctx = TestContextWithLogger(t)

	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator")
}

func TestPricingEnvironmentIsolation(t *testing.T) {
	config := cloud.AzurePublic
	config.Services = maps.Clone(config.Services)
	resourceManager := config.Services[cloud.ResourceManager]
	resourceManager.Endpoint = "https://private-arm.example/"
	config.Services[cloud.ResourceManager] = resourceManager
	environment := &auth.Environment{Cloud: config}

	if pricingEnvironment(environment, "") != environment || auth.IsPublic(environment.Cloud) {
		t.Fatal("default pricing must retain the resource cloud")
	}
	pricing := pricingEnvironment(environment, "AzurePublicCloud")
	if pricing == environment || !auth.IsPublic(pricing.Cloud) {
		t.Fatal("explicit public pricing must use an independent environment")
	}
	if environment.Cloud.Services[cloud.ResourceManager].Endpoint != resourceManager.Endpoint ||
		auth.IsPublic(environment.Cloud) {
		t.Fatal("pricing override must not reroute resource operations or mutate their environment")
	}
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...))
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = Describe("getRequiredGVKs", func() {
	It("should not require CapacityBuffer when its feature gate is disabled", func() {
		disabledCtx := coreoptions.ToContext(ctx, coretest.Options())
		gvks := getRequiredGVKs(disabledCtx)
		Expect(gvks).To(HaveLen(4))
		Expect(gvks).To(ContainElement(object.GVK(&karpv1.NodePool{})))
		Expect(gvks).To(ContainElement(object.GVK(&karpv1.NodeClaim{})))
		Expect(gvks).To(ContainElement(object.GVK(&karpv1alpha1.NodeOverlay{})))
		Expect(gvks).To(ContainElement(object.GVK(&v1beta1.AKSNodeClass{})))
		Expect(gvks).ToNot(ContainElement(object.GVK(&autoscalingv1beta1.CapacityBuffer{})))
	})

	It("should require CapacityBuffer v1beta1 when its feature gate is enabled", func() {
		enabledCtx := coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{
			FeatureGates: coretest.FeatureGates{CapacityBuffer: lo.ToPtr(true)},
		}))
		gvks := getRequiredGVKs(enabledCtx)
		Expect(gvks).To(HaveLen(5))
		Expect(gvks).To(ContainElement(object.GVK(&autoscalingv1beta1.CapacityBuffer{})))
	})
})
