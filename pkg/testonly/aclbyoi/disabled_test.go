//go:build !aclbyoi

package aclbyoi

import (
	"context"
	"testing"

	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestNormalBuildCannotEnableBYOI(t *testing.T) {
	t.Setenv("ACL_TEST_BYOI_IMAGE_ID", "not-a-valid-image")
	t.Setenv("ACL_TEST_BYOI_CLUSTER_ID", "not-a-valid-cluster")
	id, err := ImageID(context.Background(), &v1beta1.AKSNodeClass{})
	require.NoError(t, err)
	require.Empty(t, id)
	p, err := ClientPolicy("any-subscription")
	require.NoError(t, err)
	require.Nil(t, p)
}
