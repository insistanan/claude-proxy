package modelaudit

import (
	"math"
	"testing"
)

func TestProbabilityPrimitives(t *testing.T) {
	smoothed, err := SmoothDistribution([]float64{8, 2}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(smoothed[0]-0.75) > 1e-12 || math.Abs(smoothed[1]-0.25) > 1e-12 {
		t.Fatalf("平滑分布 = %#v", smoothed)
	}
	identical, err := JensenShannonDivergence([]float64{0.8, 0.2}, []float64{0.8, 0.2})
	if err != nil || math.Abs(identical) > 1e-12 {
		t.Fatalf("相同分布 JSD = %v, err=%v", identical, err)
	}
	disjoint, err := JensenShannonDivergence([]float64{1, 0}, []float64{0, 1})
	if err != nil || math.Abs(disjoint-1) > 1e-12 {
		t.Fatalf("不相交分布 JSD = %v, err=%v", disjoint, err)
	}
}

func TestCandidateClassificationAndOOD(t *testing.T) {
	candidates := map[string][]float64{
		"sol":   {0.8, 0.2},
		"terra": {0.2, 0.8},
	}
	matched, err := ClassifyCandidateDistribution([]float64{80, 20}, candidates, 0.5, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if matched.OOD || matched.BestCandidate != "sol" {
		t.Fatalf("匹配分类 = %#v", matched)
	}
	ood, err := ClassifyCandidateDistribution([]float64{50, 50}, candidates, 0.5, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if !ood.OOD || ood.BestCandidate != "" {
		t.Fatalf("OOD 分类 = %#v", ood)
	}
}

func TestIdentityReportRejectsFormalInsufficientEvidence(t *testing.T) {
	report := IdentityReport{
		Aggregator: VersionedRef{ID: "identity.aggregator", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		Conclusion: IdentityInsufficientEvidence, Formal: true, Confidence: 0.2,
	}
	if err := report.Validate(); err == nil {
		t.Fatal("证据不足报告不能标记为正式")
	}
}
