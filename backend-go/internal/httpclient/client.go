package httpclient

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

// ClientManager HTTP 客户端管理器
type ClientManager struct {
	mu      sync.RWMutex
	clients map[string]*http.Client
}

var globalManager = &ClientManager{
	clients: make(map[string]*http.Client),
}

// GetManager 获取全局客户端管理器
func GetManager() *ClientManager {
	return globalManager
}

// GetStandardClient 获取标准客户端（有超时，用于普通请求）
// 注意：启用自动压缩让Go处理gzip，配合请求头清理确保正确解压
func (cm *ClientManager) GetStandardClient(timeout time.Duration, insecure bool, proxyURL string) (*http.Client, error) {
	envConfig := config.NewEnvConfig()
	responseHeaderTimeout := time.Duration(envConfig.ResponseHeaderTimeout) * time.Second
	proxyURL = strings.TrimSpace(proxyURL)

	key := fmt.Sprintf("standard-%d-%t-%d-%t-%s", timeout, insecure, envConfig.ResponseHeaderTimeout, envConfig.ForceHTTP1, proxyKey(proxyURL))

	cm.mu.RLock()
	if client, ok := cm.clients[key]; ok {
		cm.mu.RUnlock()
		return client, nil
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if client, ok := cm.clients[key]; ok {
		return client, nil
	}
	proxyFunc, err := buildProxyFunc(proxyURL)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy:                 proxyFunc,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    false,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     !envConfig.ForceHTTP1,
	}

	tlsCfg := &tls.Config{}
	if envConfig.ForceHTTP1 {
		tlsCfg.NextProtos = []string{"http/1.1"}
	}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	transport.TLSClientConfig = tlsCfg

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	cm.clients[key] = client
	return client, nil
}

// GetStandardClientForUpstream 根据全局与渠道配置解析最终代理后创建普通请求客户端。
func (cm *ClientManager) GetStandardClientForUpstream(timeout time.Duration, cfgManager *config.ConfigManager, upstream *config.UpstreamConfig) (*http.Client, error) {
	if cfgManager == nil || upstream == nil {
		return nil, fmt.Errorf("创建上游 HTTP 客户端时缺少配置")
	}
	proxyURL, err := cfgManager.ResolveUpstreamProxyURL(upstream)
	if err != nil {
		return nil, err
	}
	return cm.GetStandardClient(timeout, upstream.InsecureSkipVerify, proxyURL)
}

// GetStreamClient 获取流式客户端（响应体无超时，用于 SSE 流式响应）
// ResponseHeaderTimeout 限制"发出请求到收到响应头"的时间（首字节超时），
// 防止"连接建立但永不响应"的上游占住 handler；响应体读取本身不受限，
// 避免思考模型长静默期被整体超时误杀。流中挂起由 IdleTimeoutReader 检测（见 STREAM_IDLE_TIMEOUT）。
func (cm *ClientManager) GetStreamClient(insecure bool, proxyURL string) (*http.Client, error) {
	envConfig := config.NewEnvConfig()
	proxyURL = strings.TrimSpace(proxyURL)

	key := fmt.Sprintf("stream-%t-%t-%d-%s", insecure, envConfig.ForceHTTP1, envConfig.ResponseHeaderTimeout, proxyKey(proxyURL))

	cm.mu.RLock()
	if client, ok := cm.clients[key]; ok {
		cm.mu.RUnlock()
		return client, nil
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if client, ok := cm.clients[key]; ok {
		return client, nil
	}
	proxyFunc, err := buildProxyFunc(proxyURL)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy:                 proxyFunc,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       120 * time.Second,
		DisableCompression:    true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Duration(envConfig.ResponseHeaderTimeout) * time.Second, // 首字节超时
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     !envConfig.ForceHTTP1,
	}

	tlsCfg := &tls.Config{}
	if envConfig.ForceHTTP1 {
		tlsCfg.NextProtos = []string{"http/1.1"}
	}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	transport.TLSClientConfig = tlsCfg

	client := &http.Client{
		Transport: transport,
		Timeout:   0,
	}

	cm.clients[key] = client
	return client, nil
}

func buildProxyFunc(rawURL string) (func(*http.Request) (*url.URL, error), error) {
	if rawURL == "" {
		return nil, nil
	}
	if err := config.ValidateProxyURL(rawURL); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析代理地址失败: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return http.ProxyURL(parsed), nil
}

func proxyKey(proxyURL string) string {
	if proxyURL == "" {
		return "direct"
	}
	sum := sha256.Sum256([]byte(proxyURL))
	return fmt.Sprintf("%x", sum[:])
}
