package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/urlhealth"
)

// createTestConfigManager 创建测试用配置管理器
func createTestConfigManager(t *testing.T, cfg config.Config) (*config.ConfigManager, func()) {
	t.Helper()

	// 创建临时目录
	tmpDir, err := os.MkdirTemp("", "scheduler-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}

	// 创建临时配置文件
	configFile := filepath.Join(tmpDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("序列化配置失败: %v", err)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("写入配置文件失败: %v", err)
	}

	// 创建配置管理器
	cfgManager, err := config.NewConfigManager(configFile)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("创建配置管理器失败: %v", err)
	}

	cleanup := func() {
		cfgManager.Close()
		os.RemoveAll(tmpDir)
	}

	return cfgManager, cleanup
}

// createTestScheduler 创建测试用调度器
func createTestScheduler(t *testing.T, cfg config.Config) (*ChannelScheduler, func()) {
	t.Helper()

	cfgManager, cleanup := createTestConfigManager(t, cfg)
	messagesMetrics := metrics.NewMetricsManager()
	responsesMetrics := metrics.NewMetricsManager()
	geminiMetrics := metrics.NewMetricsManager()
	chatMetrics := metrics.NewMetricsManager()
	imagesMetrics := metrics.NewMetricsManager()
	traceAffinity := session.NewTraceAffinityManager()
	urlManager := urlhealth.NewURLManager(30*time.Second, 3)

	scheduler := NewChannelScheduler(cfgManager, messagesMetrics, responsesMetrics, geminiMetrics, chatMetrics, imagesMetrics, traceAffinity, urlManager)

	return scheduler, func() {
		messagesMetrics.Stop()
		responsesMetrics.Stop()
		geminiMetrics.Stop()
		chatMetrics.Stop()
		imagesMetrics.Stop()
		cleanup()
	}
}

// TestPromotedChannelBypassesHealthCheck 测试促销渠道绕过健康检查
func TestPromotedChannelBypassesHealthCheck(t *testing.T) {
	// 设置促销截止时间为 5 分钟后
	promotionUntil := time.Now().Add(5 * time.Minute)

	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "normal-channel",
				BaseURL:  "https://normal.example.com",
				APIKeys:  []string{"sk-normal-key"},
				Status:   "active",
				Priority: 1,
			},
			{
				Name:           "promoted-channel",
				BaseURL:        "https://promoted.example.com",
				APIKeys:        []string{"sk-promoted-key"},
				Status:         "active",
				Priority:       2,
				PromotionUntil: &promotionUntil,
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	// 模拟促销渠道之前有高失败率（使其不健康）
	metricsManager := scheduler.messagesMetricsManager
	for i := 0; i < 10; i++ {
		metricsManager.RecordFailure("https://promoted.example.com", "sk-promoted-key")
	}

	// 验证促销渠道确实不健康
	isHealthy := metricsManager.IsChannelHealthyWithKeys("https://promoted.example.com", []string{"sk-promoted-key"})
	if isHealthy {
		t.Fatal("促销渠道应该被标记为不健康")
	}

	// 选择渠道 - 促销渠道应该被选中，即使它不健康
	result, err := scheduler.SelectChannel(context.Background(), "test-user", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("选择渠道失败: %v", err)
	}

	if result.ChannelIndex != 1 {
		t.Errorf("期望选择促销渠道 (index=1)，实际选择了 index=%d", result.ChannelIndex)
	}

	if result.Reason != "promotion_priority" {
		t.Errorf("期望选择原因为 promotion_priority，实际为 %s", result.Reason)
	}

	if result.Upstream.Name != "promoted-channel" {
		t.Errorf("期望选择 promoted-channel，实际选择了 %s", result.Upstream.Name)
	}
}

