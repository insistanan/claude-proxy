package proxycore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/gin-gonic/gin"
)

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

// truncatedBodyHandler 写出声明长度与实际不符的 400 响应：客户端
// io.ReadAll 读取时必然得到 unexpected EOF，模拟上游连接半途断开。
func truncatedBodyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":"truncated`))
	if hijacker, ok := w.(http.Hijacker); ok {
		if conn, _, err := hijacker.Hijack(); err == nil {
			_ = conn.Close() // 立即断开，制造 unexpected EOF
		}
	}
}

// TestUpstreamAttemptReadBodyErrorFailsOverToNextKey 锚点：错误响应体读取
// 失败必须按网络故障换 Key，不得把截断 body 用于错误分类或回给客户端。
func TestUpstreamAttemptReadBodyErrorFailsOverToNextKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		if key == "key-a" {
			truncatedBodyHandler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	cfgManager, err := config.NewConfigManager(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = cfgManager.Close() })

	metricsManager := metrics.NewMetricsManager()
	t.Cleanup(metricsManager.Stop)

	channelScheduler := scheduler.NewChannelScheduler(cfgManager, metricsManager, nil, nil, nil, nil, nil, nil)
	t.Cleanup(channelScheduler.Stop)

	upstream := &config.UpstreamConfig{
		BaseURL: server.URL,
		APIKeys: []string{"key-a", "key-b"},
	}
	var successKeys []string

	result := (UpstreamAttempt{
		Context:          newAttemptTestContext(context.Background()),
		EnvConfig:        &config.EnvConfig{LogLevel: "error"},
		ConfigManager:    cfgManager,
		ChannelScheduler: channelScheduler,
		Kind:             scheduler.ChannelKindMessages,
		MetricsManager:   metricsManager,
		Upstream:         upstream,
		RequestedModel:   "requested-model",
		URLResults:       BuildDefaultURLResults(upstream.GetAllBaseURLs()),
		NextAPIKey: func(up *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			for _, key := range up.APIKeys {
				if !failedKeys[key] {
					return key, nil
				}
			}
			return "", errors.New("没有可用密钥")
		},
		BuildRequest: func(_ *gin.Context, up *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPost, server.URL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("x-api-key", apiKey)
			return req, nil
		},
		HandleSuccess: func(_ *gin.Context, resp *http.Response, _ *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
			defer resp.Body.Close()
			if _, err := io.ReadAll(resp.Body); err != nil {
				return nil, err
			}
			successKeys = append(successKeys, apiKey)
			return &types.Usage{}, nil
		},
	}).TryWithModelMappingFailover()

	if !result.Handled {
		t.Fatalf("Handled = false, want true（第二个 key 应成功）")
	}
	if result.SuccessKey != "key-b" {
		t.Fatalf("SuccessKey = %q, want key-b（读取失败应换 key 而非用截断 body 分类）", result.SuccessKey)
	}
	if len(successKeys) != 1 || successKeys[0] != "key-b" {
		t.Fatalf("实际成功 key = %v, want [key-b]", successKeys)
	}
	if result.LastError != nil {
		t.Fatalf("LastError = %v, want nil", result.LastError)
	}
}
