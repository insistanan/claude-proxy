package modelaudit

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type HTTPClientFactory interface {
	ClientFor(context.Context, ResolvedTarget, ExecutionSpec) (HTTPDoer, error)
}

type ConfigHTTPClientFactory struct {
	cfgManager *config.ConfigManager
	clients    *httpclient.ClientManager
}

func NewConfigHTTPClientFactory(cfgManager *config.ConfigManager) (*ConfigHTTPClientFactory, error) {
	if cfgManager == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "配置管理器不能为空")
	}
	return &ConfigHTTPClientFactory{cfgManager: cfgManager, clients: httpclient.GetManager()}, nil
}

func (f *ConfigHTTPClientFactory) ClientFor(_ context.Context, target ResolvedTarget, spec ExecutionSpec) (HTTPDoer, error) {
	if f == nil || f.cfgManager == nil || f.clients == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "HTTP 客户端工厂未初始化")
	}
	proxyURL, err := f.cfgManager.ResolveUpstreamProxyURL(&target.Upstream)
	if err != nil {
		return nil, fmt.Errorf("解析渠道代理配置失败: %w", err)
	}
	timeout := time.Duration(spec.TimeoutMillis) * time.Millisecond
	return f.clients.GetStandardClient(timeout, target.Upstream.InsecureSkipVerify, proxyURL)
}

type StaticHTTPClientFactory struct {
	Doer HTTPDoer
}

func (f StaticHTTPClientFactory) ClientFor(context.Context, ResolvedTarget, ExecutionSpec) (HTTPDoer, error) {
	if f.Doer == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "HTTP Doer 不能为空")
	}
	return f.Doer, nil
}