func TestSchedulerScopesChannelCountAndSelectionToModelPool(t *testing.T) {
	cfg := config.Config{
		MessagePools: []config.ChannelPool{
			{ID: "sonnet", Name: "Sonnet", ModelMatcher: "sonnet", Priority: 1},
			{ID: config.DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 2},
		},
		Upstream: []config.UpstreamConfig{
			{ID: "default", Name: "default", PoolID: config.DefaultChannelPoolID, BaseURL: "https://default.example.com", APIKeys: []string{"sk-default"}, Status: "active", Priority: 1},
			{ID: "sonnet-a", Name: "sonnet-a", PoolID: "sonnet", BaseURL: "https://sonnet-a.example.com", APIKeys: []string{"sk-a"}, Status: "active", Priority: 2},
			{ID: "sonnet-b", Name: "sonnet-b", PoolID: "sonnet", BaseURL: "https://sonnet-b.example.com", APIKeys: []string{"sk-b"}, Status: "active", Priority: 3},
			{ID: "vision", Name: "vision", PoolID: "sonnet", BaseURL: "https://vision.example.com", APIKeys: []string{"sk-vision"}, Status: "active", VisionCapable: true, ExcludeFromConversation: true},
		},
	}
	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	if got := scheduler.GetActiveChannelCountForModel(ChannelKindMessages, "claude-sonnet-4"); got != 2 {
		t.Fatalf("sonnet channel count = %d, want 2", got)
	}
	if got := scheduler.GetActiveChannelCountForModel(ChannelKindMessages, "claude-opus-4"); got != 1 {
		t.Fatalf("default channel count = %d, want 1", got)
	}

	selected, err := scheduler.SelectChannel(context.Background(), "pool-user", map[int]bool{}, ChannelKindMessages, "claude-sonnet-4", true)
	if err != nil {
		t.Fatalf("SelectChannel() error = %v", err)
	}
	defer scheduler.ReleaseChannelReservation(selected.Kind, selected.ChannelIndex)
	if selected.Upstream.PoolID != "sonnet" || selected.Upstream.ExcludeFromConversation {
		t.Fatalf("selected upstream = %#v", selected.Upstream)
	}
}

func TestDefaultPoolPromotionPrecedesMatchedPoolAndFallsBackAfterFailure(t *testing.T) {
	promotionUntil := time.Now().Add(5 * time.Minute)
	cfg := config.Config{
		MessagePools: []config.ChannelPool{
			{ID: "deepseek", Name: "DeepSeek", ModelMatcher: "deepseek", Priority: 1},
			{ID: config.DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 2},
		},
		Upstream: []config.UpstreamConfig{
			{ID: "default-promoted", Name: "default-promoted", PoolID: config.DefaultChannelPoolID, BaseURL: "https://default.example.com", APIKeys: []string{"sk-default"}, Status: "active", Priority: 2, PromotionUntil: &promotionUntil},
			{ID: "deepseek-promoted", Name: "deepseek-promoted", PoolID: "deepseek", BaseURL: "https://deepseek.example.com", APIKeys: []string{"sk-deepseek"}, Status: "active", Priority: 1, PromotionUntil: &promotionUntil},
		},
	}
	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	if got := scheduler.GetActiveChannelCountForModel(ChannelKindMessages, "deepseek-chat"); got != 2 {
		t.Fatalf("deepseek 可尝试渠道数 = %d, want 2", got)
	}

	first, err := scheduler.SelectChannel(context.Background(), "pool-user", map[int]bool{}, ChannelKindMessages, "deepseek-chat", false)
	if err != nil {
		t.Fatalf("首次选择渠道失败: %v", err)
	}
	if first.ChannelIndex != 0 || first.Reason != "default_pool_promotion_priority" {
		t.Fatalf("首次应选择默认子池抢优渠道，got index=%d reason=%s", first.ChannelIndex, first.Reason)
	}
	scheduler.ReleaseChannelReservation(first.Kind, first.ChannelIndex)

	second, err := scheduler.SelectChannel(context.Background(), "pool-user", map[int]bool{first.ChannelIndex: true}, ChannelKindMessages, "deepseek-chat", false)
	if err != nil {
		t.Fatalf("默认子池抢优渠道失败后的选择失败: %v", err)
	}
	defer scheduler.ReleaseChannelReservation(second.Kind, second.ChannelIndex)
	if second.ChannelIndex != 1 || second.Reason != "promotion_priority" {
		t.Fatalf("应回落到命中子池的抢优渠道，got index=%d reason=%s", second.ChannelIndex, second.Reason)
	}
}

