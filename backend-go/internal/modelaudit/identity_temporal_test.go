package modelaudit

import (
	"testing"
	"time"
)

func TestAnalyzeTemporalIdentityStageSwitch(t *testing.T) {
	assessment, err := AnalyzeTemporalIdentity(testTemporalConfig(), temporalFixture(
		0.92, 0.90, 0.91, 0.89, 0.93, 0.90,
		0.12, 0.10, 0.11, 0.09, 0.13, 0.10,
	))
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Pattern != TemporalStageSwitch || assessment.ChangePoint == nil || assessment.ChangePoint.AfterWindow != 3 {
		t.Fatalf("阶段切换判定 = %#v", assessment)
	}
	if assessment.ChangePoint.Direction != "away_from_declared" || len(assessment.Windows) != 6 || len(assessment.Evidence) != 12 {
		t.Fatalf("阶段切换证据 = %#v", assessment)
	}
}

func TestAnalyzeTemporalIdentityShortAnomaly(t *testing.T) {
	assessment, err := AnalyzeTemporalIdentity(testTemporalConfig(), temporalFixture(
		0.92, 0.90, 0.91, 0.89, 0.20, 0.22, 0.93, 0.90, 0.91, 0.89,
	))
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Pattern != TemporalShortAnomaly || len(assessment.AnomalyWindows) != 1 || assessment.AnomalyWindows[0] != 2 {
		t.Fatalf("短期异常判定 = %#v", assessment)
	}
}

func TestAnalyzeTemporalIdentityStableShiftedAndIntermittent(t *testing.T) {
	shifted, err := AnalyzeTemporalIdentity(testTemporalConfig(), temporalFixture(
		0.10, 0.12, 0.11, 0.09, 0.13, 0.10, 0.12, 0.11,
	))
	if err != nil {
		t.Fatal(err)
	}
	if shifted.Pattern != TemporalStableShifted {
		t.Fatalf("稳定替换判定 = %#v", shifted)
	}

	intermittent, err := AnalyzeTemporalIdentity(testTemporalConfig(), temporalFixture(
		0.90, 0.92, 0.10, 0.12, 0.88, 0.90, 0.15, 0.13,
	))
	if err != nil {
		t.Fatal(err)
	}
	if intermittent.Pattern != TemporalIntermittent {
		t.Fatalf("间歇异常判定 = %#v", intermittent)
	}
}

func TestAnalyzeTemporalIdentityInsufficientEvidence(t *testing.T) {
	assessment, err := AnalyzeTemporalIdentity(testTemporalConfig(), temporalFixture(0.9, 0.8, 0.9, 0.8, 0.9, 0.8))
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Pattern != TemporalInsufficientEvidence || assessment.Reason == "" || len(assessment.Windows) != 3 {
		t.Fatalf("时序证据不足判定 = %#v", assessment)
	}
	if err := assessment.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeTemporalIdentityRejectsDuplicateOrdinal(t *testing.T) {
	observations := temporalFixture(0.9, 0.8)
	observations[1].Ordinal = observations[0].Ordinal
	if _, err := AnalyzeTemporalIdentity(testTemporalConfig(), observations); err == nil {
		t.Fatal("重复时序序号应显式失败")
	}
}

func testTemporalConfig() TemporalConfig {
	return TemporalConfig{
		Detector:   VersionedRef{ID: "identity.temporal", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		WindowSize: 2, WindowStep: 2, MinimumWindows: 4, MinimumSegmentWindows: 2,
		MatchedThreshold: 0.75, ShiftedThreshold: 0.25, ChangeThreshold: 0.5,
		SegmentSpreadThreshold: 0.1, AnomalyThreshold: 0.4, MaximumShortAnomalyWindows: 1,
	}
}

func temporalFixture(scores ...float64) []TemporalObservation {
	startedAt := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	observations := make([]TemporalObservation, len(scores))
	for index, score := range scores {
		id := "temporal-" + string(rune('a'+index))
		observations[index] = TemporalObservation{
			SampleID: id, Ordinal: index, CapturedAt: startedAt.Add(time.Duration(index) * time.Minute), Score: score,
			Evidence: []EvidenceReference{testSignalEvidence(id)},
		}
	}
	return observations
}
