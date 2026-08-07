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
	"encoding/json"
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
	"sigs.k8s.io/controller-runtime/pkg/log"
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

// Provider supplies capacity-aware VM placement recommendations.
type Provider interface {
	GetRanking(ctx context.Context, input *RankingInput) (*Recommendation, error)
}

// RankingInput identifies an allocation request to rank.
type RankingInput struct {
	VMSizes      []string
	Zones        []string
	CapacityType string
	OSType       corev1.OSName
	Count        int32
}

// VMSplitItem is a VM size allocation from the selected placement choice.
type VMSplitItem struct {
	Name  string
	Count int32
	Zone  string
}

// Recommendation is the selected placement choice and its VM allocations.
// ID is preserved so callers can correlate subsequent ARM operations with the
// recommendation that informed them.
type Recommendation struct {
	ID      string
	VMSplit []VMSplitItem
}

// DefaultProvider obtains and reactively caches SKU Mix Placement recommendations.
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
func (p *DefaultProvider) GetRanking(ctx context.Context, input *RankingInput) (*Recommendation, error) {
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

	result, ok := value.(*Recommendation)
	if !ok {
		return nil, fmt.Errorf("unexpected recommendation result type %T", value)
	}
	return cloneRecommendation(result), nil
}

func (p *DefaultProvider) fetchAndCache(ctx context.Context, key string, input *RankingInput) (*Recommendation, error) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	response, err := p.client.Post(requestCtx, p.location, toSKUMixPlacementRequest(input), nil)
	if err != nil {
		return nil, err
	}

	choice, err := bestPlacementChoice(response, input)
	if err != nil {
		return nil, err
	}
	if response.PlacementChoices[0] != choice {
		log.FromContext(ctx).Error(
			fmt.Errorf("selected SKU Mix Placement choice differs from first response choice"),
			"selected non-first SKU Mix Placement choice",
			"selectedChoice", placementChoiceJSON(choice),
			"firstChoice", placementChoiceJSON(response.PlacementChoices[0]),
		)
	}
	result, err := newRecommendationFromChoice(choice)
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
	return cloneRecommendation(result), nil
}