// TestPromotedChannelSkippedAfterFailure 测试促销渠道在本次请求失败后被跳过
func TestPromotedChannelSkippedAfterFailure(t *testing.T) {
	promotionUntil := time.Now().Add(5 * time.Minute)

	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "normal-channel",
				BaseURL:  "https://normal.example.com",
				APIKeys:  []string{"sk-normal-key"},
				Status:   "active",
				Priority: 1,
			},
			{
				Name:           "promoted-channel",
				BaseURL:        "https://promoted.example.com",
				APIKeys:        []string{"sk-promoted-key"},
				Status:         "active",
				Priority:       2,
				PromotionUntil: &promotionUntil,
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	// 标记促销渠道已失败
	failedChannels := map[int]bool{1: true}

	// 选择渠道 - 应该跳过促销渠道，选择普通渠道
	result, err := scheduler.SelectChannel(context.Background(), "test-user", failedChannels, ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("选择渠道失败: %v", err)
	}

	if result.ChannelIndex != 0 {
		t.Errorf("期望选择普通渠道 (index=0)，实际选择了 index=%d", result.ChannelIndex)
	}

	if result.Upstream.Name != "normal-channel" {
		t.Errorf("期望选择 normal-channel，实际选择了 %s", result.Upstream.Name)
	}
}

// TestUnhealthyChannelSkipped 测试不健康的渠道被跳过
func TestUnhealthyChannelSkipped(t *testing.T) {
	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "unhealthy-channel",
				BaseURL:  "https://unhealthy.example.com",
				APIKeys:  []string{"sk-unhealthy-key"},
				Status:   "active",
				Priority: 1,
			},
			{
				Name:     "healthy-channel",
				BaseURL:  "https://healthy.example.com",
				APIKeys:  []string{"sk-healthy-key"},
				Status:   "active",
				Priority: 2,
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	// 模拟第一个渠道不健康
	metricsManager := scheduler.messagesMetricsManager
	for i := 0; i < 10; i++ {
		metricsManager.RecordFailure("https://unhealthy.example.com", "sk-unhealthy-key")
	}

	// 选择渠道 - 应该跳过不健康的渠道，选择健康的渠道
	result, err := scheduler.SelectChannel(context.Background(), "test-user", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("选择渠道失败: %v", err)
	}

	if result.ChannelIndex != 1 {
		t.Errorf("期望选择健康渠道 (index=1)，实际选择了 index=%d", result.ChannelIndex)
	}

	if result.Upstream.Name != "healthy-channel" {
		t.Errorf("期望选择 healthy-channel，实际选择了 %s", result.Upstream.Name)
	}
}

// TestExpiredPromotionNotBypassHealthCheck 测试过期的促销不绕过健康检查
func TestExpiredPromotionNotBypassHealthCheck(t *testing.T) {
	// 设置促销截止时间为过去
	promotionUntil := time.Now().Add(-5 * time.Minute)

	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "healthy-channel",
				BaseURL:  "https://healthy.example.com",
				APIKeys:  []string{"sk-healthy-key"},
				Status:   "active",
				Priority: 1,
			},
			{
				Name:           "expired-promoted-channel",
				BaseURL:        "https://expired.example.com",
				APIKeys:        []string{"sk-expired-key"},
				Status:         "active",
				Priority:       2,
				PromotionUntil: &promotionUntil, // 已过期
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	// 模拟过期促销渠道不健康
	metricsManager := scheduler.messagesMetricsManager
	for i := 0; i < 10; i++ {
		metricsManager.RecordFailure("https://expired.example.com", "sk-expired-key")
	}

	// 选择渠道 - 过期促销渠道不应该被优先选择，应该选择健康的渠道
	result, err := scheduler.SelectChannel(context.Background(), "test-user", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("选择渠道失败: %v", err)
	}

	if result.ChannelIndex != 0 {
		t.Errorf("期望选择健康渠道 (index=0)，实际选择了 index=%d", result.ChannelIndex)
	}

	if result.Upstream.Name != "healthy-channel" {
		t.Errorf("期望选择 healthy-channel，实际选择了 %s", result.Upstream.Name)
	}
}

