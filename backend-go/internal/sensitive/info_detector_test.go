package sensitive

import (
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

func TestInfoDetectorMasksEveryRule(t *testing.T) {
	detector, err := NewInfoDetector(defaultSensitiveInfoSettings())
	if err != nil {
		t.Fatalf("创建检测器失败: %v", err)
	}

	tests := []struct {
		name     string
		rule     string
		input    string
		expected string
	}{
		{name: "手机号一", rule: config.SensitiveInfoRulePhone, input: "18012345523", expected: "[MASKED_PII:phone]"},
		{name: "手机号二", rule: config.SensitiveInfoRulePhone, input: "13900001234", expected: "[MASKED_PII:phone]"},
		{name: "手机号三", rule: config.SensitiveInfoRulePhone, input: "16688889999", expected: "[MASKED_PII:phone]"},
		{name: "身份证数字尾", rule: config.SensitiveInfoRuleIDCard, input: "110105194912310038", expected: "[MASKED_PII:id_card]"},
		{name: "身份证大写X", rule: config.SensitiveInfoRuleIDCard, input: "11010519491231002X", expected: "[MASKED_PII:id_card]"},
		{name: "身份证小写x", rule: config.SensitiveInfoRuleIDCard, input: "11010519491231002x", expected: "[MASKED_PII:id_card]"},
		{name: "普通邮箱", rule: config.SensitiveInfoRuleEmail, input: "user@example.com", expected: "[MASKED_PII:email]"},
		{name: "单字邮箱", rule: config.SensitiveInfoRuleEmail, input: "a@b.co", expected: "[MASKED_PII:email]"},
		{name: "带标签邮箱", rule: config.SensitiveInfoRuleEmail, input: "first.last+tag@sub.example.org", expected: "[MASKED_PII:email]"},
		{name: "内网IPv4", rule: config.SensitiveInfoRuleIPAddress, input: "192.168.1.1", expected: "[MASKED_PII:ip_address]"},
		{name: "公网IPv4", rule: config.SensitiveInfoRuleIPAddress, input: "8.8.8.8", expected: "[MASKED_PII:ip_address]"},
		{name: "IPv6", rule: config.SensitiveInfoRuleIPAddress, input: "2001:db8::1", expected: "[MASKED_PII:ip_address]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			masked, matches := detector.Mask(test.input)
			if masked != test.expected {
				t.Fatalf("掩码结果错误: got %q, want %q", masked, test.expected)
			}
			if len(matches) != 1 || matches[0].Rule != test.rule || matches[0].Original != test.input {
				t.Fatalf("命中明细错误: %+v", matches)
			}
			if matches[0].Start != 0 || matches[0].End != len(test.input) {
				t.Fatalf("命中偏移错误: %+v", matches[0])
			}
		})
	}
}

func TestInfoDetectorIgnoresInvalidIDLikeNumbers(t *testing.T) {
	detector, err := NewInfoDetector(config.SensitiveInfoConfig{
		Enabled:      true,
		Mode:         config.ContentSafetyModeMask,
		EnabledRules: []string{config.SensitiveInfoRuleIDCard},
	})
	if err != nil {
		t.Fatalf("创建身份证检测器失败: %v", err)
	}
	input := "订单号 123456789012345678，日期错误 11010520261301002X"
	masked, matches := detector.Mask(input)
	if masked != input || len(matches) != 0 {
		t.Fatalf("普通十八位数字不应被当成身份证: %q %+v", masked, matches)
	}
}

func TestInfoDetectorRuleSelectionAndInvalidValues(t *testing.T) {
	settings := config.SensitiveInfoConfig{
		Enabled:      true,
		EnabledRules: []string{config.SensitiveInfoRulePhone},
	}
	detector, err := NewInfoDetector(settings)
	if err != nil {
		t.Fatalf("创建检测器失败: %v", err)
	}
	input := "key=sk-1234567890abcdef phone=18012345523 ip=999.1.1.1"
	masked, matches := detector.Mask(input)
	if masked != "key=sk-1234567890abcdef phone=[MASKED_PII:phone] ip=999.1.1.1" {
		t.Fatalf("规则选择错误: %q", masked)
	}
	if len(matches) != 1 || matches[0].Rule != config.SensitiveInfoRulePhone {
		t.Fatalf("规则选择命中错误: %+v", matches)
	}

	if _, err := NewInfoDetector(config.SensitiveInfoConfig{Enabled: true, EnabledRules: []string{"unknown"}}); err == nil {
		t.Fatal("未知规则应返回错误")
	}
	if _, err := NewInfoDetector(config.SensitiveInfoConfig{Enabled: true, EnabledRules: []string{config.SensitiveInfoRulePhone, config.SensitiveInfoRulePhone}}); err == nil {
		t.Fatal("重复规则应返回错误")
	}
}

func TestInfoDetectorMasksMultipleValuesDeterministically(t *testing.T) {
	detector, err := NewInfoDetector(defaultSensitiveInfoSettings())
	if err != nil {
		t.Fatalf("创建检测器失败: %v", err)
	}
	input := "联系 user@example.com，电话 18012345523，服务地址 2001:db8::1。"
	masked, matches := detector.Mask(input)
	expected := "联系 [MASKED_PII:email]，电话 [MASKED_PII:phone]，服务地址 [MASKED_PII:ip_address]。"
	if masked != expected {
		t.Fatalf("多值掩码错误: got %q, want %q", masked, expected)
	}
	if len(matches) != 3 {
		t.Fatalf("多值命中数量错误: %+v", matches)
	}
	for index := 1; index < len(matches); index++ {
		if matches[index-1].End > matches[index].Start {
			t.Fatalf("命中发生重叠或乱序: %+v", matches)
		}
	}
}

func TestInfoDetectorDisabled(t *testing.T) {
	settings := defaultSensitiveInfoSettings()
	settings.Enabled = false
	detector, err := NewInfoDetector(settings)
	if err != nil {
		t.Fatalf("创建关闭的检测器失败: %v", err)
	}
	input := "user@example.com 18012345523"
	masked, matches := detector.Mask(input)
	if masked != input || len(matches) != 0 {
		t.Fatalf("关闭后仍发生掩码: %q %+v", masked, matches)
	}
}

func defaultSensitiveInfoSettings() config.SensitiveInfoConfig {
	return config.SensitiveInfoConfig{
		Enabled: true,
		Mode:    config.ContentSafetyModeMask,
		EnabledRules: []string{
			config.SensitiveInfoRulePhone,
			config.SensitiveInfoRuleIDCard,
			config.SensitiveInfoRuleEmail,
			config.SensitiveInfoRuleIPAddress,
		},
	}
}
