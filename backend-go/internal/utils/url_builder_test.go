package utils

import "testing"

// TestBuildUpstreamURL 覆盖"#"后缀与版本前缀约定的全部分支与边界。
func TestBuildUpstreamURL(t *testing.T) {
	tests := []struct {
		name          string
		baseURL       string
		versionPrefix string
		endpoint      string
		want          string
	}{
		// 裸域名：插入默认版本前缀
		{"bare_domain", "https://api.example.com", "/v1", "/chat/completions", "https://api.example.com/v1/chat/completions"},
		// 已含版本号：直接拼接
		{"versioned_v1", "https://api.example.com/v1", "/v1", "/messages", "https://api.example.com/v1/messages"},
		{"versioned_v1beta", "https://api.example.com/v1beta", "/v1", "/messages", "https://api.example.com/v1beta/messages"},
		{"versioned_v2alpha", "https://api.example.com/v2alpha", "/v1", "/x", "https://api.example.com/v2alpha/x"},
		// "#"后缀：剥离并跳过版本前缀
		{"hash_skip", "https://api.example.com#", "/v1", "/responses", "https://api.example.com/responses"},
		{"hash_skip_v1beta_default", "https://proxy.example.com#", "/v1beta", "/models/m:generateContent", "https://proxy.example.com/models/m:generateContent"},
		// 尾斜杠 / "#"+尾斜杠
		{"trailing_slash", "https://api.example.com/", "/v1", "/e", "https://api.example.com/v1/e"},
		{"hash_after_slash", "https://api.example.com/#", "/v1", "/e", "https://api.example.com/e"},
		// endpoint 规整：无前导斜杠 / 多重斜杠
		{"endpoint_no_leading_slash", "https://api.example.com", "/v1", "chat/completions", "https://api.example.com/v1/chat/completions"},
		{"endpoint_multi_slash", "https://api.example.com", "/v1", "//chat/completions", "https://api.example.com/v1/chat/completions"},
		// 版本检测不应匹配路径中非末尾的版本段
		{"version_not_at_end", "https://api.example.com/v1/foo", "/v1", "/e", "https://api.example.com/v1/foo/v1/e"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildUpstreamURL(tt.baseURL, tt.versionPrefix, tt.endpoint)
			if got != tt.want {
				t.Errorf("BuildUpstreamURL(%q, %q, %q) = %q, want %q",
					tt.baseURL, tt.versionPrefix, tt.endpoint, got, tt.want)
			}
		})
	}
}

// TestHasVersionSuffix 验证版本段检测的边界。
func TestHasVersionSuffix(t *testing.T) {
	cases := map[string]bool{
		"https://api.example.com/v1":      true,
		"https://api.example.com/v1beta":  true,
		"https://api.example.com/v2alpha": true,
		"https://api.example.com":         false,
		"https://api.example.com/v":       false, // 无数字
		"https://api.example.com/v1x2":    false, // 数字后的额外字符破坏匹配
	}
	for baseURL, want := range cases {
		if got := HasVersionSuffix(baseURL); got != want {
			t.Errorf("HasVersionSuffix(%q) = %v, want %v", baseURL, got, want)
		}
	}
}
