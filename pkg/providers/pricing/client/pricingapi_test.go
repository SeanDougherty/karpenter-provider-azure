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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestPricingPaginationAndBodyCleanup(t *testing.T) {
	var bodies []*trackedBody
	calls, pages := 0, 0
	api := New(cloud.AzurePublic).(*pricingAPI)
	api.httpClient.Transport = roundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "" || req.URL.Hostname() != "prices.azure.com" {
			t.Fatal("retail pricing must not send Azure credentials or use another host")
		}
		json := `{"Items":[]}`
		if calls == 1 {
			json = `{"Items":[],"NextPageLink":"https://prices.azure.com:443/api/retail/prices?$skip=1000"}`
		}
		body := &trackedBody{Reader: strings.NewReader(json)}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body, Request: req}, nil
	})
	if err := api.GetProductsPricePages(t.Context(), nil, func(*ProductsPricePage) { pages++ }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || pages != 2 || !bodies[0].closed || !bodies[1].closed {
		t.Fatalf("pages=%d calls=%d; every page body must be closed", pages, calls)
	}
}

func TestPricingCancellation(t *testing.T) {
	api := New(cloud.AzurePublic).(*pricingAPI)
	api.httpClient.Transport = roundTripper(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := api.GetProductsPricePages(ctx, nil, func(*ProductsPricePage) { t.Fatal("cancelled request emitted a page") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must reach the HTTP request: %v", err)
	}
}

func TestPricingRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", 500, `{}`},
		{"invalid JSON", 200, `invalid`},
		{"foreign next page", 200, `{"NextPageLink":"https://untrusted.example/api/retail/prices"}`},
		{"non-HTTPS port", 200, `{"NextPageLink":"https://prices.azure.com:8443/api/retail/prices"}`},
		{"repeated page", 200, `{"NextPageLink":"` + pricingURL + `"}`},
		{"redirect", 302, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			api := New(cloud.AzurePublic).(*pricingAPI)
			api.httpClient.Transport = roundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: tc.status, Header: http.Header{"Location": []string{"https://untrusted.example/"}},
					Body: body, Request: req,
				}, nil
			})
			if err := api.GetProductsPricePages(t.Context(), nil, func(*ProductsPricePage) {}); err == nil {
				t.Fatal("invalid pricing response must fail")
			}
			if calls != 1 || !body.closed {
				t.Fatalf("invalid page must stop pagination and close its body: calls=%d closed=%t", calls, body.closed)
			}
		})
	}
}

func TestPricingUnsupportedCloudDoesNotMakeRequests(t *testing.T) {
	api := New(cloud.AzureChina).(*pricingAPI)
	api.httpClient.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported cloud must not make a request")
		return nil, nil
	})
	if err := api.GetProductsPricePages(t.Context(), nil, func(*ProductsPricePage) {}); err == nil {
		t.Fatal("unsupported cloud must fail explicitly")
	}
}
