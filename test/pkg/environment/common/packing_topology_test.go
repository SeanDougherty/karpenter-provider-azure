package common

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPackingConstraintsPreserveFixtureCapacity(t *testing.T) {
	for _, capacity := range []int32{2, 3, 5, 8} {
		labels := map[string]string{"app": "packing"}
		constraints := PackingConstraints(labels, capacity)
		if len(constraints) != 1 {
			t.Fatal("expected one explicit hostname constraint")
		}
		c := constraints[0]
		if c.MaxSkew != capacity || c.TopologyKey != corev1.LabelHostname ||
			c.WhenUnsatisfiable != corev1.DoNotSchedule || c.LabelSelector.MatchLabels["app"] != "packing" {
			t.Fatalf("fixture packing contract changed: %+v", c)
		}
		labels["app"] = "changed"
		if c.LabelSelector.MatchLabels["app"] != "packing" {
			t.Fatal("fixture selector aliases caller labels")
		}
	}
}
