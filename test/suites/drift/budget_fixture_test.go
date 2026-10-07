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
package drift_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func budgetPackingConstraints() []corev1.TopologySpreadConstraint {
	return []corev1.TopologySpreadConstraint{{
		// As in the upstream budget fixture, allow three pods per hostname.
		// System-pool domains have no matching pods and must not force one per node.
		MaxSkew:           3,
		TopologyKey:       corev1.LabelHostname,
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "large-app"}},
	}}
}

func TestBudgetPackingConstraints(t *testing.T) {
	constraints := budgetPackingConstraints()
	if len(constraints) != 1 {
		t.Fatal("budget fixture must define its topology explicitly")
	}
	c := constraints[0]
	if c.MaxSkew != 3 || c.TopologyKey != corev1.LabelHostname ||
		c.WhenUnsatisfiable != corev1.DoNotSchedule || c.LabelSelector.MatchLabels["app"] != "large-app" {
		t.Fatalf("incorrect budget packing constraint: %+v", c)
	}
}
