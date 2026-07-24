package config

import (
	"fmt"
	"net/url"
	"strings"
)

func proxyConfigErrorf(format string, args ...interface{}) error {
	return &ConfigError{Message: fmt.Sprintf(format, args...)}
}

const (
	ProxyModeInherit = "inherit"
	ProxyModeDirect  = "direct"
	ProxyModeCustom  = "custom"
)

// ValidateProxyURL 验证 HTTP Transport 支持的代理地址。
func ValidateProxyURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return proxyConfigErrorf("代理地址格式无效")
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return proxyConfigErrorf("代理地址必须包含协议和主机，例如 http://127.0.0.1:7897")
	}
	if parsed.Hostname() == "" {
		return proxyConfigErrorf("代理地址必须包含有效主机")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return proxyConfigErrorf("不支持的代理协议 %q，仅支持 http、https、socks5 和 socks5h", parsed.Scheme)
	}
	if parsed.Fragment != "" || parsed.RawQuery != "" {
		return proxyConfigErrorf("代理地址不能包含查询参数或片段")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return proxyConfigErrorf("代理地址不能包含路径")
	}
	return nil
}

func normalizeProxyMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return ProxyModeInherit
	}
	return mode
}

func normalizeUpstreamProxyConfig(upstream *UpstreamConfig) {
	if upstream == nil {
		return
	}
	upstream.ProxyMode = normalizeProxyMode(upstream.ProxyMode)
	upstream.ProxyURL = strings.TrimSpace(upstream.ProxyURL)
	if upstream.ProxyMode != ProxyModeCustom {
		upstream.ProxyURL = ""
	}
}

func validateUpstreamProxyConfig(upstream *UpstreamConfig) error {
	if upstream == nil {
		return proxyConfigErrorf("渠道配置不能为空")
	}
	switch normalizeProxyMode(upstream.ProxyMode) {
	case ProxyModeInherit, ProxyModeDirect:
		return nil
	case ProxyModeCustom:
		if strings.TrimSpace(upstream.ProxyURL) == "" {
			return proxyConfigErrorf("使用独立代理时必须填写代理地址")
		}
		if err := ValidateProxyURL(upstream.ProxyURL); err != nil {
			return fmt.Errorf("渠道代理配置无效: %w", err)
		}
		return nil
	default:
		return proxyConfigErrorf("不支持的渠道代理模式 %q", upstream.ProxyMode)
	}
}

// ResolveUpstreamProxyURL 解析渠道最终使用的代理地址；空字符串表示直连。
func (cm *ConfigManager) ResolveUpstreamProxyURL(upstream *UpstreamConfig) (string, error) {
	if upstream == nil {
		return "", proxyConfigErrorf("无法解析空渠道的代理配置")
	}

	switch normalizeProxyMode(upstream.ProxyMode) {
	case ProxyModeDirect:
		return "", nil
	case ProxyModeCustom:
		if err := validateUpstreamProxyConfig(upstream); err != nil {
			return "", err
		}
		return strings.TrimSpace(upstream.ProxyURL), nil
	case ProxyModeInherit:
		cm.mu.RLock()
		proxyURL := strings.TrimSpace(cm.config.Settings.Network.UpstreamProxyURL)
		cm.mu.RUnlock()
		if err := ValidateProxyURL(proxyURL); err != nil {
			return "", fmt.Errorf("全局上游代理配置无效: %w", err)
		}
		return proxyURL, nil
	default:
		return "", proxyConfigErrorf("不支持的渠道代理模式 %q", upstream.ProxyMode)
	}
}

func (cm *ConfigManager) GetSettings() SettingsConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config.Settings
}

func (cm *ConfigManager) UpdateNetworkSettings(settings NetworkSettings) error {
	settings.UpstreamProxyURL = strings.TrimSpace(settings.UpstreamProxyURL)
	if err := ValidateProxyURL(settings.UpstreamProxyURL); err != nil {
		return err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()
	previous := cm.config.Settings.Network
	cm.config.Settings.Network = settings
	if err := cm.saveConfigLocked(cm.config); err != nil {
		cm.config.Settings.Network = previous
		return err
	}
	return nil
}

func (cm *ConfigManager) validateProxyConfig() error {
	if err := ValidateProxyURL(cm.config.Settings.Network.UpstreamProxyURL); err != nil {
		return fmt.Errorf("全局上游代理配置无效: %w", err)
	}
	groups := [][]UpstreamConfig{
		cm.config.Upstream,
		cm.config.ResponsesUpstream,
		cm.config.GeminiUpstream,
		cm.config.ChatUpstream,
		cm.config.ImagesUpstream,
	}
	for _, upstreams := range groups {
		for index := range upstreams {
			if err := validateUpstreamProxyConfig(&upstreams[index]); err != nil {
				return fmt.Errorf("渠道 %q 的代理配置无效: %w", upstreams[index].Name, err)
			}
		}
	}
	return nil
}
