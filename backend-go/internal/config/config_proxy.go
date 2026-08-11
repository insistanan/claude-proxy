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
	return cloneSettingsConfig(cm.config.Settings)
}

// UpdateSettings 原子更新可由管理界面维护的全部全局设置。
func (cm *ConfigManager) UpdateSettings(settings SettingsConfig) error {
	settings.Network.UpstreamProxyURL = strings.TrimSpace(settings.Network.UpstreamProxyURL)
	if err := ValidateProxyURL(settings.Network.UpstreamProxyURL); err != nil {
		return err
	}
	if err := ValidateContentSafetyConfig(settings.ContentSafety); err != nil {
		return err
	}
	settings = cloneSettingsConfig(settings)

	cm.mu.Lock()
	defer cm.mu.Unlock()
	previous := cloneSettingsConfig(cm.config.Settings)
	cm.config.Settings = settings
	if err := cm.saveConfigLocked(cm.config); err != nil {
		cm.config.Settings = previous
		return err
	}
	return nil
}

func (cm *ConfigManager) UpdateNetworkSettings(settings NetworkSettings) error {
	cm.mu.RLock()
	currentURL := cm.config.Settings.Network.UpstreamProxyURL
	cm.mu.RUnlock()

	settings.UpstreamProxyURL = strings.TrimSpace(settings.UpstreamProxyURL)
	if settings.UpstreamProxyURL == "" && currentURL != "" {
		settings.UpstreamProxyURL = currentURL
	}
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

// ValidateContentSafetyConfig 校验内容安全规则选择，拒绝未知、空白或重复配置。
func ValidateContentSafetyConfig(settings ContentSafetyConfig) error {
	if settings.SensitiveWord.Enabled &&
		!settings.SensitiveWord.PornographyEnabled &&
		!settings.SensitiveWord.GamblingEnabled &&
		!settings.SensitiveWord.DrugsEnabled &&
		!settings.SensitiveWord.ViolenceTerrorEnabled &&
		!settings.SensitiveWord.PoliticalEnabled &&
		!settings.SensitiveWord.IllegalCrimeEnabled &&
		len(settings.SensitiveWord.CustomWords) == 0 {
		return proxyConfigErrorf("敏感词检测已启用，但未选择任何分类或自定义词")
	}
	if err := validateEnabledRuleSelection("敏感信息", settings.SensitiveInfo.Enabled, settings.SensitiveInfo.EnabledRules); err != nil {
		return err
	}
	if err := validateSafetyMode("敏感信息", settings.SensitiveInfo.Mode, true); err != nil {
		return err
	}
	if err := validateRuleSelection(
		"敏感信息",
		settings.SensitiveInfo.EnabledRules,
		map[string]struct{}{
			SensitiveInfoRulePhone:     {},
			SensitiveInfoRuleIDCard:    {},
			SensitiveInfoRuleEmail:     {},
			SensitiveInfoRuleIPAddress: {},
		},
	); err != nil {
		return err
	}
	if err := validateEnabledRuleSelection("凭据", settings.Credential.Enabled, settings.Credential.EnabledRules); err != nil {
		return err
	}
	if err := validateSafetyMode("凭据用户输入", settings.Credential.UserInputMode, true); err != nil {
		return err
	}
	if err := validateSafetyMode("凭据工具结果", settings.Credential.ToolResultMode, false); err != nil {
		return err
	}
	if err := validateSafetyMode("凭据工具参数", settings.Credential.ToolArgumentMode, false); err != nil {
		return err
	}
	if err := validateRuleSelection(
		"凭据",
		settings.Credential.EnabledRules,
		map[string]struct{}{
			CredentialRuleAPIKey:           {},
			CredentialRuleNamedSecret:      {},
			CredentialRulePrivateKey:       {},
			CredentialRuleConnectionString: {},
			CredentialRuleHighEntropy:      {},
		},
	); err != nil {
		return err
	}
	if err := validateEnabledRuleSelection("危险命令", settings.DangerousCmd.Enabled, settings.DangerousCmd.EnabledRules); err != nil {
		return err
	}
	if err := validateRuleSelection(
		"危险命令",
		settings.DangerousCmd.EnabledRules,
		map[string]struct{}{
			DangerousCmdRuleDestructive:          {},
			DangerousCmdRuleDownloadExecute:      {},
			DangerousCmdRuleReverseShell:         {},
			DangerousCmdRulePrivilegeEscalation:  {},
			DangerousCmdRuleEnvironmentTampering: {},
		},
	); err != nil {
		return err
	}

	seenWords := make(map[string]struct{}, len(settings.SensitiveWord.CustomWords))
	for _, word := range settings.SensitiveWord.CustomWords {
		if strings.TrimSpace(word) == "" {
			return proxyConfigErrorf("自定义敏感词不能为空")
		}
		if _, exists := seenWords[word]; exists {
			return proxyConfigErrorf("自定义敏感词 %q 重复", word)
		}
		seenWords[word] = struct{}{}
	}
	return nil
}

func validateEnabledRuleSelection(label string, enabled bool, rules []string) error {
	if enabled && len(rules) == 0 {
		return proxyConfigErrorf("%s已启用，但未选择任何规则", label)
	}
	return nil
}

func validateSafetyMode(label, mode string, allowMask bool) error {
	switch mode {
	case ContentSafetyModeAudit, ContentSafetyModeBlock:
		return nil
	case ContentSafetyModeMask:
		if allowMask {
			return nil
		}
	}
	return proxyConfigErrorf("%s处理模式 %q 无效", label, mode)
}

func validateRuleSelection(label string, rules []string, allowed map[string]struct{}) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule) == "" {
			return proxyConfigErrorf("%s规则名不能为空", label)
		}
		if _, exists := allowed[rule]; !exists {
			return proxyConfigErrorf("不支持的%s规则 %q", label, rule)
		}
		if _, exists := seen[rule]; exists {
			return proxyConfigErrorf("%s规则 %q 重复", label, rule)
		}
		seen[rule] = struct{}{}
	}
	return nil
}

func cloneSettingsConfig(settings SettingsConfig) SettingsConfig {
	settings.ContentSafety.SensitiveWord.CustomWords = append([]string{}, settings.ContentSafety.SensitiveWord.CustomWords...)
	settings.ContentSafety.SensitiveInfo.EnabledRules = append([]string{}, settings.ContentSafety.SensitiveInfo.EnabledRules...)
	settings.ContentSafety.Credential.EnabledRules = append([]string{}, settings.ContentSafety.Credential.EnabledRules...)
	settings.ContentSafety.DangerousCmd.EnabledRules = append([]string{}, settings.ContentSafety.DangerousCmd.EnabledRules...)
	return settings
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
