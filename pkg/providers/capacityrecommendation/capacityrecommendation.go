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

package capacityrecommendation

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armrecommender"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	// defaultCacheTTL is the default cache duration if the recommendation response does not include a validUntil value.
	defaultCacheTTL = 60 * time.Second
	requestTimeout  = 5 * time.Second
)

type SKUMixPlacementScoresAPI interface {
	Post(
		ctx context.Context,
		location string,
		skuMixPlacementRequest armrecommender.SKUMixPlacementRequest,
		options *armrecommender.SKUMixPlacementScoresClientPostOptions,
	) (armrecommender.SKUMixPlacementScoresClientPostResponse, error)
}

// Provider supplies capacity-aware VM size rankings.
type Provider interface {
	GetRanking(ctx context.Context, input *RankingInput) ([]RankedVMSize, error)
}

// RankingInput identifies an allocation request to rank.
type RankingInput struct {
	VMSizes      []string
	Zones        []string
	CapacityType string
	OSType       corev1.OSName
	Count        int32
}

// RankedVMSize is an API-recommended VM size ordered by placement score.
type RankedVMSize struct {
	Name  string
	Score int
	Zone  string
}

// DefaultProvider obtains and reactively caches SKU Mix Placement rankings.
type DefaultProvider struct {
	client     SKUMixPlacementScoresAPI
	cache      *cache.Cache
	location   string
	defaultTTL time.Duration
	sfGroup    singleflight.Group
}

var _ Provider = &DefaultProvider{}

// NewProvider creates a capacity recommendation provider using the supplied
// per-response expiry cache.
func NewProvider(client SKUMixPlacementScoresAPI, cache *cache.Cache, location string) *DefaultProvider {
	return &DefaultProvider{
		client:     client,
		cache:      cache,
		location:   location,
		defaultTTL: defaultCacheTTL,
	}
}

// GetRanking returns a cached or freshly generated recommendation.
func (p *DefaultProvider) GetRanking(ctx context.Context, input *RankingInput) ([]RankedVMSize, error) {
	if err := validateInput(input); err != nil {
		return nil, fmt.Errorf("invalid SKU Mix Placement recommendation input: %w", err)
	}

	key, err := cacheKey(input)
	if err != nil {
		return nil, fmt.Errorf("hashing SKU Mix Placement recommendation input: %w", err)
	}
	if result, ok := p.getCached(key); ok {
		return result, nil
	}
	value, err, _ := p.sfGroup.Do(key, func() (any, error) {
		// check the cache again in case a different caller in sfGroup already fetched and cached the result
		if result, ok := p.getCached(key); ok {
			return result, nil
		}
		return p.fetchAndCache(ctx, key, input)
	})
	if err != nil {
		return nil, err
	}

	result, ok := value.([]RankedVMSize)
	if !ok {
		return nil, fmt.Errorf("unexpected recommendation result type %T", value)
	}
	return cloneRankedVMSizes(result), nil
}

func (p *DefaultProvider) fetchAndCache(ctx context.Context, key string, input *RankingInput) ([]RankedVMSize, error) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	response, err := p.client.Post(requestCtx, p.location, toSKUMixPlacementRequest(input), nil)
	if err != nil {
		return nil, err
	}
	result, err := parseResponse(response)
	if err != nil {
		return nil, err
	}

	validUntil := time.Now().Add(p.defaultTTL)
	if response.ValidUntil != nil {
		validUntil = *response.ValidUntil
	}
	ttl := time.Until(validUntil)
	if ttl <= 0 {
		return nil, fmt.Errorf("SKU Mix Placement recommendation expired at %s", validUntil.Format(time.RFC3339))
	}

	p.cache.Set(key, result, ttl)
	return cloneRankedVMSizes(result), nil
}

func (p *DefaultProvider) getCached(key string) ([]RankedVMSize, bool) {
	value, ok := p.cache.Get(key)
	if !ok {
		return nil, false
	}
	result, ok := value.([]RankedVMSize)
	if !ok {
		return nil, false
	}
	return cloneRankedVMSizes(result), true
}

