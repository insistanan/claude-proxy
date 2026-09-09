package sensitive

import (
	"strconv"
	"strings"
	"testing"
)

func TestVaultMasksReusesAndRestoresExactValues(t *testing.T) {
	vault := NewVault()
	original := `p@ss"word`
	text := "first=" + original + ", second=" + original
	matches := []RedactionMatch{
		{Type: "SECRET", Rule: "named_secret", Original: original, Start: 6, End: 6 + len(original)},
		{Type: "SECRET", Rule: "named_secret", Original: original, Start: len("first=") + len(original) + len(", second="), End: len(text)},
	}
	masked, err := vault.Mask(text, matches)
	if err != nil {
		t.Fatalf("脱敏失败: %v", err)
	}
	if strings.Contains(masked, original) || strings.Count(masked, "{{SECRET_") != 2 {
		t.Fatalf("脱敏结果错误: %q", masked)
	}
	parts := strings.Split(masked, ", second=")
	if len(parts) != 2 || strings.TrimPrefix(parts[0], "first=") != parts[1] {
		t.Fatalf("同一原文未复用占位符: %q", masked)
	}
	if restored := vault.Restore(masked); restored != text {
		t.Fatalf("还原结果 = %q，期望 %q", restored, text)
	}
	if restored := vault.Restore("{{SECRET_UNKNOWN}}"); restored != "{{SECRET_UNKNOWN}}" {
		t.Fatalf("未知占位符不应还原: %q", restored)
	}
}

func TestStreamRestorerHandlesEveryPlaceholderSplit(t *testing.T) {
	vault := NewVault()
	original := "18012345523"
	placeholder, err := vault.Mask(original, []RedactionMatch{{
		Type: "PHONE", Rule: "phone", Original: original, Start: 0, End: len(original),
	}})
	if err != nil {
		t.Fatalf("创建占位符失败: %v", err)
	}
	for split := 0; split <= len(placeholder); split++ {
		t.Run(strconv.Itoa(split), func(t *testing.T) {
			restorer := NewStreamRestorer(vault)
			got := restorer.Feed(placeholder[:split]) + restorer.Feed(placeholder[split:]) + restorer.Flush()
			if got != original {
				t.Fatalf("split=%d 还原结果 = %q", split, got)
			}
		})
	}

	restorer := NewStreamRestorer(vault)
	prefix := placeholder[:len(placeholder)/2]
	if got := restorer.Feed(prefix); got != "" {
		t.Fatalf("未完成占位符不应提前输出: %q", got)
	}
	if got := restorer.Flush(); got != prefix {
		t.Fatalf("结束时应原样释放残片: %q", got)
	}
}

func TestVaultRestoreJSONEscapesOriginalText(t *testing.T) {
	vault := NewVault()
	original := "line 1\n\"line 2\""
	placeholder, err := vault.Mask(original, []RedactionMatch{{Type: "SECRET", Rule: "test", Original: original, Start: 0, End: len(original)}})
	if err != nil {
		t.Fatalf("创建占位符失败: %v", err)
	}
	body, err := vault.RestoreJSON([]byte(`{"text":"` + placeholder + `","nested":{"arguments":"` + placeholder + `"}}`))
	if err != nil {
		t.Fatalf("递归还原失败: %v", err)
	}
	if strings.Count(string(body), `line 1\n\"line 2\"`) != 2 {
		t.Fatalf("JSON 转义或递归还原错误: %s", body)
	}
	topLevel, err := vault.RestoreJSON([]byte(strconv.Quote(placeholder)))
	if err != nil {
		t.Fatalf("还原顶层 JSON 字符串失败: %v", err)
	}
	if got := string(topLevel); got != strconv.Quote(original) {
		t.Fatalf("顶层 JSON 字符串还原结果 = %s", got)
	}
}

func TestVaultMappingsAreRequestLocal(t *testing.T) {
	first := NewVault()
	second := NewVault()
	original := "user@example.com"
	match := []RedactionMatch{{Type: "EMAIL", Rule: "email", Original: original, Start: 0, End: len(original)}}
	firstPlaceholder, err := first.Mask(original, match)
	if err != nil {
		t.Fatalf("首次脱敏失败: %v", err)
	}
	secondPlaceholder, err := second.Mask(original, match)
	if err != nil {
		t.Fatalf("第二次脱敏失败: %v", err)
	}
	if firstPlaceholder == secondPlaceholder {
		t.Fatal("不同请求不应复用随机占位符")
	}
	if got := second.Restore(firstPlaceholder); got != firstPlaceholder {
		t.Fatalf("请求间发生串值: %q", got)
	}
}

func TestVaultMaskKnownUsesExistingRequestMapping(t *testing.T) {
	vault := NewVault()
	original := "user@example.com"
	placeholder, err := vault.Mask(original, []RedactionMatch{{
		Type: "EMAIL", Rule: "email", Original: original, Start: 0, End: len(original),
	}})
	if err != nil {
		t.Fatalf("创建占位符失败: %v", err)
	}
	if got := vault.MaskKnown("contact " + original); got != "contact "+placeholder {
		t.Fatalf("既有映射脱敏结果 = %q", got)
	}
}

func TestVaultClearReleasesMappings(t *testing.T) {
	vault := NewVault()
	original := "18012345523"
	placeholder, err := vault.Mask(original, []RedactionMatch{{
		Type: "PHONE", Rule: "phone", Original: original, Start: 0, End: len(original),
	}})
	if err != nil {
		t.Fatalf("创建占位符失败: %v", err)
	}
	vault.ReserveText("reserved")
	vault.Clear()
	if !vault.Empty() {
		t.Fatal("清理后 Vault 应为空")
	}
	if got := vault.Restore(placeholder); got != placeholder {
		t.Fatalf("清理后不应继续还原占位符: %q", got)
	}
}
