package utils

import (
	"regexp"
	"strings"
)

// versionSuffixPattern 匹配以版本号结尾的 baseURL（/v1, /v2, /v1beta, /v2alpha 等）。
// 上游渠道地址约定：BaseURL 以 "#" 结尾表示"地址已完整，跳过自动版本前缀"。
var versionSuffixPattern = regexp.MustCompile(`/v\d+[a-z]*$`)

// HasVersionSuffix 判断（已规整的）baseURL 是否以版本号段结尾。
func HasVersionSuffix(baseURL string) bool {
	return versionSuffixPattern.MatchString(baseURL)
}

// BuildUpstreamURL 按上游渠道地址约定拼接完整 URL，是"#"后缀与版本前缀
// 规则的唯一出处：
//
//  1. baseURL 以 "#" 结尾 → 剥离 "#"，跳过自动版本前缀（地址视为完整）；
//  2. 剥离末尾 "/"；
//  3. baseURL 已含版本号段（如 /v1、/v2、/v1beta）→ 直接拼接 endpoint；
//  4. 否则在 endpoint 前插入 defaultVersionPrefix（如 "/v1"、"/v1beta"）。
//
// endpoint 会被规整为以单个 "/" 开头。
func BuildUpstreamURL(baseURL, defaultVersionPrefix, endpoint string) string {
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	endpoint = "/" + strings.TrimLeft(endpoint, "/")

	if skipVersionPrefix || versionSuffixPattern.MatchString(baseURL) {
		return baseURL + endpoint
	}
	return baseURL + defaultVersionPrefix + endpoint
}