// TestSelectChannel_DuplicatePrioritiesNormalizeToFailoverOrder
// 验证：重复优先级会被配置层归一化为严格故障转移顺序，只有当前渠道失败后才进入下一渠道。
func TestSelectChannel_DuplicatePrioritiesNormalizeToFailoverOrder(t *testing.T) {
	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "provider-a",
				BaseURL:  "https://a.example.com",
				APIKeys:  []string{"key-a"},
				Priority: 1,
				Status:   "active",
			},
			{
				Name:     "provider-b",
				BaseURL:  "https://b.example.com",
				APIKeys:  []string{"key-b"},
				Priority: 1,
				Status:   "active",
			},
			{
				Name:     "provider-c",
				BaseURL:  "https://c.example.com",
				APIKeys:  []string{"key-c"},
				Priority: 1,
				Status:   "active",
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	// 使用不同 userID，避免 Trace 亲和把后续请求钉死在同一渠道
	first, err := scheduler.SelectChannel(context.Background(), "user-1", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("第一次选渠失败: %v", err)
	}
	if !first.Reserved {
		t.Fatal("期望第一次选渠占用 in-flight 预留")
	}

	second, err := scheduler.SelectChannel(context.Background(), "user-2", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("第二次选渠失败: %v", err)
	}
	if second.ChannelIndex != first.ChannelIndex {
		t.Fatalf("严格优先级下应保持首选渠道，first=%d second=%d (reasons: %s, %s)",
			first.ChannelIndex, second.ChannelIndex, first.Reason, second.Reason)
	}

	failedChannels := map[int]bool{first.ChannelIndex: true}
	third, err := scheduler.SelectChannel(context.Background(), "user-3", failedChannels, ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("第三次选渠失败: %v", err)
	}
	if third.ChannelIndex == first.ChannelIndex {
		t.Fatalf("首选渠道失败后未切换，first=%d third=%d", first.ChannelIndex, third.ChannelIndex)
	}

	scheduler.ReleaseChannelReservation(second.Kind, second.ChannelIndex)
	scheduler.ReleaseChannelReservation(third.Kind, third.ChannelIndex)
	scheduler.ReleaseChannelReservation(first.Kind, first.ChannelIndex)
	fourth, err := scheduler.SelectChannel(context.Background(), "user-4", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("第四次选渠失败: %v", err)
	}
	if fourth.ChannelIndex != first.ChannelIndex {
		t.Fatalf("恢复后期望回到首选 index=%d，实际 index=%d (reason=%s)",
			first.ChannelIndex, fourth.ChannelIndex, fourth.Reason)
	}
}

