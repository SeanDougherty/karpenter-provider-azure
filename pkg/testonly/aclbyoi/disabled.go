//go:build !aclbyoi

package aclbyoi

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func ImageID(context.Context, *v1beta1.AKSNodeClass) (string, error) { return "", nil }

func ClientPolicy(string) (policy.Policy, error) { return nil, nil }