func (p *DefaultProvider) getCached(key string) (*Recommendation, bool) {
	value, ok := p.cache.Get(key)
	if !ok {
		return nil, false
	}
	result, ok := value.(*Recommendation)
	if !ok {
		return nil, false
	}
	return cloneRecommendation(result), true
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

func newRecommendationFromChoice(choice *armrecommender.SKUMixPlacementDeploymentChoice) (*Recommendation, error) {
	if choice == nil || choice.Score == nil || choice.SKUSplit == nil {
		return nil, fmt.Errorf("SKU Mix Placement response contained an invalid placement choice")
	}

	result := make([]VMSplitItem, 0, len(choice.SKUSplit))
	for _, split := range choice.SKUSplit {
		if split == nil || split.Name == nil || split.Capacity == nil {
			return nil, fmt.Errorf("SKU Mix Placement response contained an invalid SKU split")
		}
		result = append(result, VMSplitItem{
			Name:  *split.Name,
			Count: *split.Capacity,
			Zone:  lo.FromPtr(split.Zone),
		})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("SKU Mix Placement response contained no recommended VM sizes")
	}
	return &Recommendation{ID: lo.FromPtr(choice.ID), VMSplit: result}, nil
}

// bestPlacementChoice ensures that we choose the choice we believe is best in the presence of score-ties.
// The hope is that we can work with the SKU SPlit API team to ensure that the API always returns this choice (at least in some allocation mode),
// but today we've observed some situations where it returns a different choice than we would have naturally picked otherwise. This function
// is a bset-effort attempt to ensure that we pick the best choice we think.
// Note that this is not a guarantee, because if no choice that matches what we think is best is returned we will not pick it (we are only picking from returned choices)
func bestPlacementChoice(response armrecommender.SKUMixPlacementScoresClientPostResponse, input *RankingInput) (*armrecommender.SKUMixPlacementDeploymentChoice, error) {
	if len(response.PlacementChoices) == 0 {
		return nil, fmt.Errorf("SKU Mix Placement response contained no placement choices")
	}

	var best *armrecommender.SKUMixPlacementDeploymentChoice
	for _, choice := range response.PlacementChoices {
		if choice == nil || choice.Score == nil {
			continue
		}
		if best == nil || placementChoiceIsBetter(choice, best, input) {
			best = choice
		}
	}
	if best == nil {
		return nil, fmt.Errorf("SKU Mix Placement response contained no valid placement choices")
	}
	return best, nil
}

func placementChoiceJSON(choice *armrecommender.SKUMixPlacementDeploymentChoice) string {
	value, err := json.Marshal(choice)
	if err != nil {
		return fmt.Sprintf("<failed to marshal placement choice: %s>", err)
	}
	return string(value)
}

func placementChoiceIsBetter(
	candidate *armrecommender.SKUMixPlacementDeploymentChoice,
	current *armrecommender.SKUMixPlacementDeploymentChoice,
	input *RankingInput,
) bool {
	// Only tiebreak among sizes that are equally scored in the API response
	if *candidate.Score != *current.Score {
		return *candidate.Score > *current.Score
	}

	requestedZones := make(map[string]struct{}, len(input.Zones))
	for _, zone := range input.Zones {
		requestedZones[zone] = struct{}{}
	}

	// Compare requested SKUs in input priority order. For
	// each SKU, prefer more allocated capacity, then distribution across more
	// requested zones. Only consider the next-ranked SKU when both are tied.
	for _, vmSize := range input.VMSizes {
		candidateCapacity, candidateZones := skuStats(candidate, vmSize, requestedZones)
		currentCapacity, currentZones := skuStats(current, vmSize, requestedZones)
		if candidateCapacity != currentCapacity {
			return candidateCapacity > currentCapacity
		}
		if candidateZones != currentZones {
			return candidateZones > currentZones
		}
	}

	// If the choices allocate the requested SKUs equally, prefer the one spanning more
	// of the requested zones. Preserve API order when both are equal.
	return requestedZoneCoverage(candidate, requestedZones) > requestedZoneCoverage(current, requestedZones)
}

func skuStats(choice *armrecommender.SKUMixPlacementDeploymentChoice, vmSize string, requestedZones map[string]struct{}) (int32, int) {
	var capacity int32
	zones := make(map[string]struct{}, len(requestedZones))
	for _, splitItem := range choice.SKUSplit {
		if splitItem == nil || lo.FromPtr(splitItem.Name) != vmSize {
			continue
		}
		// TODO: MaxCapacity when they have it?
		capacity += lo.FromPtr(splitItem.Capacity)
		if splitItem.Zone == nil {
			continue
		}
		zone := lo.FromPtr(splitItem.Zone)
		if _, ok := requestedZones[zone]; ok {
			zones[zone] = struct{}{}
		}
	}
	return capacity, len(zones)
}

func requestedZoneCoverage(choice *armrecommender.SKUMixPlacementDeploymentChoice, requestedZones map[string]struct{}) int {
	covered := make(map[string]struct{}, len(requestedZones))
	for _, split := range choice.SKUSplit {
		if split == nil || split.Zone == nil {
			continue
		}
		if _, ok := requestedZones[*split.Zone]; ok {
			covered[*split.Zone] = struct{}{}
		}
	}
	return len(covered)
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
	zones := append([]string(nil), input.Zones...)
	sort.Strings(zones)
	value, err := hashstructure.Hash(struct {
		CapacityType string
		OSType       corev1.OSName
		VMSizes      []string
		Zones        []string
	}{
		CapacityType: input.CapacityType,
		OSType:       input.OSType,
		VMSizes:      input.VMSizes,
		Zones:        zones,
	}, hashstructure.FormatV2, nil)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", value), nil
}

func cloneVMSplit(in []VMSplitItem) []VMSplitItem {
	return append([]VMSplitItem(nil), in...)
}

func cloneRecommendation(in *Recommendation) *Recommendation {
	if in == nil {
		return nil
	}
	return &Recommendation{ID: in.ID, VMSplit: cloneVMSplit(in.VMSplit)}
}
