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

package capacityrecommendation_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armrecommender "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armrecommender"
	. "github.com/onsi/gomega"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/Azure/karpenter-provider-azure/pkg/fake"
	"github.com/Azure/karpenter-provider-azure/pkg/providers/capacityrecommendation"
)

func TestGetRankingReturnsRecommendation(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	client.PostBehavior.Output.Set(recommendationResponse(time.Now().Add(time.Minute), 9, "Standard_D4s_v5", "2"))
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	ranking, err := provider.GetRanking(
		context.Background(),
		&capacityrecommendation.RankingInput{
			VMSizes:      []string{"Standard_D2s_v5", "Standard_D4s_v5"},
			Zones:        []string{"1", "2"},
			CapacityType: karpv1.CapacityTypeOnDemand,
			OSType:       corev1.Linux,
			Count:        5,
		})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ranking).To(Equal([]capacityrecommendation.RankedVMSize{{Name: "Standard_D4s_v5", Score: 9, Zone: "2"}}))
	g.Expect(client.PostBehavior.Calls()).To(Equal(1))

	input := client.PostBehavior.CalledWithInput.Pop()
	g.Expect(input.Location).To(Equal("eastus"))
	g.Expect(*input.Request.CapacityProfile.Capacity).To(Equal(int32(5)))
	g.Expect(*input.Request.CapacityProfile.CapacityType).To(Equal(armrecommender.SKUMixPlacementCapacityTypeVM))
	g.Expect(*input.Request.CapacityProfile.Priority).To(Equal(armrecommender.SKUMixPlacementPriorityRegular))
	g.Expect(*input.Request.CapacityProfile.AllocationStrategy).To(Equal(armrecommender.SKUMixPlacementAllocationStrategyPrioritized))
	g.Expect(*input.Request.CapacityProfile.OSType).To(Equal(armrecommender.SKUMixPlacementOSTypeLinux))
	g.Expect(input.Request.Zones).To(ConsistOf(to.Ptr("1"), to.Ptr("2")))
	g.Expect(input.Request.InstanceDescription.VMSizes).To(HaveLen(2))
	g.Expect(*input.Request.InstanceDescription.VMSizes[0].Name).To(Equal("Standard_D2s_v5"))
	g.Expect(*input.Request.InstanceDescription.VMSizes[0].Rank).To(Equal(int32(0)))
}

func TestGetRankingCachesByCapacityTypeZonesAndVMSizes(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	client.PostBehavior.Output.Set(recommendationResponse(time.Now().Add(time.Minute), 8, "Standard_D2s_v5", "1"))
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	first, err := provider.GetRanking(
		context.Background(),
		&capacityrecommendation.RankingInput{
			VMSizes:      []string{"Standard_D2s_v5", "Standard_D4s_v5"},
			Zones:        []string{"1", "2"},
			CapacityType: karpv1.CapacityTypeOnDemand,
			OSType:       corev1.Linux,
			Count:        5,
		})
	g.Expect(err).NotTo(HaveOccurred())
	second, err := provider.GetRanking(
		context.Background(),
		&capacityrecommendation.RankingInput{
			VMSizes:      []string{"Standard_D4s_v5", "Standard_D2s_v5"},
			Zones:        []string{"2", "1"},
			CapacityType: karpv1.CapacityTypeOnDemand,
			OSType:       corev1.Linux,
			Count:        1,
		})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(second).To(Equal(first))
	g.Expect(client.PostBehavior.Calls()).To(Equal(1))
}

func TestGetRankingCacheKeyIncludesOSType(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	client.PostBehavior.Output.Set(recommendationResponse(time.Now().Add(time.Minute), 9, "Standard_D2s_v5", "1"))
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	linuxInput := validInput()
	_, err := provider.GetRanking(context.Background(), linuxInput)
	g.Expect(err).NotTo(HaveOccurred())

	windowsInput := validInput()
	windowsInput.OSType = corev1.Windows
	_, err = provider.GetRanking(context.Background(), windowsInput)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(client.PostBehavior.Calls()).To(Equal(2))
}