func toSKUMixPlacementRequest(input *RankingInput) armrecommender.SKUMixPlacementRequest {
	priority := armrecommender.SKUMixPlacementPriorityRegular
	if input.CapacityType == karpv1.CapacityTypeSpot {
		priority = armrecommender.SKUMixPlacementPrioritySpot
	}
	var osType armrecommender.SKUMixPlacementOSType
	switch input.OSType {
	case corev1.Linux:
		osType = armrecommender.SKUMixPlacementOSTypeLinux
	case corev1.Windows:
		osType = armrecommender.SKUMixPlacementOSTypeWindows
	}
	vmSizes := make([]*armrecommender.SKUMixPlacementVMSize, 0, len(input.VMSizes))
	for rank, name := range input.VMSizes {
		vmSizes = append(vmSizes, &armrecommender.SKUMixPlacementVMSize{
			Name: to.Ptr(name),
			Rank: to.Ptr(int32(rank)),
		})
	}
	zones := make([]*string, 0, len(input.Zones))
	for _, zone := range input.Zones {
		zones = append(zones, to.Ptr(zone))
	}
	return armrecommender.SKUMixPlacementRequest{
		Zones: zones,
		CapacityProfile: &armrecommender.SKUMixPlacementCapacityProfile{
			Capacity:           to.Ptr(input.Count),
			CapacityType:       to.Ptr(armrecommender.SKUMixPlacementCapacityTypeVM),
			Priority:           to.Ptr(priority),
			AllocationStrategy: to.Ptr(armrecommender.SKUMixPlacementAllocationStrategyPrioritized),
			OSType:             to.Ptr(osType),
		},
		InstanceDescription: &armrecommender.SKUMixPlacementInstanceDescription{
			VMSizes: vmSizes,
		},
	}
}

func parseResponse(response armrecommender.SKUMixPlacementScoresClientPostResponse) ([]RankedVMSize, error) {
	if len(response.PlacementChoices) == 0 {
		return nil, fmt.Errorf("SKU Mix Placement response contained no placement choices")
	}

	choices := append([]*armrecommender.SKUMixPlacementDeploymentChoice(nil), response.PlacementChoices...)
	// TODO: this is possibly overkill since the service is supposed to return them in order, but guarding defensively for now...
	sort.SliceStable(choices, func(i, j int) bool {
		return choiceScore(choices[i]) > choiceScore(choices[j])
	})

	result := make([]RankedVMSize, 0)
	for _, choice := range choices {
		if choice == nil || choice.Score == nil || choice.SKUSplit == nil {
			return nil, fmt.Errorf("SKU Mix Placement response contained an invalid placement choice")
		}
		for _, split := range choice.SKUSplit {
			if split == nil || split.Name == nil {
				return nil, fmt.Errorf("SKU Mix Placement response contained an invalid SKU split")
			}
			result = append(
				result,
				RankedVMSize{
					Name:  *split.Name,
					Score: int(lo.FromPtr(choice.Score)),
					Zone:  lo.FromPtr(split.Zone),
				})
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("SKU Mix Placement response contained no recommended VM sizes")
	}
	return result, nil
}

func choiceScore(choice *armrecommender.SKUMixPlacementDeploymentChoice) int32 {
	if choice == nil {
		return 0
	}
	return lo.FromPtr(choice.Score)
}

func validateInput(input *RankingInput) error {
	if input == nil {
		return fmt.Errorf("input is nil")
	}
	if len(input.VMSizes) == 0 {
		return fmt.Errorf("no VM sizes specified")
	}
	if input.Count <= 0 {
		return fmt.Errorf("count %d is outside the supported range", input.Count)
	}
	switch input.CapacityType {
	case karpv1.CapacityTypeOnDemand, karpv1.CapacityTypeSpot:
	default:
		return fmt.Errorf("unsupported capacity type %q", input.CapacityType)
	}
	switch input.OSType {
	case corev1.Linux, corev1.Windows:
	default:
		return fmt.Errorf("unsupported OS type %q", input.OSType)
	}
	return nil
}

func cacheKey(input *RankingInput) (string, error) {
	value, err := hashstructure.Hash(struct {
		CapacityType string
		OSType       corev1.OSName
		VMSizes      []string
		Zones        []string
	}{
		CapacityType: input.CapacityType,
		OSType:       input.OSType,
		VMSizes:      input.VMSizes,
		Zones:        input.Zones,
	}, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", value), nil
}

func cloneRankedVMSizes(in []RankedVMSize) []RankedVMSize {
	return append([]RankedVMSize(nil), in...)
}
