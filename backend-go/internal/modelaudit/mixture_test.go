package modelaudit

import (
	"math"
	"testing"
)

func TestEstimateMixture(t *testing.T) {
	estimate, err := EstimateMixture(
		[]int{50, 50},
		map[string][]float64{"sol": {0.9, 0.1}, "terra": {0.1, 0.9}},
		MixtureConfig{MaxIterations: 500, Tolerance: 1e-9, BootstrapSamples: 100, ConfidenceLevel: 0.95, Seed: 42},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !estimate.Converged || len(estimate.Components) != 2 {
		t.Fatalf("混合估计 = %#v", estimate)
	}
	for _, component := range estimate.Components {
		if math.Abs(component.Proportion-0.5) > 0.01 {
			t.Fatalf("混合比例 = %#v", estimate.Components)
		}
		if component.Lower > component.Proportion || component.Upper < component.Proportion {
			t.Fatalf("混合区间未覆盖估计值: %#v", component)
		}
	}
}

func TestEstimateMixtureRejectsUnexplainedCategory(t *testing.T) {
	_, err := EstimateMixture(
		[]int{0, 1},
		map[string][]float64{"sol": {1, 0}, "terra": {1, 0}},
		MixtureConfig{MaxIterations: 10, Tolerance: 1e-6, BootstrapSamples: 10, ConfidenceLevel: 0.95, Seed: 1},
	)
	if err == nil {
		t.Fatal("所有候选概率为 0 的观测类别应显式失败")
	}
}