func TestGetRankingReturnsAPIError(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	client.PostBehavior.Error.Set(errors.New("recommendation API unavailable"))
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	ranking, err := provider.GetRanking(context.Background(), validInput())
	g.Expect(err).To(MatchError("recommendation API unavailable"))
	g.Expect(ranking).To(BeNil())
	g.Expect(client.PostBehavior.Calls()).To(Equal(1))
}

func TestGetRankingReturnsInvalidInputError(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	ranking, err := provider.GetRanking(
		context.Background(),
		&capacityrecommendation.RankingInput{
			CapacityType: karpv1.CapacityTypeOnDemand,
			Count:        5,
		})
	g.Expect(ranking).To(BeNil())
	g.Expect(err).To(MatchError(ContainSubstring("no VM sizes specified")))
	g.Expect(client.PostBehavior.Calls()).To(Equal(0))
}

func TestGetRankingReturnsInvalidResponseError(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	client.PostBehavior.Output.Set(&armrecommender.SKUMixPlacementScoresClientPostResponse{})
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	ranking, err := provider.GetRanking(context.Background(), validInput())
	g.Expect(err).To(MatchError(ContainSubstring("no placement choices")))
	g.Expect(ranking).To(BeNil())
}

func TestGetRankingHonorsValidUntil(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	cache := newCache()
	validUntil := time.Now().Add(time.Minute)
	client.PostBehavior.Output.Set(recommendationResponse(validUntil, 9, "Standard_D2s_v5", "1"))
	provider := capacityrecommendation.NewProvider(client, cache, "eastus")

	_, err := provider.GetRanking(context.Background(), validInput())
	g.Expect(err).NotTo(HaveOccurred())

	items := cache.Items()
	g.Expect(items).To(HaveLen(1))
	for _, item := range items {
		g.Expect(time.Unix(0, item.Expiration)).To(BeTemporally("~", validUntil, time.Millisecond))
	}
}

func TestGetRankingDeduplicatesConcurrentRequests(t *testing.T) {
	g := NewWithT(t)
	client := &fake.SKUMixPlacementScoresAPI{}
	client.PostBehavior.Output.Set(recommendationResponse(time.Now().Add(time.Minute), 9, "Standard_D2s_v5", "1"))
	started := make(chan struct{})
	release := make(chan struct{})
	client.PostBehavior.SetCustomTransformer(func(*fake.SKUMixPlacementScoresPostInput) error {
		close(started)
		<-release
		return nil
	})
	provider := capacityrecommendation.NewProvider(client, newCache(), "eastus")

	var wg sync.WaitGroup
	type result struct {
		ranking []capacityrecommendation.RankedVMSize
		err     error
	}
	results := make(chan result, 6)
	request := func() {
		defer wg.Done()
		ranking, err := provider.GetRanking(context.Background(), validInput())
		results <- result{ranking: ranking, err: err}
	}

	wg.Add(1)
	go request()
	<-started
	for range 5 {
		wg.Add(1)
		go request()
	}
	close(release)
	wg.Wait()
	close(results)

	for result := range results {
		g.Expect(result.err).NotTo(HaveOccurred())
		g.Expect(result.ranking).To(HaveLen(1))
	}
	g.Expect(client.PostBehavior.Calls()).To(Equal(1))
}

func validInput() *capacityrecommendation.RankingInput {
	return &capacityrecommendation.RankingInput{
		VMSizes:      []string{"Standard_D2s_v5"},
		Zones:        []string{"1"},
		CapacityType: karpv1.CapacityTypeOnDemand,
		OSType:       corev1.Linux,
		Count:        5,
	}
}

func newCache() *cache.Cache {
	return cache.New(cache.NoExpiration, time.Minute)
}

func recommendationResponse(validUntil time.Time, score int32, name string, zone string) *armrecommender.SKUMixPlacementScoresClientPostResponse {
	return &armrecommender.SKUMixPlacementScoresClientPostResponse{
		SKUMixPlacementResponse: armrecommender.SKUMixPlacementResponse{
			ValidUntil: to.Ptr(validUntil),
			PlacementChoices: []*armrecommender.SKUMixPlacementDeploymentChoice{
				{
					Score: to.Ptr(score),
					SKUSplit: []*armrecommender.SKUMixPlacementItem{
						{
							Name: to.Ptr(name),
							Zone: to.Ptr(zone),
						},
					},
				},
			},
		},
	}
}
