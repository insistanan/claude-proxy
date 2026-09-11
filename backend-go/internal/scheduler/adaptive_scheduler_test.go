package scheduler

import (
	"testing"

	"github.com/BenedictKing/api-proxy/internal/metrics"
	"github.com/stretchr/testify/assert"
)

func TestCalculateTTFTPenalty(t *testing.T) {
	// 低延迟无惩罚 (<= 1200ms)
	viewFast := metrics.PerformanceSnapshotView{AvgTTFB: 800}
	assert.Equal(t, float64(0), calculateTTFTPenalty(viewFast))

	// 边界值 1200ms
	viewBorder := metrics.PerformanceSnapshotView{AvgTTFB: 1200}
	assert.Equal(t, float64(0), calculateTTFTPenalty(viewBorder))

	// 中间延迟线性惩罚 (2600ms -> 12.5 分)
	viewMedium := metrics.PerformanceSnapshotView{AvgTTFB: 2600}
	penaltyMedium := calculateTTFTPenalty(viewMedium)
	assert.InDelta(t, 12.5, penaltyMedium, 0.1)

	// 超高延迟满额惩罚 (>= 4000ms -> 25 分)
	viewSlow := metrics.PerformanceSnapshotView{AvgTTFB: 4500}
	assert.Equal(t, 25.0, calculateTTFTPenalty(viewSlow))
}
