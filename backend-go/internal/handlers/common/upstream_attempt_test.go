package common

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/gin-gonic/gin"
)

func TestUpstreamAttemptLegacyAdapterPreservesEmptyResult(t *testing.T) {
	result := (UpstreamAttempt{RequestedModel: "model"}).TryWithModelMappingFailover()
	handled, successKey, successBaseURLIdx, failoverErr, usage, lastError := TryUpstreamWithModelMappingFailover(
		nil, nil, nil, nil, "", "", nil, nil, "model", false, nil, nil, false,
		nil, nil, nil, nil, nil, nil, AttemptLogContext{},
	)

	if result.Handled != handled || result.SuccessKey != successKey || result.SuccessBaseURLIdx != successBaseURLIdx {
		t.Fatalf("兼容入口基础结果不一致: object=%+v legacy=(%v, %q, %d)", result, handled, successKey, successBaseURLIdx)
	}
	if result.FailoverError != failoverErr || result.Usage != usage || result.LastError != lastError {
		t.Fatalf("兼容入口错误或用量结果不一致: object=%+v", result)
	}
}

func TestUpstreamAttemptDisablesModelFailoverAfterFirstMapping(t *testing.T) {
	cfgManager, err := config.NewConfigManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = cfgManager.Close() })

	metricsManager := metrics.NewMetricsManager()
	t.Cleanup(metricsManager.Stop)

	upstream := &config.UpstreamConfig{
		BaseURL: "https://example.com",
		APIKeys: []string{"test-key"},
		ModelMapping: map[string][]string{
			"requested-model": {"first-model", "second-model"},
		},
	}
	stopErr := errors.New("stop after observing mapped model")
	var resolvedModels []string

	result := (UpstreamAttempt{
		Context:            newAttemptTestContext(context.Background()),
		EnvConfig:          &config.EnvConfig{LogLevel: "error"},
		ConfigManager:      cfgManager,
		MetricsManager:     metricsManager,
		Upstream:           upstream,
		RequestedModel:     "requested-model",
		AllowModelFailover: false,
		URLResults:         BuildDefaultURLResults(upstream.GetAllBaseURLs()),
		NextAPIKey: func(upstream *config.UpstreamConfig, _ map[string]bool) (string, error) {
			resolvedModels = append(resolvedModels, config.ResolveUpstreamModel("requested-model", upstream))
			return "", stopErr
		},
		BuildRequest: func(*gin.Context, *config.UpstreamConfig, string) (*http.Request, error) {
			return nil, errors.New("unexpected build request")
		},
		HandleSuccess: func(*gin.Context, *http.Response, *config.UpstreamConfig, string) (*types.Usage, error) {
			return nil, errors.New("unexpected success handler")
		},
	}).TryWithModelMappingFailover()

	if result.Handled {
		t.Fatalf("Handled = true, want false")
	}
	if !errors.Is(result.LastError, stopErr) {
		t.Fatalf("LastError = %v, want %v", result.LastError, stopErr)
	}
	if len(resolvedModels) != 1 || resolvedModels[0] != "first-model" {
		t.Fatalf("实际尝试模型 = %v, want [first-model]", resolvedModels)
	}
}

func TestUpstreamAttemptStopsModelFailoverOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := (UpstreamAttempt{
		Context: newAttemptTestContext(ctx),
		Upstream: &config.UpstreamConfig{
			ModelMapping: map[string][]string{
				"requested-model": {"first-model", "second-model"},
			},
		},
		RequestedModel:     "requested-model",
		AllowModelFailover: true,
	}).TryWithModelMappingFailover()

	if !result.Handled {
		t.Fatalf("Handled = false, want true")
	}
	if !errors.Is(result.LastError, context.Canceled) {
		t.Fatalf("LastError = %v, want context.Canceled", result.LastError)
	}
	if result.SuccessKey != "" || result.FailoverError != nil || result.Usage != nil {
		t.Fatalf("取消结果包含意外成功或故障转移数据: %+v", result)
	}
}

func TestUpstreamAttemptBuildPreparedRequestClassifiesBuildError(t *testing.T) {
	buildErr := errors.New("build request failed")
	attempt := UpstreamAttempt{
		Context: newAttemptTestContext(context.Background()),
		BuildRequest: func(*gin.Context, *config.UpstreamConfig, string) (*http.Request, error) {
			return nil, buildErr
		},
	}

	request, stage, err := attempt.buildPreparedAttemptRequest(&config.UpstreamConfig{}, "test-key")
	if request != nil {
		t.Fatalf("request = %v, want nil", request)
	}
	if stage != "build_request" {
		t.Fatalf("stage = %q, want build_request", stage)
	}
	if !errors.Is(err, buildErr) {
		t.Fatalf("err = %v, want %v", err, buildErr)
	}
}

func newAttemptTestContext(ctx context.Context) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	return c
}
