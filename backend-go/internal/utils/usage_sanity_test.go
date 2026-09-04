package utils

import "testing"

// TestSanityCheckedInputTokens 锁定上游 usage 合理性校验的契约。
//
// 用例中的 26032 / 209736 两组数字来自实测：同一渠道同一分钟内，448KB 的请求被报成
// 26032，压缩后 24KB 的请求被报成 209736——双向错报，且都足以让客户端的压缩决策失效。
func TestSanityCheckedInputTokens(t *testing.T) {
	tests := []struct {
		name           string
		estimatedTotal int
		upstreamInput  int
		upstreamCached int
		wantCorrected  int
		wantNeed       bool
	}{
		{
			name:           "上游可信: 估算与上游同量级",
			estimatedTotal: 130000,
			upstreamInput:  128000,
			wantNeed:       false,
		},
		{
			name:           "上游可信: 偏差未达 2 倍",
			estimatedTotal: 130000,
			upstreamInput:  70000,
			wantNeed:       false,
		},
		{
			name:           "少报: 448KB 请求被报成 26032",
			estimatedTotal: 130000,
			upstreamInput:  26032,
			wantCorrected:  130000,
			wantNeed:       true,
		},
		{
			name:           "多报: 压缩后 24KB 请求被报成 209736",
			estimatedTotal: 7000,
			upstreamInput:  209736,
			wantCorrected:  7000,
			wantNeed:       true,
		},
		{
			name:           "Anthropic 分开报缓存: input 小但含缓存后合计可信",
			estimatedTotal: 205071,
			upstreamInput:  271,
			upstreamCached: 204800,
			wantNeed:       false,
		},
		{
			name:           "Anthropic 语义: 校正值扣掉缓存量",
			estimatedTotal: 130000,
			upstreamInput:  100,
			upstreamCached: 1000,
			wantCorrected:  129000,
			wantNeed:       true,
		},
		{
			name:           "上游完全没报且量级够: 保留缺失，避免整包估算",
			estimatedTotal: 50000,
			upstreamInput:  0,
			wantNeed:       false,
		},
		{
			name:           "小对话: 双边都够不到量级门槛，不介入",
			estimatedTotal: 3000,
			upstreamInput:  10,
			wantNeed:       false,
		},
		{
			name:           "小对话多报: 上游侧超门槛仍需拦截",
			estimatedTotal: 3000,
			upstreamInput:  25000,
			wantCorrected:  3000,
			wantNeed:       true,
		},
		{
			name:           "估算不可用时不介入",
			estimatedTotal: 0,
			upstreamInput:  209736,
			wantNeed:       false,
		},
		{
			name:           "校正值与上游值相同时不改写",
			estimatedTotal: 30000,
			upstreamInput:  30000,
			wantNeed:       false,
		},
		{
			name:           "负数入参钳零后按缺失处理",
			estimatedTotal: 50000,
			upstreamInput:  -5,
			upstreamCached: -10,
			wantNeed:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCorrected, gotNeed := SanityCheckedInputTokens(tt.estimatedTotal, tt.upstreamInput, tt.upstreamCached)
			if gotNeed != tt.wantNeed {
				t.Fatalf("need = %v, want %v (corrected=%d)", gotNeed, tt.wantNeed, gotCorrected)
			}
			if !tt.wantNeed {
				return
			}
			if gotCorrected != tt.wantCorrected {
				t.Errorf("corrected = %d, want %d", gotCorrected, tt.wantCorrected)
			}
			// 校正后 input + cached 必须回到真实规模，不与保留的缓存字段重叠
			cached := tt.upstreamCached
			if cached < 0 {
				cached = 0
			}
			if gotCorrected+cached != tt.estimatedTotal {
				t.Errorf("input(%d) + cached(%d) = %d, 应等于估算总量 %d",
					gotCorrected, cached, gotCorrected+cached, tt.estimatedTotal)
			}
		})
	}
}

func TestAnthropicCachedInputTokens(t *testing.T) {
	tests := []struct {
		name             string
		cacheRead        int
		cacheCreation    int
		cacheCreation5m  int
		cacheCreation1h  int
		wantCachedTokens int
	}{
		{
			name:             "创建总量存在时不重复加 TTL 明细",
			cacheRead:        100,
			cacheCreation:    500,
			cacheCreation5m:  200,
			cacheCreation1h:  300,
			wantCachedTokens: 600,
		},
		{
			name:             "创建总量缺失时使用 TTL 明细之和",
			cacheRead:        100,
			cacheCreation5m:  200,
			cacheCreation1h:  300,
			wantCachedTokens: 600,
		},
		{
			name:             "负值按零处理",
			cacheRead:        -100,
			cacheCreation:    -500,
			cacheCreation5m:  200,
			cacheCreation1h:  300,
			wantCachedTokens: 500,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := AnthropicCachedInputTokens(
				testCase.cacheRead,
				testCase.cacheCreation,
				testCase.cacheCreation5m,
				testCase.cacheCreation1h,
			)
			if got != testCase.wantCachedTokens {
				t.Fatalf("cached tokens = %d, want %d", got, testCase.wantCachedTokens)
			}
		})
	}
}

func TestSanityCheckedAnthropicUsage(t *testing.T) {
	t.Run("可信缓存元组保持不变", func(t *testing.T) {
		_, needsCorrection := SanityCheckedAnthropicUsage(
			205071,
			271,
			204800,
			0,
			0,
			0,
		)
		if needsCorrection {
			t.Fatal("可信 usage 不应被校正")
		}
	})

	t.Run("input 错报但缓存可信时只重建 input", func(t *testing.T) {
		correction, needsCorrection := SanityCheckedAnthropicUsage(
			130000,
			100,
			1000,
			0,
			0,
			0,
		)
		if !needsCorrection {
			t.Fatal("数量级错报应触发校正")
		}
		if correction.InputTokens != 129000 || correction.ClearCache {
			t.Fatalf("unexpected correction: %+v", correction)
		}
	})

	t.Run("缓存本身错报时重建完整客户端元组", func(t *testing.T) {
		correction, needsCorrection := SanityCheckedAnthropicUsage(
			7000,
			500,
			204800,
			0,
			0,
			0,
		)
		if !needsCorrection {
			t.Fatal("缓存数量级错报应触发校正")
		}
		if correction.InputTokens != 7000 || !correction.ClearCache {
			t.Fatalf("unexpected correction: %+v", correction)
		}
	})
}
