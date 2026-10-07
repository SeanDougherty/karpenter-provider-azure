package common

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestBudgetDisruptionTermination(t *testing.T) {
	now := metav1.Now()
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "original", DeletionTimestamp: &now, Finalizers: []string{karpv1.TerminationFinalizer, TestingFinalizer}},
		Spec:       corev1.NodeSpec{ProviderID: "azure:///vm/original", Taints: []corev1.Taint{karpv1.DisruptedNoScheduleTaint}},
	}
	claim := karpv1.NodeClaim{Status: karpv1.NodeClaimStatus{ProviderID: node.Spec.ProviderID}}
	for _, tc := range []struct {
		name        string
		deleting    bool
		finalizers  []string
		terminating bool
		want        int
	}{
		{"in-flight termination", true, node.Finalizers, false, 1},
		{"completed instance held by test", true, []string{TestingFinalizer}, false, 0},
		{"live node without controller finalizer", false, []string{TestingFinalizer}, false, 1},
		{"not a test-held node", true, nil, false, 1},
		{"existing terminating condition", true, node.Finalizers, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, nc := node.DeepCopy(), claim.DeepCopy()
			current.Finalizers = tc.finalizers
			if !tc.deleting {
				current.DeletionTimestamp = nil
			}
			if tc.terminating {
				nc.StatusConditions().SetTrue(karpv1.ConditionTypeInstanceTerminating)
			}
			got, err := budgetDisruptingCount([]karpv1.NodeClaim{*nc}, []corev1.Node{*current}, 1, 1)
			if err != nil || got != tc.want {
				t.Fatalf("disrupting=%d, want %d: %v", got, tc.want, err)
			}
		})
	}
}

func TestBudgetDisruptionPreservesCeilings(t *testing.T) {
	nodes := []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "one"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{karpv1.DisruptedNoScheduleTaint}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "two"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{karpv1.DisruptedNoScheduleTaint}}},
	}
	if _, err := budgetDisruptingCount(nil, nodes, 2, 1); err == nil {
		t.Fatal("disruption ceiling was weakened")
	}
	if _, err := budgetDisruptingCount(make([]karpv1.NodeClaim, 4), nil, 2, 1); err == nil {
		t.Fatal("claim ceiling was weakened")
	}
	nodes[0].Spec.Taints, nodes[1].Spec.Taints = nil, nil
	nodes = append(nodes, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "three"}}, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "four"}})
	if _, err := budgetDisruptingCount(nil, nodes, 2, 1); err == nil {
		t.Fatal("node ceiling was weakened")
	}
}
