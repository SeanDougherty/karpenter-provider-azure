package common

import (
	"fmt"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func budgetDisruptingCount(claims []karpv1.NodeClaim, nodes []corev1.Node, initial, maximum int) (int, error) {
	terminatingIDs := sets.New[string]()
	for _, claim := range claims {
		if claim.StatusConditions().IsTrue(karpv1.ConditionTypeInstanceTerminating) && claim.Status.ProviderID != "" {
			terminatingIDs.Insert(claim.Status.ProviderID)
		}
	}
	completedNames := sets.New[string]()
	for _, node := range nodes {
		// A test finalizer can retain a Node after Karpenter has completed
		// instance termination and removed its own finalizer.
		if node.DeletionTimestamp != nil && lo.Contains(node.Finalizers, TestingFinalizer) && !lo.Contains(node.Finalizers, karpv1.TerminationFinalizer) {
			completedNames.Insert(node.Name)
			if node.Spec.ProviderID != "" {
				terminatingIDs.Insert(node.Spec.ProviderID)
			}
		}
	}
	activeClaims := 0
	for _, claim := range claims {
		if !claim.StatusConditions().IsTrue(karpv1.ConditionTypeInstanceTerminating) && !terminatingIDs.Has(claim.Status.ProviderID) {
			activeClaims++
		}
	}
	activeNodes := sets.New[string]()
	disrupting := 0
	for _, node := range nodes {
		if terminatingIDs.Has(node.Spec.ProviderID) || completedNames.Has(node.Name) {
			continue
		}
		activeNodes.Insert(node.Name)
		if lo.ContainsBy(node.Spec.Taints, func(taint corev1.Taint) bool { return taint.MatchTaint(&karpv1.DisruptedNoScheduleTaint) }) {
			disrupting++
		}
	}
	if activeClaims > initial+maximum || activeNodes.Len() > initial+maximum {
		return disrupting, fmt.Errorf("too many active resources: claims=%d nodes=%d maximum=%d", activeClaims, activeNodes.Len(), initial+maximum)
	}
	if disrupting > maximum {
		return disrupting, fmt.Errorf("too many disruptions: got %d, maximum %d", disrupting, maximum)
	}
	return disrupting, nil
}
