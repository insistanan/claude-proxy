package httpclient

import (
	"crypto/tls"
	"fmt"
	"net/http"
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
func (cm *ClientManager) GetStandardClient(timeout time.Duration, insecure bool) *http.Client {
	envConfig := config.NewEnvConfig()
	responseHeaderTimeout := time.Duration(envConfig.ResponseHeaderTimeout) * time.Second

	key := fmt.Sprintf("standard-%d-%t-%d-%t", timeout, insecure, envConfig.ResponseHeaderTimeout, envConfig.ForceHTTP1)

	cm.mu.RLock()
	if client, ok := cm.clients[key]; ok {
		cm.mu.RUnlock()
		return client
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if client, ok := cm.clients[key]; ok {
		return client
	}

	transport := &http.Transport{
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
	return client
}

// GetStreamClient 获取流式客户端（无超时，用于 SSE 流式响应）
// 流式请求不设 ResponseHeaderTimeout，避免上游首字节延迟导致误超时
func (cm *ClientManager) GetStreamClient(insecure bool) *http.Client {
	envConfig := config.NewEnvConfig()

	key := fmt.Sprintf("stream-%t-%t", insecure, envConfig.ForceHTTP1)

	cm.mu.RLock()
	if client, ok := cm.clients[key]; ok {
		cm.mu.RUnlock()
		return client
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if client, ok := cm.clients[key]; ok {
		return client
	}

	transport := &http.Transport{
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       120 * time.Second,
		DisableCompression:    true,
		TLSHandshakeTimeout:   10 * time.Second,
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
	return client
}