// TestSelectChannel_ProtocolIsolation
// 验证：messages 与 responses 的在途负载彼此独立，不会互相抢占分摊结果。
func TestSelectChannel_ProtocolIsolation(t *testing.T) {
	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "msg-provider-a",
				BaseURL:  "https://a.example.com",
				APIKeys:  []string{"key-a"},
				Priority: 1,
				Status:   "active",
			},
			{
				Name:     "msg-provider-b",
				BaseURL:  "https://b.example.com",
				APIKeys:  []string{"key-b"},
				Priority: 1,
				Status:   "active",
			},
		},
		ResponsesUpstream: []config.UpstreamConfig{
			{
				Name:     "resp-provider-a",
				BaseURL:  "https://ra.example.com",
				APIKeys:  []string{"rkey-a"},
				Priority: 1,
				Status:   "active",
			},
			{
				Name:     "resp-provider-b",
				BaseURL:  "https://rb.example.com",
				APIKeys:  []string{"rkey-b"},
				Priority: 1,
				Status:   "active",
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	msg1, err := scheduler.SelectChannel(context.Background(), "msg-user-1", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("messages 选渠失败: %v", err)
	}
	msg2, err := scheduler.SelectChannel(context.Background(), "msg-user-2", make(map[int]bool), ChannelKindMessages, "", false)
	if err != nil {
		t.Fatalf("messages 第二次选渠失败: %v", err)
	}
	if msg1.ChannelIndex != msg2.ChannelIndex {
		t.Fatalf("messages 严格优先级应保持首选渠道: %d != %d", msg1.ChannelIndex, msg2.ChannelIndex)
	}

	// responses 协议有独立渠道列表与 in-flight 计数，首次选择不应被 messages 占用影响
	resp1, err := scheduler.SelectChannel(context.Background(), "resp-user-1", make(map[int]bool), ChannelKindResponses, "", false)
	if err != nil {
		t.Fatalf("responses 选渠失败: %v", err)
	}
	if resp1.ChannelIndex != 0 && resp1.ChannelIndex != 1 {
		t.Fatalf("responses 选渠 index 非法: %d", resp1.ChannelIndex)
	}

	// responses 也独立遵循自身严格优先级顺序
	resp2, err := scheduler.SelectChannel(context.Background(), "resp-user-2", make(map[int]bool), ChannelKindResponses, "", false)
	if err != nil {
		t.Fatalf("responses 第二次选渠失败: %v", err)
	}
	if resp1.ChannelIndex != resp2.ChannelIndex {
		t.Fatalf("responses 严格优先级应保持首选渠道: %d != %d", resp1.ChannelIndex, resp2.ChannelIndex)
	}
	scheduler.ReleaseChannelReservation(msg1.Kind, msg1.ChannelIndex)
	scheduler.ReleaseChannelReservation(msg2.Kind, msg2.ChannelIndex)
	scheduler.ReleaseChannelReservation(resp1.Kind, resp1.ChannelIndex)
	scheduler.ReleaseChannelReservation(resp2.Kind, resp2.ChannelIndex)
}

// TestSelectChannel_AdaptiveRespectsFailoverPriority
// 验证：带模型画像的自适应评分也不能越过配置优先级。
func TestSelectChannel_AdaptiveRespectsFailoverPriority(t *testing.T) {
	cfg := config.Config{
		Upstream: []config.UpstreamConfig{
			{
				Name:     "provider-a",
				BaseURL:  "https://a.example.com",
				APIKeys:  []string{"key-a"},
				Priority: 1,
				Status:   "active",
			},
			{
				Name:     "provider-b",
				BaseURL:  "https://b.example.com",
				APIKeys:  []string{"key-b"},
				Priority: 1,
				Status:   "active",
			},
		},
	}

	scheduler, cleanup := createTestScheduler(t, cfg)
	defer cleanup()

	// 预热两边画像，避免一边无画像导致评分差过大
	if scheduler.profileManager != nil {
		for _, item := range []struct {
			baseURL string
			key     string
			idx     int
		}{
			{"https://a.example.com", "key-a", 0},
			{"https://b.example.com", "key-b", 1},
		} {
			for i := 0; i < 3; i++ {
				requestID := uint64(item.idx*100 + i + 1)
				scheduler.profileManager.StartRequest(item.baseURL, []string{item.key}, "claude-sonnet-4", item.idx, requestID)
				scheduler.profileManager.EndRequest(item.baseURL, []string{item.key}, "claude-sonnet-4", item.idx, requestID, true, 100)
			}
		}
	}

	first, err := scheduler.SelectChannel(context.Background(), "adaptive-user-1", make(map[int]bool), ChannelKindMessages, "claude-sonnet-4", false)
	if err != nil {
		t.Fatalf("adaptive 第一次选渠失败: %v", err)
	}
	second, err := scheduler.SelectChannel(context.Background(), "adaptive-user-2", make(map[int]bool), ChannelKindMessages, "claude-sonnet-4", false)
	if err != nil {
		t.Fatalf("adaptive 第二次选渠失败: %v", err)
	}
	if first.ChannelIndex != second.ChannelIndex {
		t.Fatalf("adaptive 路径越过了严格优先级: first=%d second=%d (reasons: %s, %s)",
			first.ChannelIndex, second.ChannelIndex, first.Reason, second.Reason)
	}
	scheduler.ReleaseChannelReservation(first.Kind, first.ChannelIndex)
	scheduler.ReleaseChannelReservation(second.Kind, second.ChannelIndex)
}
