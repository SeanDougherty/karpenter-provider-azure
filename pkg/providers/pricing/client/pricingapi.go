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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/karpenter-provider-azure/pkg/auth"
)

const (
	apiVersion = "2023-01-01-preview"
	pricingURL = "https://prices.azure.com/api/retail/prices?api-version=" + apiVersion // TODO: This API is not available in national clouds.
)

type PricingAPI interface {
	GetProductsPricePages(context.Context, []*Filter, func(output *ProductsPricePage)) error
}

type pricingAPI struct {
	cloud      cloud.Configuration
	httpClient *http.Client
}

func New(cloud cloud.Configuration) PricingAPI {
	return &pricingAPI{
		cloud: cloud,
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

func (papi *pricingAPI) GetProductsPricePages(ctx context.Context, filters []*Filter, pageHandler func(output *ProductsPricePage)) error {
	nextURL := pricingURL

	if !auth.IsPublic(papi.cloud) {
		// If the cloud is not Azure Public, the pricing API isn't supported and we return an error
		return fmt.Errorf("pricing API is not supported in non-public clouds")
	}

	if len(filters) > 0 {
		filterParams := []string{}
		for _, filter := range filters {
			filterParams = append(filterParams, filter.String())
		}

		filterParamsEscaped := url.QueryEscape(strings.Join(filterParams[:], " and "))

		nextURL += fmt.Sprintf("&$filter=%s", filterParamsEscaped)
	}

	visited := map[string]bool{}
	for nextURL != "" {
		if visited[nextURL] {
			return fmt.Errorf("pricing API returned a repeated page URL")
		}
		visited[nextURL] = true
		page, err := papi.getPage(ctx, nextURL)
		if err != nil {
			return err
		}
		pageHandler(&page)
		nextURL = page.NextPageLink
	}
	return nil
}

func (papi *pricingAPI) getPage(ctx context.Context, pageURL string) (ProductsPricePage, error) {
	var page ProductsPricePage
	u, err := url.Parse(pageURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "prices.azure.com") ||
		(u.Port() != "" && u.Port() != "443") ||
		u.Path != "/api/retail/prices" || u.User != nil || u.Fragment != "" {
		return page, fmt.Errorf("pricing API returned an invalid page URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return page, fmt.Errorf("creating pricing request: %w", err)
	}
	response, err := papi.httpClient.Do(request)
	if err != nil {
		return page, fmt.Errorf("requesting pricing page: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return page, fmt.Errorf("pricing API returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return page, fmt.Errorf("reading pricing page: %w", err)
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return page, fmt.Errorf("decoding pricing page: %w", err)
	}
	return page, nil
}
